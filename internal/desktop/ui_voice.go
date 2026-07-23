package desktop

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/slime4ik/backmess/internal/hub"
)

/* ---------- панель «я» внизу списка каналов ---------- */

func (u *UI) userPanel() fyne.CanvasObject {
	u.btnMute = widget.NewButtonWithIcon("", iconMicOn, func() { u.toggleMute() })
	u.btnMute.Importance = widget.LowImportance
	u.btnDeaf = widget.NewButtonWithIcon("", iconSoundOn, func() { u.toggleDeaf() })
	u.btnDeaf.Importance = widget.LowImportance
	btnSettings := widget.NewButtonWithIcon("", theme.SettingsIcon(), func() { u.showAudioSettings() })
	btnSettings.Importance = widget.LowImportance

	u.meBox = container.NewHBox()
	// индикатор связи живёт только в панели звонка: два одинаковых показателя
	// на экране лишние, а вне канала задержка ни на что не влияет
	panel := container.NewBorder(nil, nil, u.meBox,
		container.NewHBox(u.btnMute, u.btnDeaf, btnSettings),
		nil,
	)
	return container.NewPadded(panel)
}

func (u *UI) renderMe() {
	if u.meBox == nil {
		return
	}
	u.meBox.Objects = nil
	if u.me.ID == "" {
		u.meBox.Refresh()
		return
	}
	u.avMe = u.avatarView(u.me, 30)
	u.avMe.setSpeaking(u.speaking[u.me.ID] && !u.muted, u.levels[u.me.ID])
	u.meBox.Add(u.avMe.obj)
	u.meBox.Add(txt(u.me.Name, colText, 12, true))
	u.meBox.Refresh()
}

/* ---------- настройки звука ---------- */

