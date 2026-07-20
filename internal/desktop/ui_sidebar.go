package desktop

import (
	"fmt"
	"image/color"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/slime4ik/backmess/internal/auth"
	"github.com/slime4ik/backmess/internal/hub"
)

/* ---------- каркас ---------- */

func (u *UI) buildShell() {
	u.railBox = container.NewVBox()
	u.chanBox = container.NewVBox()
	u.membersBox = container.NewVBox()
	u.msgsBox = container.NewVBox()
	u.msgScroll = container.NewVScroll(u.msgsBox)
	u.ping = newPingBars()

	rail := filled(colRail, container.NewBorder(
		nil,
		container.NewPadded(u.railAddButton()),
		nil, nil,
		container.NewVScroll(container.NewPadded(u.railBox)),
	))

	u.groupTitle = txt("mess", colText, 15, true)
	groupHeader := container.NewBorder(nil, nil, nil,
		u.groupMenuButton(),
		container.NewPadded(u.groupTitle),
	)

	u.voiceBox = container.NewVBox()
	sidebar := filled(colSide, container.NewBorder(
		container.NewVBox(container.NewPadded(groupHeader), canvas.NewLine(colLine)),
		container.NewVBox(u.voiceBox, canvas.NewLine(colLine), u.userPanel()),
		nil, nil,
		container.NewVScroll(container.NewPadded(u.chanBox)),
	))

	u.chatArea = container.NewStack()
	members := filled(colSide, container.NewBorder(
		container.NewVBox(container.NewPadded(txt("УЧАСТНИКИ", colDim, 11, true)), canvas.NewLine(colLine)),
		nil, nil, nil,
		container.NewVScroll(container.NewPadded(u.membersBox)),
	))

	u.win.SetContent(container.NewBorder(nil, nil,
		container.NewHBox(column(68, rail), column(232, sidebar)),
		column(200, members),
		filled(colMain, u.chatArea),
	))

	// перетаскивание картинок прямо в окно — самый быстрый способ кинуть скрин
	u.win.SetOnDropped(func(_ fyne.Position, uris []fyne.URI) { u.dropFiles(uris) })
}

func (u *UI) renderAll() {
	if u.railBox == nil {
		return // каркас ещё не построен (экран логина)
	}
	u.renderRail()
	u.renderChannels()
	u.renderMembers()
	u.renderChatArea()
	u.renderVoicePanel()
	u.renderMe()
	u.updateAVButtons()
	if g := u.curGroupView(); g != nil {
		u.groupTitle.Text = g.Name
	} else {
		u.groupTitle.Text = "mess"
	}
	u.groupTitle.Refresh()
}

/* ---------- рельса групп ---------- */

func (u *UI) renderRail() {
	if u.railBox == nil {
		return
	}
	u.railBox.Objects = nil
	for _, g := range u.groups {
		g := g
		active := g.ID == u.curGroup

		badge := canvas.NewRectangle(color.Transparent)
		badge.CornerRadius = 2
		if u.groupHasUnread(g) {
			badge.FillColor = colAmber
		} else if active {
			badge.FillColor = colText
		}

		icon := canvas.NewCircle(hexColor(auth.ColorFor(g.ID).Hex))
		if active {
			icon.FillColor = colAmber
		}
		label := txt(initials(g.Name), color.NRGBA{0x11, 0x14, 0x18, 0xFF}, 15, true)
		label.Alignment = fyne.TextAlignCenter
		avatar := sized(42, 42, container.NewStack(icon, container.NewCenter(label)))

		row := newTapRow(
			container.NewHBox(sized(3, 42, badge), avatar),
			func() { u.selectGroup(g.ID) },
		)
		row.onSecondary = func(pos fyne.Position) { u.groupMenu(g, pos) }
		u.railBox.Add(row)
	}
	u.railBox.Refresh()
}

func (u *UI) groupHasUnread(g hub.GroupView) bool {
	for _, c := range g.Channels {
		if u.unread[c.ID] {
			return true
		}
	}
	return false
}

func initials(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "?"
	}
	parts := strings.Fields(name)
	if len(parts) >= 2 {
		return strings.ToUpper(string([]rune(parts[0])[:1]) + string([]rune(parts[1])[:1]))
	}
	r := []rune(name)
	if len(r) >= 2 {
		return strings.ToUpper(string(r[:2]))
	}
	return strings.ToUpper(string(r))
}

func (u *UI) railAddButton() *widget.Button {
	b := widget.NewButtonWithIcon("", theme.ContentAddIcon(), func() { u.showAddGroup() })
	b.Importance = widget.LowImportance
	return b
}

/* ---------- список каналов ---------- */

