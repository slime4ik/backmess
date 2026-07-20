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
	u.btnMute = widget.NewButtonWithIcon("", theme.VolumeUpIcon(), func() { u.toggleMute() })
	u.btnMute.Importance = widget.LowImportance
	u.btnDeaf = widget.NewButtonWithIcon("", theme.VolumeDownIcon(), func() { u.toggleDeaf() })
	u.btnDeaf.Importance = widget.LowImportance
	btnSettings := widget.NewButtonWithIcon("", theme.SettingsIcon(), func() { u.showAudioSettings() })
	btnSettings.Importance = widget.LowImportance

	u.meBox = container.NewHBox()
	panel := container.NewBorder(nil, nil, u.meBox,
		container.NewHBox(u.btnMute, u.btnDeaf, btnSettings),
		container.NewCenter(u.ping.box),
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
	name := txt(u.me.Name, colText, 12, true)
	sub := txt("в сети", colGreen, 10, false)
	if u.deafened {
		sub = txt("звук выключен", colRed, 10, false)
	} else if u.muted {
		sub = txt("микрофон выключен", colRed, 10, false)
	}
	u.meBox.Add(u.avatarRing(u.me, 30, u.speaking[u.me.ID] && !u.muted))
	u.meBox.Add(container.NewVBox(name, sub))
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
		u.audio.Beep([]float64{523, 659, 784}, 0.12, 0.16)
	})

	content := container.NewVBox(
		txt("МИКРОФОН", colDim, 11, true), micSel,
		txt("НАУШНИКИ / ДИНАМИКИ", colDim, 11, true), outSel,
		test,
		status,
		txt("если воткнул наушники после запуска — выбери их тут", colDim, 10, false),
	)
	d := dialog.NewCustom("звук", "закрыть", content, u.win)
	d.Resize(fyne.NewSize(420, 340))
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
	if u.voice == nil || u.voiceCh == "" {
		u.voiceBox.Refresh()
		return
	}

	chName, grName := u.voiceNames()
	head := container.NewHBox(
		txt("●", colGreen, 14, true),
		txt(u.voiceStatus, colGreen, 12, true),
	)
	u.voiceTimer = txt("00:00", colDim, 11, false)
	u.voicePing = newPingBars()
	u.voicePing.set(u.voice.RTTMS())

	where := txt(chName+" · "+grName, colDim, 11, false)

	leave := widget.NewButtonWithIcon("отключиться", theme.CancelIcon(), func() { u.leaveVoice() })
	leave.Importance = widget.DangerImportance

	u.voiceBox.Add(container.NewPadded(container.NewVBox(
		head,
		where,
		container.NewHBox(u.voiceTimer, layoutSpacer(6), u.voicePing.box),
		leave,
	)))
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
	u.voiceStatus = "подключаюсь…"
	u.renderVoicePanel()
	u.renderChannels()

	// mine указывает на «своё» соединение: колбэки старого клиента прилетают
	// уже после того, как мы переключились на новый канал, и без этой проверки
	// они бы стёрли состояние нового подключения
	var mine *VoiceClient

	ev := VoiceEvents{
		OnMemberJoin: func(m hub.MemberInfo) {
			u.audio.Beep([]float64{392, 587}, 0.09, 0.10)
		},
		OnMemberLeave: func(id string) {
			u.audio.Beep([]float64{587, 392}, 0.09, 0.10)
		},
		OnRTC: func(state string) {
			fyne.Do(func() {
				if mine == nil || u.voice != mine {
					return
				}
				switch state {
				case "connected":
					u.voiceStatus = "голос подключён"
					u.audio.Beep([]float64{523, 784}, 0.08, 0.10)
				case "connecting", "new":
					u.voiceStatus = "подключаюсь…"
				case "failed", "disconnected":
					u.voiceStatus = "связь потеряна"
				default:
					u.voiceStatus = state
				}
				u.renderVoicePanel()
			})
		},
		OnClosed: func(reason string) {
			fyne.Do(func() {
				if mine == nil || u.voice != mine {
					return // это отвалилось прошлое подключение, оно уже неактуально
				}
				u.voice = nil
				u.voiceCh = ""
				u.voiceStatus = ""
				u.renderVoicePanel()
				u.renderChannels()
				u.toast("голос отключился: " + reason)
			})
		},
	}

	go func() {
		vc, err := JoinVoice(u.api, u.audio, chID, ev)
		fyne.Do(func() {
			if err != nil {
				if u.voiceCh == chID {
					u.voiceCh = ""
					u.voiceStatus = ""
					u.renderVoicePanel()
					u.renderChannels()
				}
				u.toast("не зашёл в «" + name + "»: " + err.Error())
				return
			}
			// пока мы подключались, юзер мог уйти в другой канал
			if u.voiceCh != chID {
				vc.Close("передумал")
				return
			}
			mine = vc
			u.voice = vc
			vc.SetState(u.muted || !u.micOK, u.deafened)
			u.renderVoicePanel()
			u.renderChannels()
		})
	}()
}

func (u *UI) leaveVoice() {
	if v := u.voice; v != nil {
		u.voice = nil // сначала снимаем «текущий», чтобы колбэк ничего не трогал
		v.Close("вышел")
	}
	u.voiceCh = ""
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
	u.audio.Beep([]float64{map[bool]float64{true: 233, false: 349}[u.muted]}, 0.05, 0.06)
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
		u.btnMute.SetIcon(theme.VolumeMuteIcon())
		u.btnMute.Importance = widget.DangerImportance
	} else {
		u.btnMute.SetIcon(theme.VolumeUpIcon())
		u.btnMute.Importance = widget.LowImportance
	}
	if u.deafened {
		u.btnDeaf.SetIcon(theme.VolumeMuteIcon())
		u.btnDeaf.Importance = widget.DangerImportance
	} else {
		u.btnDeaf.SetIcon(theme.VolumeDownIcon())
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
	t := time.NewTicker(150 * time.Millisecond)
	defer t.Stop()
	tick := 0
	for range t.C {
		tick++
		if u.voice == nil {
			if len(u.speaking) > 0 {
				fyne.Do(func() {
					u.speaking = map[string]bool{}
					u.renderChannels()
					u.renderMembers()
				})
			}
			continue
		}

		// два порога вместо одного: иначе на границе тишины индикатор
		// мигает по несколько раз в секунду и рябит в глазах
		const (
			levelOn  = 0.05
			levelOff = 0.02
		)
		next := map[string]bool{}
		for id, v := range u.audio.Levels() {
			key := id
			if id == "" {
				key = u.me.ID // "" — это мой собственный микрофон
			}
			if key == "" {
				continue
			}
			switch {
			case v > levelOn:
				next[key] = true
			case v < levelOff:
				next[key] = false
			default:
				next[key] = u.speaking[key]
			}
		}
		changed := len(next) != len(u.speaking)
		if !changed {
			for k, v := range next {
				if u.speaking[k] != v {
					changed = true
					break
				}
			}
		}

		everySecond := tick%7 == 0
		if changed || everySecond {
			fyne.Do(func() {
				if changed {
					u.speaking = next
					u.renderChannels()
					u.renderMembers()
				}
				if everySecond {
					u.updateVoiceStats()
				}
			})
		}
	}
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
