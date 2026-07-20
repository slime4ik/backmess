package desktop

import "testing"

// Звуки собираются из двух семплов на старте. Тест страхует от тихой поломки:
// пустой сигнал иначе проявился бы только тишиной у юзера, без ошибки в логе.
func TestSoundEvents(t *testing.T) {
	soundsOnce.Do(buildSounds)

	if len(samples[sampleBig]) == 0 || len(samples[sampleSmall]) == 0 {
		t.Fatal("исходные семплы не встроились")
	}

	for name := range soundEvents {
		pcm, ok := events[name]
		if !ok || len(pcm) == 0 {
			t.Errorf("сигнал %q не собрался", name)
			continue
		}
		// уведомление должно быть коротким: длинное перекрывает разговор
		if sec := float64(len(pcm)) / sampleRate; sec > 1.5 {
			t.Errorf("сигнал %q слишком длинный: %.2f с", name, sec)
		}
		var peak int16
		for _, s := range pcm {
			if s > peak {
				peak = s
			} else if -s > peak {
				peak = -s
			}
		}
		if peak == 0 {
			t.Errorf("сигнал %q — тишина", name)
		}
		// при наложении двух семплов легко словить перегруз и хрип
		if peak > 32000 {
			t.Errorf("сигнал %q перегружен: пик %d", name, peak)
		}
	}
}

// Разные события обязаны звучать по-разному, иначе теряется весь смысл:
// в игре окно не видно и сигнал — единственный способ понять, что случилось.
func TestSoundEventsAreDistinct(t *testing.T) {
	soundsOnce.Do(buildSounds)
	seen := map[string]string{}
	for name := range soundEvents {
		pcm := events[name]
		if len(pcm) == 0 {
			continue
		}
		// грубая подпись: длина плюс сумма модулей — совпадение означает,
		// что сигналы неразличимы и на слух
		var sum int64
		for _, s := range pcm {
			if s < 0 {
				s = -s
			}
			sum += int64(s)
		}
		key := string(rune(len(pcm)%251)) + ":" + string(rune(sum%251))
		if other, dup := seen[key]; dup {
			t.Errorf("сигналы %q и %q звучат одинаково", name, other)
		}
		seen[key] = name
	}
}