func (u *UI) renderChannels() {
	if u.chanBox == nil {
		return
	}
	u.chanBox.Objects = nil
	u.avChannels = map[string][]*avatarView{}
	g := u.curGroupView()
	if g == nil {
		u.chanBox.Refresh()
		return
	}

	addSection := func(title, kind string) {
		var chans []*channelRef
		for _, c := range g.Channels {
			if c.Kind == kind {
				chans = append(chans, &channelRef{ID: c.ID, Name: c.Name, Kind: c.Kind})
			}
		}
		if len(chans) == 0 {
			return
		}
		head := container.NewBorder(nil, nil, nil,
			u.addChannelButton(g.ID, kind),
			txt(title, colDim, 11, true),
		)
		u.chanBox.Add(container.NewPadded(head))
		for _, c := range chans {
			c := c
			u.chanBox.Add(u.channelRow(*g, c))
			// под голосовым каналом — кто в нём сидит; это главное, чего
			// не хватало: видно состав, ещё не заходя внутрь
			if c.Kind == "voice" {
				for _, vm := range g.Voice[c.ID] {
					u.chanBox.Add(u.voiceMemberRow(vm))
				}
			}
		}
	}

	addSection("ТЕКСТОВЫЕ", "text")
	addSection("ГОЛОСОВЫЕ", "voice")
	u.chanBox.Refresh()
}

type channelRef struct{ ID, Name, Kind string }

func (u *UI) channelRow(g hub.GroupView, c *channelRef) fyne.CanvasObject {
	mark := "#"
	if c.Kind == "voice" {
		mark = "🔊"
	}
	nameCol := colDim
	active := (c.Kind == "text" && c.ID == u.curChan) || (c.Kind == "voice" && c.ID == u.voiceCh)
	if active {
		nameCol = colText
	}
	if u.unread[c.ID] {
		nameCol = colText
	}

	line := container.NewHBox(txt(mark, colDim, 13, false), txt(c.Name, nameCol, 13, u.unread[c.ID]))
	if u.unread[c.ID] {
		dot := canvas.NewCircle(colAmber)
		line.Add(sized(7, 7, dot))
	}
	if c.Kind == "voice" && c.ID == u.voiceCh {
		line.Add(txt("ты тут", colGreen, 10, true))
	}

	row := newTapRow(container.NewPadded(line), func() {
		if c.Kind == "voice" {
			u.joinVoice(c.ID, c.Name)
		} else {
			u.openChannel(c.ID)
		}
	})
	row.SetActive(active)
	row.onSecondary = func(pos fyne.Position) { u.channelMenu(g, *c, pos) }
	return row
}

// voiceMemberRow — участник внутри голосового канала: аватарка, ник и
// значки мута/глухоты. Говорящего обводим зелёным кольцом.
func (u *UI) voiceMemberRow(vm hub.VoiceMember) fyne.CanvasObject {
	speaking := u.speaking[vm.User.ID] && !vm.State.Muted
	nameCol := colDim
	if speaking {
		nameCol = colSpeak
	}
	av := u.avatarView(vm.User, 20)
	av.setSpeaking(speaking, u.levels[vm.User.ID])
	u.avChannels[vm.User.ID] = append(u.avChannels[vm.User.ID], av)
	line := container.NewHBox(
		sized(10, 14, canvas.NewRectangle(color.Transparent)), // отступ вложенности
		av.obj,
		txt(vm.User.Name, nameCol, 12, false),
	)
	if vm.State.Deafened {
		line.Add(txt("глух", colRed, 10, true))
	} else if vm.State.Muted {
		line.Add(txt("мут", colRed, 10, true))
	}
	user := vm.User
	row := newTapRow(container.NewPadded(line), nil)
	row.onSecondary = func(pos fyne.Position) { u.memberMenu(user, pos) }
	return row
}

// memberMenu — всплывашка по правому клику на человеке: сразу ползунок
// громкости, без промежуточного окна. Настройка локальная, применяется в
// микшере и никого, кроме тебя, не касается.
func (u *UI) memberMenu(user auth.User, pos fyne.Position) {
	if user.ID == u.me.ID {
		return // себе громкость не крутят
	}

	cur := u.audio.UserVolume(user.ID)
	val := txt(fmt.Sprintf("%d%%", cur), colText, 11, true)

	sl := widget.NewSlider(0, 200)
	sl.Step = 5
	sl.Value = float64(cur)
	sl.OnChanged = func(v float64) {
		val.Text = fmt.Sprintf("%d%%", int(v))
		val.Refresh()
		u.audio.SetUserVolume(user.ID, int(v))
		u.app.Preferences().SetInt("vol:"+user.ID, int(v))
	}

	var pop *widget.PopUp
	reset := widget.NewButton("сброс", func() { sl.SetValue(100) })
	reset.Importance = widget.LowImportance
	closeBtn := widget.NewButtonWithIcon("", theme.CancelIcon(), func() { pop.Hide() })
	closeBtn.Importance = widget.LowImportance

	bg := canvas.NewRectangle(colSide)
	bg.CornerRadius = 8
	bg.StrokeColor = colLine
	bg.StrokeWidth = 1

	card := container.NewVBox(
		container.NewBorder(nil, nil,
			container.NewHBox(u.avatar(user, 22), txt(user.Name, colText, 12, true)),
			closeBtn, nil),
		container.NewBorder(nil, nil, txt("громкость", colDim, 11, false), val, nil),
		sl,
		reset,
	)
	content := container.NewStack(bg, container.NewPadded(card))
	pop = widget.NewPopUp(container.NewGridWrap(fyne.NewSize(240, 150), content), u.win.Canvas())
	pop.ShowAtPosition(pos)
}

