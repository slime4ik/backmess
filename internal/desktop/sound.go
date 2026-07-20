package desktop

import (
	"embed"
	"encoding/binary"
	"sync"
)

// Звуки лежат уже раскодированными в сырой PCM (48 кГц, моно, 16 бит) — ровно
// в формате микшера. Поэтому в приложении нет ни MP3-декодера, ни ресемплера
// времени выполнения: готовый звук просто подмешивается в буфер вывода.
//
//go:embed sounds/*.pcm
var soundFS embed.FS

// Исходники: два семпла, из них собираются все сигналы.
const (
	sampleBig   = "henta_ahh" // выразительный, ~0.5 с
	sampleSmall = "kentut"    // короткий и тихий, ~0.2 с
)

// События. Приложение почти всегда свёрнуто за игрой или браузером, поэтому
// каждое событие обязано опознаваться на слух, не глядя в окно.
//
// Правило простое и одинаковое везде:
//   - выше по тону / восходящий рисунок = появление, вход, включение
//   - ниже по тону / нисходящий         = уход, выход, выключение
//   - крупный семпл = про людей, мелкий = про тебя и твои действия
const (
	SoundJoin     = "join"     // кто-то зашёл в голосовой
	SoundLeave    = "leave"    // кто-то вышел
	SoundConnect  = "connect"  // ты подключился к каналу
	SoundHangup   = "hangup"   // ты отключился или связь оборвалась
	SoundMessage  = "message"  // новое сообщение
	SoundReply    = "reply"    // ответили именно тебе
	SoundMuteOn   = "muteon"   // выключил микрофон
	SoundMuteOff  = "muteoff"  // включил микрофон
	SoundDeafOn   = "deafon"   // выключил звук совсем
	SoundDeafOff  = "deafoff"  // вернул звук
	SoundNewGroup = "newgroup" // добавилась группа/канал
)

// step — один семпл в составе сигнала.
type step struct {
	sample string
	pitch  float64 // 1.0 — как в исходнике, больше — выше и короче
	gain   float64
	offset int // сдвиг от начала сигнала, в сэмплах (48 на миллисекунду)
}

const ms = sampleRate / 1000

// Рисунки сигналов. Двойной семпл — это «про тебя»: свои действия отличаются
// от чужих не только тоном, но и ритмом, чтобы не путать «я вышел» и «кто-то
// вышел» в разгар катки.
var soundEvents = map[string][]step{
	SoundJoin:  {{sampleBig, 1.00, 0.70, 0}},
	SoundLeave: {{sampleBig, 0.78, 0.60, 0}},

	SoundConnect: {
		{sampleSmall, 1.00, 0.55, 0},
		{sampleSmall, 1.30, 0.55, 110 * ms},
	},
	SoundHangup: {
		{sampleSmall, 1.30, 0.55, 0},
		{sampleSmall, 0.85, 0.55, 110 * ms},
	},

	SoundMessage: {{sampleSmall, 1.15, 0.45, 0}},
	SoundReply: {
		{sampleSmall, 1.25, 0.50, 0},
		{sampleBig, 1.05, 0.65, 90 * ms},
	},

	// свои переключатели — самые частые, поэтому тихие и очень короткие
	SoundMuteOn:  {{sampleSmall, 0.75, 0.30, 0}},
	SoundMuteOff: {{sampleSmall, 1.20, 0.30, 0}},
	SoundDeafOn: {
		{sampleSmall, 0.90, 0.28, 0},
		{sampleSmall, 0.70, 0.28, 90 * ms},
	},
	SoundDeafOff: {
		{sampleSmall, 0.95, 0.28, 0},
		{sampleSmall, 1.25, 0.28, 90 * ms},
	},

	SoundNewGroup: {{sampleSmall, 1.35, 0.45, 0}},
}

var (
	soundsOnce sync.Once
	samples    map[string][]int16 // исходники
	events     map[string][]int16 // готовые сигналы, собранные один раз
)

// buildSounds раскладывает семплы и заранее собирает все сигналы целиком.
// Собираем на старте, а не при каждом воспроизведении: событие может
// прилететь из аудио-потока, где считать что-либо лишнее нельзя.
func buildSounds() {
	samples = map[string][]int16{}
	entries, err := soundFS.ReadDir("sounds")
	if err != nil {
		return
	}
	for _, e := range entries {
		b, err := soundFS.ReadFile("sounds/" + e.Name())
		if err != nil {
			continue
		}
		pcm := make([]int16, len(b)/2)
		for i := range pcm {
			pcm[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
		}
		samples[e.Name()[:len(e.Name())-len(".pcm")]] = pcm
	}

	events = map[string][]int16{}
	for name, steps := range soundEvents {
		if mixed := buildEvent(steps); len(mixed) > 0 {
			events[name] = mixed
		}
	}
}

func buildEvent(steps []step) []int16 {
	parts := make([][]int16, 0, len(steps))
	total := 0
	for _, s := range steps {
		src := samples[s.sample]
		if len(src) == 0 {
			parts = append(parts, nil)
			continue
		}
		p := resample(src, s.pitch)
		parts = append(parts, p)
		if end := s.offset + len(p); end > total {
			total = end
		}
	}
	if total == 0 {
		return nil
	}
	out := make([]int16, total)
	for i, s := range steps {
		p := parts[i]
		for j, v := range p {
			out[s.offset+j] = clamp16(int32(out[s.offset+j]) + int32(float64(v)*s.gain))
		}
	}
	return out
}

// resample меняет высоту звука: ratio>1 — выше и короче. Линейная
// интерполяция для коротких сигналов звучит достаточно чисто, а стоит копейки.
func resample(src []int16, ratio float64) []int16 {
	if ratio == 1 || ratio <= 0 {
		return src
	}
	n := int(float64(len(src)) / ratio)
	if n <= 1 {
		return src
	}
	out := make([]int16, n)
	for i := range out {
		pos := float64(i) * ratio
		j := int(pos)
		switch {
		case j+1 < len(src):
			f := pos - float64(j)
			out[i] = int16(float64(src[j])*(1-f) + float64(src[j+1])*f)
		case j < len(src):
			out[i] = src[j]
		}
	}
	return out
}

func clamp16(v int32) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// PlaySound проигрывает сигнал события. Слышен даже в режиме «заглушен»:
// это сигнал интерфейса, а не чужой голос.
func (e *AudioEngine) PlaySound(name string) {
	soundsOnce.Do(buildSounds)
	pcm := events[name]
	if len(pcm) == 0 {
		return
	}

	e.beepMu.Lock()
	defer e.beepMu.Unlock()
	// не копим очередь: если события сыплются пачкой, лишние пропускаем
	if len(e.beepBuf) > sampleRate {
		return
	}
	// подмешиваем к тому, что уже стоит в очереди, а не дописываем в хвост —
	// иначе два события подряд слились бы в один длинный звук
	if len(e.beepBuf) < len(pcm) {
		grown := make([]int16, len(pcm))
		copy(grown, e.beepBuf)
		e.beepBuf = grown
	}
	for i, s := range pcm {
		e.beepBuf[i] = clamp16(int32(e.beepBuf[i]) + int32(s))
	}
}
