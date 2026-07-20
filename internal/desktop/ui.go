package desktop

import (
	"io"
	"net/url"
	"os"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/slime4ik/backmess/internal/auth"
	"github.com/slime4ik/backmess/internal/hub"
	"github.com/slime4ik/backmess/web"
)

// Сервер зашит намертво — в UI поля для его смены нет, чужому серверу этот
// клиент не нужен. MESS_SERVER — только для локальной разработки/тестов
// (`MESS_SERVER=http://localhost:8080 go run -tags nolibopusfile ./cmd/backmess-app`),
// в собранных релизах переменная не выставлена и используется прод-домен.
var defaultServer = envOr("MESS_SERVER", "https://discord.djaploy.dev")

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type UI struct {
	app     fyne.App
	win     fyne.Window
	audio   *AudioEngine
	api     *API
	gw      *Gateway
	avatars *avatarCache

	me     auth.User
	groups []hub.GroupView

	curGroup string // выбранная группа
	curChan  string // открытый текстовый канал

	voice    *VoiceClient
	voiceCh  string
	muted    bool
	deafened bool
	micOK    bool

	speaking map[string]bool // user id -> говорит прямо сейчас
	unread   map[string]bool // channel id -> есть непрочитанное

	// каркас
	railBox    *fyne.Container
	chanBox    *fyne.Container
	membersBox *fyne.Container
	msgsBox    *fyne.Container
	msgScroll  *container.Scroll
	chatEntry  *widget.Entry
	chatArea   *fyne.Container
	chanTitle  *canvas.Text
	groupTitle *canvas.Text
	meBox      *fyne.Container
	btnMute    *widget.Button
	btnDeaf    *widget.Button
	ping       *pingBars

	// панель голосового подключения
	voiceBox    *fyne.Container
	voiceStatus string
	voiceTimer  *canvas.Text
	voicePing   *pingBars

	// склейка подряд идущих сообщений одного автора
	lastAuthor string
	lastTS     int64
}

func Run() {
	// Метаданные задаём в коде, а не через FyneApp.toml: файл ищется рядом с
	// бинарником, а его юзер качает с GitHub одним файлом. Флаг fyneDo говорит
	// Fyne, что все обновления UI у нас уже завёрнуты в fyne.Do — иначе он на
	// каждом старте пишет в лог предупреждение про threading model.
	fyneapp.SetMetadata(fyne.AppMetadata{
		ID:         "dev.djaploy.mess",
		Name:       "mess",
		Version:    "0.1.0",
		Build:      1,
		Migrations: map[string]bool{"fyneDo": true},
	})

	a := fyneapp.NewWithID("dev.djaploy.mess")
	a.Settings().SetTheme(darkTheme{})
	if b, err := readStaticFile("icon.svg"); err == nil {
		a.SetIcon(fyne.NewStaticResource("icon.svg", b))
	}
	w := a.NewWindow("mess")
	w.Resize(fyne.NewSize(1180, 720))

	u := &UI{
		app: a, win: w,
		speaking: map[string]bool{},
		unread:   map[string]bool{},
	}

	audio, err := NewAudioEngine()
	if err != nil {
		w.SetContent(container.NewCenter(widget.NewLabel("звук не завёлся: " + err.Error())))
		w.ShowAndRun()
		return
	}
	u.audio = audio
	u.micOK, err = audio.Start()
	if err != nil {
		w.SetContent(container.NewCenter(widget.NewLabel("звук не завёлся: " + err.Error())))
		w.ShowAndRun()
		return
	}

	// сохранённая сессия? — продлеваем через /api/refresh, а не просто /api/me,
	// чтобы токен не протухал, пока юзер время от времени запускает приложение
	token := a.Preferences().String("token")
	if token != "" {
		api := NewAPI(defaultServer, token)
		go func() {
			_, err := api.Refresh()
			fyne.Do(func() {
				if err == nil {
					a.Preferences().SetString("token", api.Token)
					u.start(api)
				} else {
					u.showLogin("")
				}
			})
		}()
		w.SetContent(container.NewCenter(widget.NewLabel("подключаюсь…")))
	} else {
		u.showLogin("")
	}

	go u.levelLoop()
	w.SetCloseIntercept(func() {
		u.leaveVoice()
		if u.gw != nil {
			u.gw.Close()
		}
		audio.Close()
		w.Close()
	})
	w.ShowAndRun()
}