// showAudioSettings — выбор микрофона и наушников. Без этого, если система
// выбрала не то устройство, починить это изнутри приложения было нельзя.
func (u *UI) showAudioSettings() {
	mics := u.audio.Devices(true)
	outs := u.audio.Devices(false)

	const auto = "по умолчанию (система)"
	names := func(ds []Device) []string {
		out := []string{auto}
		for _, d := range ds {
			n := d.Name
			if d.Default {
				n += " ★"
			}
			out = append(out, n)
		}
		return out
	}
	// со звёздочкой показываем, но храним и передаём чистое имя
	clean := func(s string) string {
		if s == auto {
			return ""
		}
		return strings.TrimSuffix(s, " ★")
	}
	pick := func(cur string, ds []Device) string {
		for _, d := range ds {
			if d.Name == cur {
				if d.Default {
					return d.Name + " ★"
				}
				return d.Name
			}
		}
		return auto
	}

	micSel := widget.NewSelect(names(mics), nil)
	micSel.SetSelected(pick(u.audio.CaptureName(), mics))
	outSel := widget.NewSelect(names(outs), nil)
	outSel.SetSelected(pick(u.audio.PlaybackName(), outs))

	status := txt("", colDim, 11, false)

	micSel.OnChanged = func(s string) {
		name := clean(s)
		if err := u.audio.StartCapture(name); err != nil {
			status.Text = "микрофон не открылся: " + err.Error()
			status.Color = colRed
			u.micOK = false
		} else {
			status.Text = "микрофон переключён"
			status.Color = colGreen
			u.micOK = true
			u.app.Preferences().SetString("mic", name)
		}
		status.Refresh()
	}
	outSel.OnChanged = func(s string) {
		name := clean(s)
		if err := u.audio.StartPlayback(name); err != nil {
			status.Text = "вывод не открылся: " + err.Error()
			status.Color = colRed
		} else {
			status.Text = "вывод переключён"
			status.Color = colGreen
			u.app.Preferences().SetString("out", name)
		}
		status.Refresh()
	}

	test := widget.NewButton("проверить звук", func() {
		u.audio.PlaySound(SoundJoin)
	})

	// Шумоподавление: чистит голос от постоянного фона (вентилятор, гул).
	dnCheck := widget.NewCheck("шумоподавление", func(on bool) {
		u.audio.SetDenoise(on)
		u.app.Preferences().SetBool("denoise", on)
	})
	dnCheck.SetChecked(u.audio.Denoise())

	// Ручной порог: полоска + подсказка + живой уровень. Показывается только
	// когда авто выключен.
	gateHint := txt("", colDim, 10, false)
	setHint := func(v float64) {
		switch {
		case v < 1:
			gateHint.Text = "передаётся всё, включая шумы"
		case v < 8:
			gateHint.Text = "отсекает тихий фон"
		case v < 18:
			gateHint.Text = "отсекает клавиатуру и вентилятор"
		default:
			gateHint.Text = "только громкая речь"
		}
		gateHint.Refresh()
	}
	gateBox, _ := labeledSlider("ПОРОГ ТИШИНЫ", 0, 30, 1, u.audio.Gate()*100,
		func(v float64) string { return fmt.Sprintf("%.0f%%", v) },
		"слышно всё", "только речь",
		func(v float64) {
			u.audio.SetGate(v / 100)
			u.app.Preferences().SetFloat("gate", v/100)
			setHint(v)
		})
	setHint(u.audio.Gate() * 100)

	u.gateMeter = canvas.NewRectangle(colGreen)
	u.gateMeter.CornerRadius = 2
	meterBG := canvas.NewRectangle(colInput)
	meterBG.CornerRadius = 2
	meter := container.NewStack(meterBG,
		container.NewBorder(nil, nil, nil, layoutSpacer(1), sized(0, 6, u.gateMeter)))

	// ручной блок скрывается целиком в авторежиме
	manualBox := container.NewVBox(
		gateBox, gateHint,
		txt("текущий уровень:", colDim, 10, false),
		sized(0, 6, meter),
	)

	// Авто-порог: приложение само держит порог чуть выше фона. По умолчанию
	// включён — большинству ручной порог настраивать не хочется.
	autoCheck := widget.NewCheck("автоматически подбирать порог", func(on bool) {
		u.audio.SetAutoGate(on)
		u.app.Preferences().SetBool("autogate", on)
		if on {
			manualBox.Hide()
		} else {
			manualBox.Show()
		}
	})
	autoCheck.SetChecked(u.audio.AutoGate())
	if u.audio.AutoGate() {
		manualBox.Hide()
	}

	content := container.NewVBox(
		txt("МИКРОФОН", colDim, 11, true), micSel,
		txt("НАУШНИКИ / ДИНАМИКИ", colDim, 11, true), outSel,
		widget.NewSeparator(),
		txt("ОБРАБОТКА ГОЛОСА", colDim, 11, true),
		dnCheck,
		autoCheck,
		manualBox,
		widget.NewSeparator(),
		test,
		status,
		txt("если воткнул наушники после запуска — выбери их тут", colDim, 10, false),
	)
	d := dialog.NewCustom("звук", "закрыть", content, u.win)
	d.SetOnClosed(func() { u.gateMeter = nil })
	d.Resize(fyne.NewSize(440, 560))
	d.Show()
}

/* ---------- панель голосового подключения ---------- */