func (u *UI) addChannelButton(gid, kind string) *widget.Button {
	b := widget.NewButtonWithIcon("", theme.ContentAddIcon(), func() {
		title := "новый текстовый канал"
		if kind == "voice" {
			title = "новый голосовой канал"
		}
		entry := widget.NewEntry()
		entry.SetPlaceHolder("название")
		d := dialog.NewForm(title, "создать", "отмена",
			[]*widget.FormItem{{Text: "имя", Widget: entry}},
			func(ok bool) {
				if !ok || strings.TrimSpace(entry.Text) == "" {
					return
				}
				name := entry.Text
				go func() {
					if err := u.api.AddChannel(gid, name, kind); err != nil {
						u.toast("канал не создался: " + err.Error())
					}
				}()
			}, u.win)
		d.Resize(fyne.NewSize(360, 180))
		d.Show()
	})
	b.Importance = widget.LowImportance
	return b
}

/* ---------- контекстные меню ---------- */

func (u *UI) channelMenu(g hub.GroupView, c channelRef, pos fyne.Position) {
	items := []*fyne.MenuItem{}
	if c.Kind == "text" {
		items = append(items, fyne.NewMenuItem("открыть", func() { u.openChannel(c.ID) }))
	} else {
		items = append(items, fyne.NewMenuItem("зайти", func() { u.joinVoice(c.ID, c.Name) }))
	}
	if g.Owner == u.me.ID {
		items = append(items,
			fyne.NewMenuItem("переименовать", func() { u.renameChannelDialog(g.ID, c) }),
			fyne.NewMenuItem("удалить", func() { u.deleteChannel(g.ID, c) }),
		)
	}
	widget.ShowPopUpMenuAtPosition(fyne.NewMenu("", items...), u.win.Canvas(), pos)
}

func (u *UI) renameChannelDialog(gid string, c channelRef) {
	entry := widget.NewEntry()
	entry.SetText(c.Name)
	d := dialog.NewForm("переименовать канал", "ок", "отмена",
		[]*widget.FormItem{{Text: "имя", Widget: entry}},
		func(ok bool) {
			if !ok || strings.TrimSpace(entry.Text) == "" {
				return
			}
			name := entry.Text
			go func() {
				if err := u.api.RenameChannel(gid, c.ID, name); err != nil {
					u.toast("не переименовалось: " + err.Error())
				}
			}()
		}, u.win)
	d.Resize(fyne.NewSize(360, 180))
	d.Show()
}

// deleteChannel убирает канал из UI сразу, не дожидаясь ответа сервера:
// раньше приходилось ждать до пяти секунд и казалось, что кнопка не работает.
// Если сервер откажет — вернём его на место следующим событием группы.
func (u *UI) deleteChannel(gid string, c channelRef) {
	dialog.ShowConfirm("удалить канал?", "«"+c.Name+"» исчезнет вместе с перепиской", func(ok bool) {
		if !ok {
			return
		}
		if c.ID == u.voiceCh {
			u.leaveVoice()
		}
		u.dropChannelLocally(gid, c.ID)
		go func() {
			if err := u.api.DeleteChannel(gid, c.ID); err != nil {
				u.toast("не удалилось: " + err.Error())
			}
		}()
	}, u.win)
}

func (u *UI) dropChannelLocally(gid, chID string) {
	g := u.group(gid)
	if g == nil {
		return
	}
	out := g.Channels[:0]
	for _, c := range g.Channels {
		if c.ID != chID {
			out = append(out, c)
		}
	}
	g.Channels = out
	if u.curChan == chID {
		u.curChan = firstTextChannel(*g)
		if u.curChan != "" {
			u.openChannel(u.curChan)
		}
	}
	u.renderChannels()
	u.renderChatArea()
}

func (u *UI) groupMenuButton() *widget.Button {
	var b *widget.Button
	b = widget.NewButtonWithIcon("", theme.MoreVerticalIcon(), func() {
		g := u.curGroupView()
		if g == nil {
			return
		}
		// меню вешаем под саму кнопку, а не в угол экрана
		pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(b)
		u.groupMenu(*g, pos.AddXY(0, b.Size().Height))
	})
	b.Importance = widget.LowImportance
	return b
}

