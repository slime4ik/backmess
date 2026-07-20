package desktop

import "testing"

// Звуки лежат в репозитории уже раскодированными, поэтому сломать их можно
// только пересобрав из исходников. Тест страхует от тихой поломки: пустой или
// битый файл иначе проявился бы только тишиной у юзера.
func TestEmbeddedSounds(t *testing.T) {
	soundsOnce.Do(loadSounds)

	for _, name := range []string{SoundJoin, SoundLeave, SoundMessage, SoundReply} {
		pcm, ok := sounds[name]
		if !ok {
			t.Fatalf("звук %q не встроен", name)
		}
		if len(pcm) == 0 {
			t.Fatalf("звук %q пустой", name)
		}
		// уведомление должно быть коротким: длинное раздражает и перекрывает речь
		if sec := float64(len(pcm)) / sampleRate; sec > 1.5 {
			t.Errorf("звук %q слишком длинный: %.2f с", name, sec)
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
			t.Errorf("звук %q — сплошная тишина", name)
		}
		// громкость выровнена при сборке; если пик у самой границы, значит
		// файл подменили ненормализованным и он ударит по ушам
		if peak > 30000 {
			t.Errorf("звук %q слишком громкий: пик %d", name, peak)
		}
	}
}
