package desktop

import (
	"math"
	"testing"
)

// Реальный фон (вентилятор, гул сети, шипение) стационарен. Моделируем его
// стабильным тоном 60 Гц и проверяем, что денойзер давит его, но пропускает
// «голос» на 300 Гц. Это ближе к настоящему применению, чем белый шум.
func TestDenoiserSuppressesSteadyNoise(t *testing.T) {
	d := newDenoiser()
	frame := make([]int16, frameSize)

	energyAt := func(sig []int16, freq float64) float64 {
		// корреляция с синусом нужной частоты — грубая оценка энергии на ней
		var re, im float64
		for i, s := range sig {
			ph := 2 * math.Pi * freq * float64(i) / sampleRate
			re += float64(s) / 32768 * math.Cos(ph)
			im += float64(s) / 32768 * math.Sin(ph)
		}
		return math.Hypot(re, im) / float64(len(sig))
	}

	var humIn, humOut, voiceIn, voiceOut float64
	n := 0
	for f := 0; f < 160; f++ {
		for i := range frame {
			t := f*frameSize + i
			hum := 0.15 * math.Sin(2*math.Pi*60*float64(t)/sampleRate)
			voice := 0.0
			if f >= 40 { // первые кадры — только фон, денойзер учится
				voice = 0.3 * math.Sin(2*math.Pi*300*float64(t)/sampleRate)
			}
			frame[i] = int16((hum + voice) * 32767)
		}
		out := d.Process(frame)
		if f >= 120 { // после прогрева и обучения фону
			humIn += energyAt(frame, 60)
			humOut += energyAt(out, 60)
			voiceIn += energyAt(frame, 300)
			voiceOut += energyAt(out, 300)
			n++
		}
	}

	humKept := humOut / humIn
	voiceKept := voiceOut / voiceIn
	t.Logf("фон 60Гц остался: %.0f%%   голос 300Гц остался: %.0f%%", humKept*100, voiceKept*100)

	if humKept > 0.5 {
		t.Errorf("фон почти не подавлен: осталось %.0f%%", humKept*100)
	}
	if voiceKept < 0.6 {
		t.Errorf("голос заметно порезан: осталось только %.0f%%", voiceKept*100)
	}
}

// Число отсчётов на выходе = числу на входе (после прогрева): движок кодирует
// ровно 960 на кадр, денойзер не имеет права менять размер.
func TestDenoiserFrameSize(t *testing.T) {
	d := newDenoiser()
	frame := make([]int16, frameSize)
	for f := 0; f < 10; f++ {
		if got := len(d.Process(frame)); got != frameSize {
			t.Fatalf("кадр %d: вернул %d отсчётов вместо %d", f, got, frameSize)
		}
	}
}