// renderVoicePanel рисует зелёную плашку «ты в голосе»: канал, время в нём,
// задержка и кнопка выхода. Без неё после входа в канал вообще непонятно,
// подключился ты или нет.
func (u *UI) renderVoicePanel() {
	if u.voiceBox == nil {
		return
	}
	u.voiceBox.Objects = nil
	// Панель висит, пока человек числится в канале, — в том числе когда связь
	// отвалилась и мы её восстанавливаем. Раньше она просто исчезала, и обрыв
	// проходил незамеченным: сидишь, говоришь, а тебя уже никто не слышит.
	if u.voiceCh == "" {
		u.voiceBox.Refresh()
		return
	}

	live := u.voice != nil && u.voiceStatus == "голос подключён"
	dotCol, textCol := colYellow, colYellow
	if live {
		dotCol, textCol = colGreen, colGreen
	} else if u.voiceTries > 0 {
		dotCol, textCol = colRed, colRed
	}

	chName, grName := u.voiceNames()
	head := container.NewHBox(
		txt("●", dotCol, 14, true),
		txt(u.voiceStatus, textCol, 12, true),
	)
	where := txt(chName+" · "+grName, colDim, 11, false)

	body := container.NewVBox(head, where)
	if live {
		u.voiceTimer = txt("00:00", colDim, 11, false)
		u.voicePing = newPingBars()
		u.voicePing.set(u.voice.RTTMS())
		body.Add(container.NewHBox(u.voiceTimer, layoutSpacer(6), u.voicePing.box))
	} else {
		u.voiceTimer, u.voicePing = nil, nil
		if u.voiceTries > 0 {
			body.Add(txt("попытка "+itoa(u.voiceTries), colDim, 10, false))
		}
	}

	leave := widget.NewButtonWithIcon("отключиться", theme.CancelIcon(), func() { u.leaveVoice() })
	leave.Importance = widget.DangerImportance
	body.Add(leave)

	u.voiceBox.Add(container.NewPadded(body))
	u.voiceBox.Refresh()
}

func (u *UI) voiceNames() (chName, grName string) {
	chName, grName = "канал", ""
	if g := u.channel(u.voiceCh); g != nil {
		grName = g.Name
		for _, c := range g.Channels {
			if c.ID == u.voiceCh {
				chName = c.Name
			}
		}
	}
	return
}

/* ---------- вход/выход ---------- */

func (u *UI) joinVoice(chID, name string) {
	if u.voiceCh == chID && u.voice != nil {
		return
	}
	u.leaveVoice()
	u.voiceCh = chID
	u.voiceName = name
	u.voiceTries = 0
	u.connectVoice()
}

// connectVoice поднимает соединение с текущим каналом. При обрыве сам зовёт
// себя снова: из канала человека не должно выбрасывать, пока он не нажал
// «отключиться». Единственный способ выйти — leaveVoice.
func (u *UI) connectVoice() {
	chID, name := u.voiceCh, u.voiceName
	if chID == "" {
		return
	}
	if u.voiceTries == 0 {
		u.voiceStatus = "подключаюсь…"
	} else {
		u.voiceStatus = "переподключаюсь…"
	}
	u.renderVoicePanel()
	u.renderChannels()

	// mine указывает на «своё» соединение: колбэки старого клиента прилетают
	// уже после того, как мы переключились, и без этой проверки они бы стёрли
	// состояние нового подключения
	var mine *VoiceClient

	ev := VoiceEvents{
		OnMemberJoin: func(m hub.MemberInfo) {
			u.audio.PlaySound(SoundJoin)
		},
		OnMemberLeave: func(id string) {
			u.audio.PlaySound(SoundLeave)
		},
		OnRTC: func(state string) {
			fyne.Do(func() {
				if mine == nil || u.voice != mine {
					return
				}
				switch state {
				case "connected":
					reconnected := u.voiceTries > 0
					u.voiceTries = 0
					u.voiceStatus = "голос подключён"
					u.audio.PlaySound(SoundConnect)
					if reconnected {
						u.toast("связь восстановлена")
					}
				case "connecting", "new":
					u.voiceStatus = "подключаюсь…"
				case "disconnected":
					// не обрыв: ICE сам восстанавливается, ждём
					u.voiceStatus = "связь пропала, жду…"
				case "failed":
					u.voiceStatus = "связь потеряна"
				}
				u.renderVoicePanel()
			})
		},
		OnClosed: func(reason string) {
			fyne.Do(func() {
				if mine == nil || u.voice != mine {
					return // отвалилось прошлое подключение, оно уже неактуально
				}
				u.voice = nil
				if u.voiceCh == "" {
					return // человек сам вышел — ничего не восстанавливаем
				}
				u.scheduleReconnect(reason)
			})
		},
	}

	go func() {
		vc, err := JoinVoice(u.api, u.audio, chID, ev)
		fyne.Do(func() {
			if err != nil {
				if u.voiceCh == chID {
					u.scheduleReconnect(err.Error())
				}
				return
			}
			// пока подключались, человек мог уйти в другой канал
			if u.voiceCh != chID {
				vc.Close("передумал")
				return
			}
			mine = vc
			u.voice = vc
			vc.SetState(u.muted || !u.micOK, u.deafened)
			u.renderVoicePanel()
			u.renderChannels()
			_ = name
		})
	}()
}