func readStaticFile(name string) ([]byte, error) {
	f, err := web.Static().Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

/* ---------- логин ---------- */

func (u *UI) showLogin(errText string) {
	title := txt("mess", colAmber, 42, true)
	errLbl := txt(errText, colRed, 12, false)
	errLbl.Alignment = fyne.TextAlignCenter

	var btn *widget.Button
	btn = widget.NewButton("Войти через Discord", func() {
		btn.Disable()
		errLbl.Text = ""
		errLbl.Refresh()

		flow, err := BeginDiscordLogin(defaultServer)
		if err != nil {
			btn.Enable()
			errLbl.Text = "не вышло: " + err.Error()
			errLbl.Refresh()
			return
		}
		pu, err := url.Parse(flow.URL)
		if err != nil {
			btn.Enable()
			errLbl.Text = "не вышло: " + err.Error()
			errLbl.Refresh()
			return
		}
		// открываем браузер синхронно, с UI-потока — иначе macOS не может
		// сматчить обработчик URL и падает с kLSApplicationNotFoundErr
		if err := u.app.OpenURL(pu); err != nil {
			btn.Enable()
			errLbl.Text = "не открылся браузер: " + err.Error()
			errLbl.Refresh()
			return
		}
		errLbl.Text = "жду подтверждения в браузере…"
		errLbl.Refresh()
		go func() {
			api, err := flow.Wait(3 * time.Minute)
			fyne.Do(func() {
				btn.Enable()
				if err != nil {
					errLbl.Text = "не вышло: " + err.Error()
					errLbl.Refresh()
					return
				}
				u.app.Preferences().SetString("token", api.Token)
				u.start(api)
			})
		}()
	})
	btn.Importance = widget.HighImportance

	card := container.NewVBox(container.NewCenter(title), widget.NewLabel(""), btn, errLbl)
	u.win.SetContent(filled(colMain, container.NewCenter(container.NewGridWrap(fyne.NewSize(320, 220), card))))
}

/* ---------- запуск основного экрана ---------- */

func (u *UI) start(api *API) {
	u.api = api
	u.avatars = newAvatarCache(api)
	u.buildShell()
	u.gw = NewGateway(api, GatewayEvents{
		OnReady: func(me auth.User, gs []hub.GroupView) {
			fyne.Do(func() {
				u.me = me
				u.groups = gs
				u.restoreSelection()
				u.renderAll()
				if u.curChan != "" {
					u.openChannel(u.curChan) // подтянуть историю открытого канала
				}
			})
		},
		OnGroup: func(g hub.GroupView) {
			fyne.Do(func() { u.upsertGroup(g); u.renderAll() })
		},
		OnGroupGone: func(id string) {
			fyne.Do(func() { u.removeGroup(id); u.renderAll() })
		},
		OnPresence: func(uid string, online bool) {
			fyne.Do(func() { u.setPresence(uid, online); u.renderMembers() })
		},
		OnVoice: func(ch string, ms []hub.VoiceMember) {
			fyne.Do(func() { u.setVoice(ch, ms); u.renderChannels(); u.renderMembers() })
		},
		OnChat: func(m hub.OutMsg) {
			u.onChat(m)
		},
		OnHistory: func(ch string, msgs []hub.OutMsg) {
			fyne.Do(func() { u.renderHistory(ch, msgs) })
		},
		OnPing: func(ms int) {
			fyne.Do(func() { u.ping.set(ms) })
		},
		OnLink: func(up bool) {
			fyne.Do(func() {
				if !up {
					u.ping.set(-1)
				}
			})
		},
	})
}

/* ---------- состояние ---------- */

func (u *UI) group(id string) *hub.GroupView {
	for i := range u.groups {
		if u.groups[i].ID == id {
			return &u.groups[i]
		}
	}
	return nil
}

func (u *UI) curGroupView() *hub.GroupView { return u.group(u.curGroup) }

func (u *UI) channel(chID string) *hub.GroupView {
	for i := range u.groups {
		for _, c := range u.groups[i].Channels {
			if c.ID == chID {
				return &u.groups[i]
			}
		}
	}
	return nil
}

func (u *UI) upsertGroup(g hub.GroupView) {
	for i := range u.groups {
		if u.groups[i].ID == g.ID {
			u.groups[i] = g
			return
		}
	}
	u.groups = append(u.groups, g)
	// свежесозданную/принятую группу сразу открываем — иначе непонятно,
	// сработала ли кнопка вообще
	u.curGroup = g.ID
	u.curChan = firstTextChannel(g)
}

func (u *UI) removeGroup(id string) {
	out := u.groups[:0]
	for _, g := range u.groups {
		if g.ID != id {
			out = append(out, g)
		}
	}
	u.groups = out
	if u.curGroup == id {
		u.curGroup, u.curChan = "", ""
		u.restoreSelection()
	}
}

func (u *UI) setPresence(uid string, online bool) {
	for i := range u.groups {
		if u.groups[i].Online == nil {
			u.groups[i].Online = map[string]bool{}
		}
		if u.groups[i].HasMember(uid) {
			u.groups[i].Online[uid] = online
		}
	}
}

func (u *UI) setVoice(chID string, ms []hub.VoiceMember) {
	g := u.channel(chID)
	if g == nil {
		return
	}
	if g.Voice == nil {
		g.Voice = map[string][]hub.VoiceMember{}
	}
	if len(ms) == 0 {
		delete(g.Voice, chID)
	} else {
		g.Voice[chID] = ms
	}
}

func firstTextChannel(g hub.GroupView) string {
	for _, c := range g.Channels {
		if c.Kind == "text" {
			return c.ID
		}
	}
	return ""
}

// restoreSelection выбирает, что показать: то, что было открыто в прошлый раз,
// иначе первую группу и её первый текстовый канал.
func (u *UI) restoreSelection() {
	p := u.app.Preferences()
	if u.curGroup == "" {
		u.curGroup = p.String("lastGroup")
	}
	if u.group(u.curGroup) == nil {
		u.curGroup = ""
		if len(u.groups) > 0 {
			u.curGroup = u.groups[0].ID
		}
	}
	g := u.curGroupView()
	if g == nil {
		u.curChan = ""
		return
	}
	if u.curChan == "" {
		u.curChan = p.String("lastChan:" + g.ID)
	}
	if !hasChannel(*g, u.curChan) {
		u.curChan = firstTextChannel(*g)
	}
}

func hasChannel(g hub.GroupView, chID string) bool {
	for _, c := range g.Channels {
		if c.ID == chID && c.Kind == "text" {
			return true
		}
	}
	return false
}

func (u *UI) selectGroup(id string) {
	if u.curGroup == id {
		return
	}
	u.curGroup = id
	u.curChan = ""
	u.app.Preferences().SetString("lastGroup", id)
	u.restoreSelection()
	u.renderAll()
	if u.curChan != "" {
		u.openChannel(u.curChan)
	}
}

func (u *UI) openChannel(chID string) {
	u.curChan = chID
	delete(u.unread, chID)
	u.app.Preferences().SetString("lastChan:"+u.curGroup, chID)
	u.lastAuthor, u.lastTS = "", 0
	if u.msgsBox != nil {
		u.msgsBox.Objects = nil
		u.msgsBox.Refresh()
	}
	u.renderChannels()
	u.renderRail() // непрочитанное могло погаснуть — обновляем значок группы
	u.renderChatArea()
	u.gw.RequestHistory(chID)
}
