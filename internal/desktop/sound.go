package desktop

import (
	"embed"
	"encoding/binary"
	"sync"
)

// Звуки уведомлений лежат уже раскодированными в сырой PCM (48 кГц, моно,
// 16 бит) — ровно в том формате, в котором работает микшер. Поэтому в
// приложении нет ни MP3-декодера, ни ресемплера: файл просто копируется в
// буфер воспроизведения. Семьдесят килобайт на оба звука.
//
//go:embed sounds/*.pcm
var soundFS embed.FS

const (
	// SoundJoin — кто-то зашёл в голосовой. Заметный, его ждёшь.
	SoundJoin = "henta_ahh"
	// SoundLeave — кто-то вышел. Короткий и тихий, чтобы не дёргать.
	SoundLeave = "kentut"
	// SoundMessage — новое сообщение в другом канале. Самое частое событие,
	// поэтому самый незаметный звук.
	SoundMessage = "kentut"
	// SoundReply — ответили именно тебе. Редко и важно.
	SoundReply = "henta_ahh"
)

var (
	soundsOnce sync.Once
	sounds     map[string][]int16
)

func loadSounds() {
	sounds = map[string][]int16{}
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
		name := e.Name()[:len(e.Name())-len(".pcm")]
		sounds[name] = pcm
	}
}

// PlaySound подмешивает готовый звук в поток воспроизведения. Слышен даже в
// режиме «заглушен»: это интерфейсный сигнал, а не чужой голос.
// gain — 0..1, чтобы одно и то же можно было проиграть громче или тише.
func (e *AudioEngine) PlaySound(name string, gain float64) {
	soundsOnce.Do(loadSounds)
	pcm := sounds[name]
	if len(pcm) == 0 {
		return
	}
	if gain <= 0 {
		return
	}
	if gain > 1 {
		gain = 1
	}

	e.beepMu.Lock()
	defer e.beepMu.Unlock()
	// не копим очередь: если звуки сыплются пачкой, лишние просто пропускаем
	if len(e.beepBuf) > sampleRate {
		return
	}
	// подмешиваем к уже стоящему в очереди, а не дописываем в хвост —
	// иначе два события подряд звучали бы как один длинный звук
	if len(e.beepBuf) < len(pcm) {
		grown := make([]int16, len(pcm))
		copy(grown, e.beepBuf)
		e.beepBuf = grown
	}
	for i, s := range pcm {
		v := int32(e.beepBuf[i]) + int32(float64(s)*gain)
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		e.beepBuf[i] = int16(v)
	}
}