// scheduleReconnect ставит повтор с нарастающей паузой. Звук обрыва играем
// один раз, чтобы при долгом отсутствии сети не пиликать каждые пару секунд.
func (u *UI) scheduleReconnect(reason string) {
	if u.voiceCh == "" {
		return
	}
	if u.voiceTries == 0 {
		u.audio.PlaySound(SoundHangup)
		u.toast("связь с каналом пропала, восстанавливаю…")
	}
	u.voiceTries++

	delay := time.Duration(u.voiceTries) * time.Second
	if delay > 10*time.Second {
		delay = 10 * time.Second
	}
	u.voiceStatus = "переподключаюсь…"
	u.renderVoicePanel()
	u.renderChannels()

	ch := u.voiceCh
	time.AfterFunc(delay, func() {
		fyne.Do(func() {
			// за время паузы человек мог выйти сам или уйти в другой канал
			if u.voiceCh == ch && u.voice == nil {
				u.connectVoice()
			}
		})
	})
}

func (u *UI) leaveVoice() {
	// сначала снимаем канал: по нему переподключение понимает, что выход
	// сделан человеком, и не пытается восстановить связь
	u.voiceCh = ""
	u.voiceName = ""
	u.voiceTries = 0
	if v := u.voice; v != nil {
		u.voice = nil
		v.Close("вышел")
		u.audio.PlaySound(SoundHangup)
	}
	u.voiceStatus = ""
	u.renderVoicePanel()
	u.renderChannels()
}

/* ---------- мут / глухота ---------- */

func (u *UI) toggleMute() {
	if !u.micOK {
		u.toast("микрофона нет — сидишь слушателем")
		return
	}
	u.muted = !u.muted
	if !u.muted {
		u.deafened = false
	}
	u.applyAV()
}

func (u *UI) toggleDeaf() {
	u.deafened = !u.deafened
	if u.deafened {
		u.muted = true
	}
	u.applyAV()
}

func (u *UI) applyAV() {
	// свои переключатели звучат тихо, но по-разному: в игре надо на слух
	// понимать, что именно ты нажал, не разворачивая окно
	switch {
	case u.deafened:
		u.audio.PlaySound(SoundDeafOn)
	case u.wasDeafened:
		u.audio.PlaySound(SoundDeafOff)
	case u.muted:
		u.audio.PlaySound(SoundMuteOn)
	default:
		u.audio.PlaySound(SoundMuteOff)
	}
	u.wasDeafened = u.deafened
	if u.voice != nil {
		u.voice.SetState(u.muted, u.deafened)
	} else {
		u.audio.SetMuted(u.muted)
		u.audio.SetDeafened(u.deafened)
	}
	u.updateAVButtons()
	u.renderMe()
}

func (u *UI) updateAVButtons() {
	if u.btnMute == nil {
		return
	}
	if u.muted {
		u.btnMute.SetIcon(iconMicOff)
		u.btnMute.Importance = widget.DangerImportance
	} else {
		u.btnMute.SetIcon(iconMicOn)
		u.btnMute.Importance = widget.LowImportance
	}
	if u.deafened {
		u.btnDeaf.SetIcon(iconSoundOff)
		u.btnDeaf.Importance = widget.DangerImportance
	} else {
		u.btnDeaf.SetIcon(iconSoundOn)
		u.btnDeaf.Importance = widget.LowImportance
	}
	u.btnMute.Refresh()
	u.btnDeaf.Refresh()
}

/* ---------- индикация «кто говорит» + таймер + пинг ---------- */

