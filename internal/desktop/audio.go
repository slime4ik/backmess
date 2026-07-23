package desktop

import (
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/gen2brain/malgo"
	opus "gopkg.in/hraban/opus.v2"
)

const (
	sampleRate = 48000
	frameSize  = 960 // 20 мс моно — стандартный кадр Opus
	maxFrame   = 5760
	// ~0.4 с после спада уровня продолжаем передавать: иначе шумодав срезает
	// concы слов и речь звучит рубленой
	gateTailFrames = 20
	// кадр раз в ~секунду при полной тишине: держит NAT и ICE живыми
	keepAliveFrames = 50
)

// AudioEngine: захват микрофона → Opus 32kbps VoIP → наружу через OnMicFrame;
// входящие opus-пакеты декодируются по источникам и сводятся микшером
// в один плейбек-поток. Всё на miniaudio (malgo) — без системных зависимостей.
type AudioEngine struct {
	ctx      *malgo.AllocatedContext
	capture  *malgo.Device
	playback *malgo.Device

	// выбранные устройства (пустая строка — системное по умолчанию)
	captureName  string
	playbackName string

	enc    *opus.Encoder
	micAcc []int16
	encBuf []byte
	micLvl atomic.Int32
	muted  atomic.Bool
	deaf   atomic.Bool

	gate       atomic.Int32 // ручной порог чувствительности в единицах пика
	autoGate   atomic.Bool  // авто-порог по уровню фона
	noiseFloor float64      // оценка фона для авто-порога (только из onCapture)
	gateTail   int          // сколько кадров ещё пропускать после спада уровня
	keepAlive  int          // кадров прошло с последней отправки

	dnMu    sync.Mutex
	denoise *denoiser // шумоподавление; nil = выключено

	// OnMicFrame вызывается из аудио-потока: копия кадра уже сделана
	OnMicFrame func(data []byte)

	mu      sync.Mutex
	sources map[string]*audioSource
	userVol map[string]int // выбранная юзером громкость участников

	beepMu  sync.Mutex
	beepBuf []int16
	mixAcc  []int32
}

type audioSource struct {
	dec    *opus.Decoder
	frames chan []int16
	rem    []int16
	pcm    []int16
	lvl    atomic.Int32
	vol    atomic.Int32 // громкость в процентах, 100 = как есть
}

// vol хранит громкость+1 в процентах: 0 в atomic значит «не задано» → 100%,
// а осознанный ноль (полная тишина) кодируется как 1 → 0%.
func (s *audioSource) volume() float64 {
	v := s.vol.Load()
	if v == 0 {
		return 1 // по умолчанию как есть
	}
	return float64(v-1) / 100
}

func NewAudioEngine() (*AudioEngine, error) {
	enc, err := opus.NewEncoder(sampleRate, 1, opus.AppVoIP)
	if err != nil {
		return nil, fmt.Errorf("opus encoder: %w", err)
	}
	enc.SetBitrate(32000)
	enc.SetInBandFEC(true)

	e := &AudioEngine{
		enc:     enc,
		encBuf:  make([]byte, 4000),
		sources: map[string]*audioSource{},
		userVol: map[string]int{},
		mixAcc:  make([]int32, 8192),
	}

	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("audio context: %w", err)
	}
	e.ctx = ctx
	return e, nil
}

// Device — звуковое устройство для выпадающего списка в настройках.
type Device struct {
	Name    string
	Default bool
	id      malgo.DeviceID
}

// Devices перечисляет устройства: наушники втыкают и вытыкают постоянно,
// и без возможности переключиться приложение становится бесполезным, если
// система выбрала не то устройство.
func (e *AudioEngine) Devices(capture bool) []Device {
	kind := malgo.Playback
	if capture {
		kind = malgo.Capture
	}
	infos, err := e.ctx.Devices(kind)
	if err != nil {
		return nil
	}
	out := make([]Device, 0, len(infos))
	for _, d := range infos {
		out = append(out, Device{Name: d.Name(), Default: d.IsDefault != 0, id: d.ID})
	}
	return out
}

