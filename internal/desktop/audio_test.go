package desktop

import (
	"testing"
	"unsafe"
)

// Молчащий (или замьюченный) микрофон не должен полностью прекращать поток:
// без исходящих пакетов NAT закрывает сопоставление и человека выбрасывает из
// канала посреди разговора. Тест ловит возврат к «полной тишине».
func TestSilenceStillSendsKeepalive(t *testing.T) {
	e := &AudioEngine{encBuf: make([]byte, 4000)}
	enc, err := newTestEncoder()
	if err != nil {
		t.Skip("opus недоступен:", err)
	}
	e.enc = enc
	e.SetGate(0.05)

	var frames int
	e.OnMicFrame = func([]byte) { frames++ }

	// 5 секунд абсолютной тишины
	silence := make([]byte, frameSize*2)
	for i := 0; i < 250; i++ {
		e.onCapture(nil, silence, 0)
	}
	if frames == 0 {
		t.Fatal("при тишине не ушло ни одного кадра — соединение умрёт по NAT")
	}
	// но и не должно слать всё подряд: это шумодав, а не его отсутствие
	if frames > 25 {
		t.Errorf("при тишине ушло %d кадров — шумодав не работает", frames)
	}
	t.Logf("тишина: %d кадров за 5 с (ожидаем ~5)", frames)

	// то же самое в муте
	frames = 0
	e.SetMuted(true)
	for i := 0; i < 250; i++ {
		e.onCapture(nil, silence, 0)
	}
	if frames == 0 {
		t.Fatal("в муте поток замолкает насовсем — человека выбросит из канала")
	}
	t.Logf("мут: %d кадров за 5 с", frames)
}

// Громкий звук должен проходить целиком, без прореживания.
func TestLoudAudioPassesThrough(t *testing.T) {
	e := &AudioEngine{encBuf: make([]byte, 4000)}
	enc, err := newTestEncoder()
	if err != nil {
		t.Skip("opus недоступен:", err)
	}
	e.enc = enc
	e.SetGate(0.05)

	var frames int
	e.OnMicFrame = func([]byte) { frames++ }

	loud := make([]int16, frameSize)
	for i := range loud {
		loud[i] = 12000
	}
	raw := unsafe.Slice((*byte)(unsafe.Pointer(&loud[0])), len(loud)*2)
	for i := 0; i < 50; i++ {
		e.onCapture(nil, raw, 0)
	}
	if frames != 50 {
		t.Errorf("громкий звук: ушло %d из 50 кадров, речь будет рваной", frames)
	}
}