func (u *UI) groupMenu(g hub.GroupView, pos fyne.Position) {
	items := []*fyne.MenuItem{
		fyne.NewMenuItem("скопировать ссылку-приглашение", func() {
			link := u.api.InviteURL(g.Invite)
			u.app.Clipboard().SetContent(link)
			u.toast("ссылка скопирована: " + link)
		}),
	}
	if g.Owner == u.me.ID {
		items = append(items, fyne.NewMenuItem("удалить группу", func() {
			dialog.ShowConfirm("удалить группу?", "«"+g.Name+"» исчезнет у всех участников", func(ok bool) {
				if !ok {
					return
				}
				go func() {
					if err := u.api.DeleteGroup(g.ID); err != nil {
						u.toast("не удалилось: " + err.Error())
					}
				}()
			}, u.win)
		}))
	} else {
		items = append(items, fyne.NewMenuItem("выйти из группы", func() {
			go func() {
				if err := u.api.LeaveGroup(g.ID); err != nil {
					u.toast("не вышло: " + err.Error())
				}
			}()
		}))
	}
	widget.ShowPopUpMenuAtPosition(fyne.NewMenu("", items...), u.win.Canvas(), pos)
}

/* ---------- создание/вступление в группу ---------- */

func (u *UI) showAddGroup() {
	name := widget.NewEntry()
	name.SetPlaceHolder("название группы")
	code := widget.NewEntry()
	code.SetPlaceHolder("ссылка или код приглашения")

	create := widget.NewButton("создать группу", func() {
		if strings.TrimSpace(name.Text) == "" {
			return
		}
		v := name.Text
		go func() {
			if err := u.api.CreateGroup(v); err != nil {
				u.toast("не создалась: " + err.Error())
			}
		}()
	})
	create.Importance = widget.HighImportance
	join := widget.NewButton("войти по коду", func() {
		if strings.TrimSpace(code.Text) == "" {
			return
		}
		v := code.Text
		go func() {
			if err := u.api.JoinGroup(v); err != nil {
				u.toast("не зашёл: " + err.Error())
			}
		}()
	})

	content := container.NewVBox(
		txt("СОЗДАТЬ СВОЮ", colDim, 11, true), name, create,
		widget.NewSeparator(),
		txt("ИЛИ ПРИСОЕДИНИТЬСЯ", colDim, 11, true), code, join,
	)
	d := dialog.NewCustom("группы", "закрыть", content, u.win)
	d.Resize(fyne.NewSize(400, 340))
	d.Show()
}

/* ---------- участники группы ---------- */

func (u *UI) renderMembers() {
	if u.membersBox == nil {
		return
	}
	u.membersBox.Objects = nil
	u.avMembers = map[string][]*avatarView{}
	g := u.curGroupView()
	if g == nil {
		u.membersBox.Refresh()
		return
	}

	type entry struct {
		user   auth.User
		online bool
	}
	list := make([]entry, 0, len(g.Users))
	for _, us := range g.Users {
		list = append(list, entry{user: us, online: g.Online[us.ID]})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].online != list[j].online {
			return list[i].online
		}
		return strings.ToLower(list[i].user.Name) < strings.ToLower(list[j].user.Name)
	})

	online, offline := 0, 0
	for _, e := range list {
		if e.online {
			online++
		} else {
			offline++
		}
	}

	section := func(title string, want bool, n int) {
		if n == 0 {
			return
		}
		u.membersBox.Add(container.NewPadded(txt(fmt.Sprintf("%s — %d", title, n), colDim, 11, true)))
		for _, e := range list {
			if e.online != want {
				continue
			}
			speaking := u.speaking[e.user.ID]
			nameCol := colText
			if !e.online {
				nameCol = colOffline
			}
			if speaking {
				nameCol = colSpeak
			}
			name := txt(e.user.Name, nameCol, 13, false)
			av := u.avatarView(e.user, 24)
			av.setSpeaking(speaking, u.levels[e.user.ID])
			u.avMembers[e.user.ID] = append(u.avMembers[e.user.ID], av)
			row := container.NewHBox(
				av.obj,
				container.NewVBox(name),
				statusDot(e.online, 8),
			)
			if e.user.ID == g.Owner {
				row.Add(txt("★", colAmber, 11, false))
			}
			user := e.user
			tap := newTapRow(container.NewPadded(row), nil)
			tap.onSecondary = func(pos fyne.Position) { u.memberMenu(user, pos) }
			u.membersBox.Add(tap)
		}
	}
	section("В СЕТИ", true, online)
	section("НЕ В СЕТИ", false, offline)
	u.membersBox.Refresh()
}