// findDevice ищет устройство по имени; пустое имя или пропажа железа —
// возвращаем nil, что означает «системное по умолчанию».
func (e *AudioEngine) findDevice(name string, capture bool) *malgo.DeviceID {
	if name == "" {
		return nil
	}
	for _, d := range e.Devices(capture) {
		if d.Name == name {
			id := d.id
			return &id
		}
	}
	return nil
}

// Start поднимает устройства. Ошибка захвата не фатальна (нет микрофона /
// нет разрешения) — тогда работаем слушателем, о чём сообщаем наружу.
func (e *AudioEngine) Start() (micOK bool, err error) {
	if err := e.StartPlayback(""); err != nil {
		return false, err
	}
	return e.StartCapture("") == nil, nil
}

// StartPlayback (пере)открывает вывод звука. Пустое имя — системное по умолчанию.
func (e *AudioEngine) StartPlayback(name string) error {
	cfg := malgo.DefaultDeviceConfig(malgo.Playback)
	cfg.Playback.Format = malgo.FormatS16
	cfg.Playback.Channels = 1
	cfg.SampleRate = sampleRate
	if id := e.findDevice(name, false); id != nil {
		cfg.Playback.DeviceID = id.Pointer()
	}
	dev, err := malgo.InitDevice(e.ctx.Context, cfg, malgo.DeviceCallbacks{Data: e.onPlayback})
	if err != nil {
		return fmt.Errorf("плейбек: %w", err)
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return fmt.Errorf("плейбек: %w", err)
	}
	if old := e.playback; old != nil {
		old.Uninit()
	}
	e.playback = dev
	e.playbackName = name
	return nil
}

// StartCapture (пере)открывает микрофон. Пустое имя — системный по умолчанию.
func (e *AudioEngine) StartCapture(name string) error {
	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = 1
	cfg.SampleRate = sampleRate
	if id := e.findDevice(name, true); id != nil {
		cfg.Capture.DeviceID = id.Pointer()
	}
	dev, err := malgo.InitDevice(e.ctx.Context, cfg, malgo.DeviceCallbacks{Data: e.onCapture})
	if err != nil {
		return err
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return err
	}
	if old := e.capture; old != nil {
		old.Uninit()
	}
	e.capture = dev
	e.captureName = name
	return nil
}

func (e *AudioEngine) CaptureName() string  { return e.captureName }
func (e *AudioEngine) PlaybackName() string { return e.playbackName }

func (e *AudioEngine) Close() {
	if e.capture != nil {
		e.capture.Uninit()
	}
	if e.playback != nil {
		e.playback.Uninit()
	}
	if e.ctx != nil {
		e.ctx.Uninit()
		e.ctx.Free()
	}
}

func (e *AudioEngine) SetMuted(m bool)    { e.muted.Store(m) }
func (e *AudioEngine) SetDeafened(d bool) { e.deaf.Store(d) }

func s16(b []byte) []int16 {
	if len(b) < 2 {
		return nil
	}
	return unsafe.Slice((*int16)(unsafe.Pointer(&b[0])), len(b)/2)
}

func peak16(pcm []int16) int32 {
	var p int32
	for _, v := range pcm {
		a := int32(v)
		if a < 0 {
			a = -a
		}
		if a > p {
			p = a
		}
	}
	return p
}

// Gate — ручной порог чувствительности микрофона (0..1 от максимума). Всё тише
// порога не кодируется и не уходит в сеть: это и шумодав, и экономия.
func (e *AudioEngine) SetGate(v float64) {
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	e.gate.Store(int32(v * 32768))
}

func (e *AudioEngine) Gate() float64 { return float64(e.gate.Load()) / 32768 }

// SetAutoGate включает автоматический порог: приложение само оценивает уровень
// фона и ставит порог чуть выше него. Ручной ползунок при этом не нужен.
func (e *AudioEngine) SetAutoGate(on bool) { e.autoGate.Store(on) }
func (e *AudioEngine) AutoGate() bool      { return e.autoGate.Load() }

