package desktop

import (
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
)

// Собственные уведомления вместо fyne.App.SendNotification.
//
// Причина: на macOS Fyne зовёт `osascript -e 'display notification ...'`.
// Это, во-первых, запуск отдельного процесса на каждое сообщение (~40 мс и
// десятки мегабайт), во-вторых — баннер уходит от имени Script Editor и
// сопровождается СИСТЕМНЫМ звуком уведомления. Именно он и звучал вместо
// наших звуков, и только на маке.
//
// Здесь баннер показывается только когда окно свёрнуто (иначе он не нужен —
// человек и так смотрит в чат), с ограничением частоты. Звук во всех случаях
// наш собственный, из микшера.

type notifier struct {
	app        fyne.App
	foreground atomic.Bool

	mu   sync.Mutex
	last time.Time
	// сколько событий проглотили с прошлого баннера — покажем числом,
	// чтобы человек понимал, что пропустил не одно сообщение
	skipped int
}

func newNotifier(app fyne.App) *notifier {
	n := &notifier{app: app}
	n.foreground.Store(true)
	lc := app.Lifecycle()
	lc.SetOnEnteredForeground(func() { n.foreground.Store(true) })
	lc.SetOnExitedForeground(func() { n.foreground.Store(false) })
	return n
}

// Foreground — окно приложения сейчас активно?
func (n *notifier) Foreground() bool { return n.foreground.Load() }

// notify показывает системный баннер. Если окно активно — не делает ничего:
// звук уже сыграл, а непрочитанное видно точкой на канале.
func (n *notifier) notify(title, body string) {
	if n == nil || n.foreground.Load() {
		return
	}

	// не чаще одного баннера в 5 секунд: в активном чате это иначе десятки
	// процессов osascript подряд
	n.mu.Lock()
	if time.Since(n.last) < 5*time.Second {
		n.skipped++
		n.mu.Unlock()
		return
	}
	n.last = time.Now()
	skipped := n.skipped
	n.skipped = 0
	n.mu.Unlock()

	if skipped > 0 {
		body = body + " (и ещё " + itoa(skipped) + ")"
	}
	go n.show(title, body)
}

func (n *notifier) show(title, body string) {
	if runtime.GOOS == "darwin" {
		// без `sound name` macOS баннер не озвучивает — звук у нас свой
		script := "display notification " + asQuote(body) + " with title " + asQuote(title)
		_ = exec.Command("osascript", "-e", script).Run()
		return
	}
	// на других системах штатный путь Fyne подходит: там он не плодит процессы
	n.app.SendNotification(fyne.NewNotification(title, body))
}

// asQuote — строковый литерал AppleScript. Кавычки и обратные слэши
// экранируются, переводы строк убираются: иначе скрипт не соберётся.
func asQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if len([]rune(s)) > 180 {
		s = string([]rune(s)[:180]) + "…"
	}
	return `"` + s + `"`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