// levelLoop крутится всё время работы приложения: раз в 150 мс смотрит
// пиковые уровни звука и перерисовывает подсветку говорящих, а раз в секунду
// обновляет таймер и задержку голосового канала.
func (u *UI) levelLoop() {
	t := time.NewTicker(120 * time.Millisecond)
	defer t.Stop()
	tick := 0
	for range t.C {
		tick++
		inVoice := u.voice != nil
		if !inVoice && len(u.speaking) == 0 && u.gateMeter == nil {
			continue // нечего обновлять — не будим интерфейс попусту
		}

		levels := map[string]float64{}
		if inVoice || u.gateMeter != nil {
			for id, v := range u.audio.Levels() {
				key := id
				if id == "" {
					key = u.me.ID // "" — это мой собственный микрофон
				}
				if key != "" {
					levels[key] = v
				}
			}
		}

		// два порога вместо одного: иначе на границе тишины индикатор
		// мигает по несколько раз в секунду и рябит в глазах
		const (
			levelOn  = 0.05
			levelOff = 0.02
		)
		next := map[string]bool{}
		for id, v := range levels {
			switch {
			case v > levelOn:
				next[id] = true
			case v < levelOff:
				next[id] = false
			default:
				next[id] = u.speaking[id]
			}
		}
		everySecond := tick%8 == 0

		fyne.Do(func() {
			u.levels = levels
			u.speaking = next
			u.refreshSpeakingRings()
			if u.gateMeter != nil {
				u.updateGateMeter(levels[u.me.ID])
			}
			if everySecond {
				u.updateVoiceStats()
			}
		})
	}
}

// refreshSpeakingRings трогает только сами кольца. Раньше на каждое изменение
// пересобирались списки каналов и участников целиком — по несколько раз в
// секунду, что и было основной причиной лишнего расхода процессора.
func (u *UI) refreshSpeakingRings() {
	apply := func(m map[string][]*avatarView) {
		for id, views := range m {
			on := u.speaking[id]
			lvl := u.levels[id]
			for _, av := range views {
				av.setSpeaking(on, lvl)
			}
		}
	}
	apply(u.avChannels)
	apply(u.avMembers)
	if u.avMe != nil {
		u.avMe.setSpeaking(u.speaking[u.me.ID] && !u.muted, u.levels[u.me.ID])
	}
}

// updateGateMeter — живая полоска уровня в настройках: по ней видно, куда
// ставить порог чувствительности.
func (u *UI) updateGateMeter(level float64) {
	w := float32(level * 380)
	if w > 380 {
		w = 380
	}
	u.gateMeter.Resize(fyne.NewSize(w, 6))
	if level > u.audio.Gate() {
		u.gateMeter.FillColor = colGreen
	} else {
		u.gateMeter.FillColor = colOffline
	}
	u.gateMeter.Refresh()
}

func (u *UI) updateVoiceStats() {
	if u.voice == nil {
		return
	}
	if u.voiceTimer != nil {
		d := time.Since(u.voice.JoinedAt)
		h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
		if h > 0 {
			u.voiceTimer.Text = fmt.Sprintf("%d:%02d:%02d", h, m, s)
		} else {
			u.voiceTimer.Text = fmt.Sprintf("%02d:%02d", m, s)
		}
		u.voiceTimer.Refresh()
	}
	if u.voicePing != nil {
		if rtt := u.voice.RTTMS(); rtt > 0 {
			u.voicePing.set(rtt)
		}
	}
}

/* ---------- всплывающее уведомление ---------- */

// toast — короткое сообщение внизу окна. Безопасно звать из любой горутины.
func (u *UI) toast(text string) {
	fyne.Do(func() {
		if u.win == nil {
			return
		}
		bg := canvas.NewRectangle(colInput)
		bg.CornerRadius = 8
		bg.StrokeColor = colLine
		bg.StrokeWidth = 1
		label := txt(text, colText, 12, false)
		pop := widget.NewPopUp(
			container.NewStack(bg, container.NewPadded(container.NewPadded(label))),
			u.win.Canvas(),
		)
		sz := u.win.Canvas().Size()
		pop.Move(fyne.NewPos(24, sz.Height-pop.MinSize().Height-24))
		pop.Show()
		go func() {
			time.Sleep(4 * time.Second)
			fyne.Do(pop.Hide)
		}()
	})
}