// SetDenoise включает/выключает шумоподавление. Денойзер создаётся лениво и
// живёт, пока включён; выключение освобождает его буферы.
func (e *AudioEngine) SetDenoise(on bool) {
	e.dnMu.Lock()
	defer e.dnMu.Unlock()
	if on && e.denoise == nil {
		e.denoise = newDenoiser()
	} else if !on {
		e.denoise = nil
	}
}

func (e *AudioEngine) Denoise() bool {
	e.dnMu.Lock()
	defer e.dnMu.Unlock()
	return e.denoise != nil
}

// threshold возвращает актуальный порог в единицах пика. В ручном режиме — то,
// что выставил юзер. В авто — фон, который отслеживается по тихим кадрам:
// быстро вниз (сразу видим, что стало тише), медленно вверх (не считаем
// собственную речь за фон), плюс множитель, чтобы речь уверенно превышала порог.
func (e *AudioEngine) threshold(peak int32, muted bool) int32 {
	if !e.autoGate.Load() {
		return e.gate.Load()
	}
	fp := float64(peak)
	if fp < e.noiseFloor {
		e.noiseFloor = 0.6*e.noiseFloor + 0.4*fp
	} else if !muted {
		e.noiseFloor = 0.999*e.noiseFloor + 0.001*fp
	}
	thr := e.noiseFloor*2.5 + 250 // небольшой абсолютный минимум против полной тишины
	if thr > 32768 {
		thr = 32768
	}
	return int32(thr)
}

func (e *AudioEngine) onCapture(_, in []byte, _ uint32) {
	e.micAcc = append(e.micAcc, s16(in)...)
	for len(e.micAcc) >= frameSize {
		frame := e.micAcc[:frameSize]
		muted := e.muted.Load()

		// Шумоподавление — до всего остального, чтобы и порог, и уровень видели
		// уже очищенный звук. Обработчик и решение вкл/выкл берём под локом
		// один раз: их могут менять из UI.
		if !muted {
			e.dnMu.Lock()
			dn := e.denoise
			e.dnMu.Unlock()
			if dn != nil {
				cleaned := dn.Process(frame)
				copy(frame, cleaned)
			}
		}

		peak := peak16(frame)
		thr := e.threshold(peak, muted)

		// Решаем, отправлять ли кадр. Пока человек молчит (или сидит в муте),
		// звук не кодируется и не уходит в сеть — это и шумодав, и экономия
		// трафика с процессором.
		//
		// Но замолкать НАСОВСЕМ нельзя: без исходящих пакетов NAT через пару
		// минут закрывает сопоставление, соединение рвётся и человека выносит
		// из канала. Поэтому при тишине раз в ~секунду всё равно отправляем
		// кадр тишины — он занимает считанные байты, зато поток жив.
		send := false
		silent := false
		switch {
		case muted:
			e.micLvl.Store(0)
			e.gateTail = 0
			silent = true
		case peak >= thr:
			e.micLvl.Store(peak)
			e.gateTail = gateTailFrames
			send = true
		default:
			// ниже порога: доигрываем «хвост», чтобы не рубить концы слов
			e.micLvl.Store(0)
			if e.gateTail > 0 {
				e.gateTail--
				send = true
			} else {
				silent = true
			}
		}

		e.keepAlive++
		if silent && e.keepAlive >= keepAliveFrames {
			send, silent = true, true
		} else if silent {
			send = false
		}

		if send {
			e.keepAlive = 0
			if silent {
				for i := range frame {
					frame[i] = 0
				}
			}
			if n, err := e.enc.Encode(frame, e.encBuf); err == nil && e.OnMicFrame != nil {
				e.OnMicFrame(append([]byte(nil), e.encBuf[:n]...))
			}
		}
		e.micAcc = append(e.micAcc[:0], e.micAcc[frameSize:]...)
	}
}

func (e *AudioEngine) onPlayback(out, _ []byte, _ uint32) {
	dst := s16(out)
	for i := range dst {
		dst[i] = 0
	}
	if len(dst) == 0 {
		return
	}
	if cap(e.mixAcc) < len(dst) {
		e.mixAcc = make([]int32, len(dst))
	}
	acc := e.mixAcc[:len(dst)]
	for i := range acc {
		acc[i] = 0
	}

	if !e.deaf.Load() {
		e.mu.Lock()
		for _, src := range e.sources {
			src.mixInto(acc)
		}
		e.mu.Unlock()
	}

	// писки интерфейса слышны даже в «заглушен»
	e.beepMu.Lock()
	n := len(e.beepBuf)
	if n > len(acc) {
		n = len(acc)
	}
	for i := 0; i < n; i++ {
		acc[i] += int32(e.beepBuf[i])
	}
	e.beepBuf = e.beepBuf[n:]
	e.beepMu.Unlock()

	for i, v := range acc {
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		dst[i] = int16(v)
	}
}

func (s *audioSource) mixInto(acc []int32) {
	need := len(acc)
	got := 0
	for got < need {
		if len(s.rem) == 0 {
			select {
			case f := <-s.frames:
				s.rem = f
			default:
				return
			}
		}
		n := len(s.rem)
		if n > need-got {
			n = need - got
		}
		vol := s.volume()
		if vol == 1 {
			for i := 0; i < n; i++ {
				acc[got+i] += int32(s.rem[i])
			}
		} else {
			for i := 0; i < n; i++ {
				acc[got+i] += int32(float64(s.rem[i]) * vol)
			}
		}
		s.rem = s.rem[n:]
		got += n
	}
}

// Ingest — входящий opus-пакет от участника id (вызывается из ридера трека).
func (e *AudioEngine) Ingest(id string, payload []byte) {
	if len(payload) == 0 {
		return
	}
	e.mu.Lock()
	src := e.sources[id]
	if src == nil {
		dec, err := opus.NewDecoder(sampleRate, 1)
		if err != nil {
			e.mu.Unlock()
			return
		}
		src = &audioSource{dec: dec, frames: make(chan []int16, 8), pcm: make([]int16, maxFrame)}
		if v, ok := e.userVol[id]; ok {
			src.vol.Store(int32(v + 1)) // +1: см. audioSource.volume
		}
		e.sources[id] = src
	}
	e.mu.Unlock()

	n, err := src.dec.Decode(payload, src.pcm)
	if err != nil || n == 0 {
		return
	}
	src.lvl.Store(peak16(src.pcm[:n]))
	frame := append([]int16(nil), src.pcm[:n]...)
	select {
	case src.frames <- frame:
	default: // отстаём — кадр дропаем, лаг важнее плавности
	}
}

// SetUserVolume — индивидуальная громкость участника в процентах (100 = обычная).
// Хранится вместе с источником, применяется прямо в микшере.
func (e *AudioEngine) SetUserVolume(id string, percent int) {
	if percent < 0 {
		percent = 0
	}
	e.mu.Lock()
	e.userVol[id] = percent
	if src := e.sources[id]; src != nil {
		src.vol.Store(int32(percent + 1)) // +1: см. audioSource.volume
	}
	e.mu.Unlock()
}

func (e *AudioEngine) UserVolume(id string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if v, ok := e.userVol[id]; ok {
		return v
	}
	return 100
}

func (e *AudioEngine) RemoveSource(id string) {
	e.mu.Lock()
	delete(e.sources, id)
	e.mu.Unlock()
}

func (e *AudioEngine) ClearSources() {
	e.mu.Lock()
	e.sources = map[string]*audioSource{}
	e.mu.Unlock()
}

// Levels — пиковые уровни для индикации «кто говорит»; ключ "" — свой микрофон.
func (e *AudioEngine) Levels() map[string]float64 {
	out := map[string]float64{"": float64(e.micLvl.Swap(0)) / 32768}
	e.mu.Lock()
	for id, s := range e.sources {
		out[id] = float64(s.lvl.Swap(0)) / 32768
	}
	e.mu.Unlock()
	return out
}
