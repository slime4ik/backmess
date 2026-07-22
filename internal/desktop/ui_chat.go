package desktop

import (
	"fmt"
	"image/color"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/slime4ik/backmess/internal/hub"
)

/* ---------- центральная область ---------- */

// renderChatArea показывает либо чат выбранного канала, либо понятную
// заглушку: пустое серое поле без объяснений — главная причина ощущения,
// что приложение мёртвое.
func (u *UI) renderChatArea() {
	if u.chatArea == nil {
		return
	}
	if len(u.groups) == 0 {
		u.chatArea.Objects = []fyne.CanvasObject{u.emptyState(
			"тут пока пусто",
			"создай свою группу или зайди в чужую по ссылке-приглашению",
			"создать или войти", func() { u.showAddGroup() },
		)}
		u.chatArea.Refresh()
		return
	}
	if u.curChan == "" {
		u.chatArea.Objects = []fyne.CanvasObject{u.emptyState(
			"нет текстового канала",
			"создай канал в списке слева — плюсиком рядом с «ТЕКСТОВЫЕ»",
			"", nil,
		)}
		u.chatArea.Refresh()
		return
	}
	u.chatArea.Objects = []fyne.CanvasObject{u.chatPane()}
	u.chatArea.Refresh()
}

func (u *UI) emptyState(title, sub, btnText string, action func()) fyne.CanvasObject {
	t := txt(title, colText, 20, true)
	t.Alignment = fyne.TextAlignCenter
	s := txt(sub, colDim, 13, false)
	s.Alignment = fyne.TextAlignCenter
	box := container.NewVBox(t, s)
	if btnText != "" && action != nil {
		b := widget.NewButton(btnText, action)
		b.Importance = widget.HighImportance
		box.Add(container.NewCenter(b))
	}
	return container.NewCenter(container.NewGridWrap(fyne.NewSize(420, 160), box))
}

func (u *UI) chatPane() fyne.CanvasObject {
	u.chanTitle = txt("", colText, 15, true)

	u.chatEntry = widget.NewEntry()
	u.chatEntry.SetPlaceHolder("написать сообщение…")
	u.chatEntry.OnSubmitted = func(s string) { u.sendChat(s) }

	attach := widget.NewButtonWithIcon("", theme.MediaPhotoIcon(), func() { u.pickImage() })
	attach.Importance = widget.LowImportance
	send := widget.NewButtonWithIcon("", theme.MailSendIcon(), func() { u.sendChat(u.chatEntry.Text) })
	send.Importance = widget.LowImportance

	inputBG := canvas.NewRectangle(colInput)
	inputBG.CornerRadius = 10
	input := container.NewStack(inputBG, container.NewBorder(nil, nil, attach, send, u.chatEntry))

	u.replyBar = container.NewVBox()
	u.renderReplyBar()

	u.updateChatHeader()
	header := container.NewVBox(
		container.NewPadded(container.NewHBox(txt("#", colDim, 15, false), u.chanTitle)),
		canvas.NewLine(colLine),
	)
	bottom := container.NewVBox(u.replyBar, container.NewPadded(input))
	return container.NewBorder(header, bottom, nil, nil, u.msgScroll)
}

func (u *UI) updateChatHeader() {
	if u.chanTitle == nil {
		return
	}
	name := ""
	if g := u.curGroupView(); g != nil {
		for _, c := range g.Channels {
			if c.ID == u.curChan {
				name = c.Name
			}
		}
	}
	u.chanTitle.Text = name
	u.chanTitle.Refresh()
}

/* ---------- ответы ---------- */

// renderReplyBar — полоска «отвечаю такому-то» над полем ввода.
func (u *UI) renderReplyBar() {
	if u.replyBar == nil {
		return
	}
	u.replyBar.Objects = nil
	if u.replyTo == 0 {
		u.replyBar.Refresh()
		return
	}
	m, ok := u.msgByID[u.replyTo]
	if !ok {
		u.replyTo = 0
		u.replyBar.Refresh()
		return
	}
	cancel := widget.NewButtonWithIcon("", theme.CancelIcon(), func() {
		u.replyTo = 0
		u.renderReplyBar()
	})
	cancel.Importance = widget.LowImportance
	line := container.NewBorder(nil, nil,
		container.NewHBox(
			txt("в ответ", colDim, 11, false),
			txt(m.From.Name, hexColor(m.From.Color.Hex), 11, true),
		),
		cancel,
		txt(shorten(msgPreview(m), 70), colDim, 11, false),
	)
	u.replyBar.Add(container.NewPadded(line))
	u.replyBar.Refresh()
}

func msgPreview(m hub.OutMsg) string {
	if m.Text != "" {
		return m.Text
	}
	if m.Img != "" {
		return "картинка"
	}
	return "сообщение"
}

func shorten(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

/* ---------- сообщения ---------- */

func (u *UI) renderHistory(chID string, msgs []hub.OutMsg) {
	if chID != u.curChan {
		return
	}
	u.msgsBox.Objects = nil
	u.msgByID = map[int64]hub.OutMsg{}
	u.lastAuthor, u.lastTS = "", 0
	for _, m := range msgs {
		u.msgByID[m.ID] = m
	}
	for _, m := range msgs {
		u.msgsBox.Add(u.msgWidget(m))
	}
	u.msgsBox.Refresh()
	u.scrollToBottom()
}

// scrollToBottom прокручивает ленту вниз, к свежим сообщениям. Второй вызов с
// задержкой обязателен: сразу после Refresh размеры содержимого ещё не
// пересчитаны, и одиночный ScrollToBottom оставлял ленту наверху, на самых
// старых сообщениях.
func (u *UI) scrollToBottom() {
	if u.msgScroll == nil {
		return
	}
	u.msgScroll.ScrollToBottom()
	sc := u.msgScroll
	time.AfterFunc(60*time.Millisecond, func() {
		fyne.Do(func() {
			if u.msgScroll == sc {
				sc.ScrollToBottom()
			}
		})
	})
}

func (u *UI) onChat(m hub.OutMsg) {
	mine := m.From.ID == u.me.ID
	// ответ именно на твоё сообщение — повод уведомить даже в открытом канале:
	// его легко пропустить, если лента ушла вверх
	replyToMe := false
	if m.ReplyTo != 0 && !mine {
		if src, ok := u.msgByID[m.ReplyTo]; ok && src.From.ID == u.me.ID {
			replyToMe = true
		}
	}
	// в остальном уведомляем только про чужие сообщения в других каналах:
	// пиликать на канал, который открыт прямо сейчас, — раздражает
	if replyToMe {
		u.notify.notify(m.From.Name+" ответил тебе · "+u.chanName(m.Ch), msgPreview(m))
		u.audio.PlaySound(SoundReply)
	} else if !mine && m.Ch != u.curChan {
		u.notify.notify(m.From.Name+" · "+u.chanName(m.Ch), msgPreview(m))
		u.audio.PlaySound(SoundMessage)
	}
	fyne.Do(func() {
		if m.Ch != u.curChan {
			u.unread[m.Ch] = true
			u.renderChannels()
			u.renderRail()
			return
		}
		if u.msgByID == nil {
			u.msgByID = map[int64]hub.OutMsg{}
		}
		u.msgByID[m.ID] = m
		u.msgsBox.Add(u.msgWidget(m))
		u.msgsBox.Refresh()
		u.scrollToBottom()
	})
}

func (u *UI) chanName(chID string) string {
	if g := u.channel(chID); g != nil {
		for _, c := range g.Channels {
			if c.ID == chID {
				return c.Name
			}
		}
	}
	return "чат"
}

// msgWidget рисует сообщение. Подряд идущие сообщения одного автора
// склеиваются в блок (как в Discord) — иначе аватарка и ник повторяются
// на каждой строчке и лента превращается в кашу.
func (u *UI) msgWidget(m hub.OutMsg) fyne.CanvasObject {
	// сообщение-ответ всегда начинает новый блок: иначе не видно, к чему цитата
	grouped := m.ReplyTo == 0 && m.From.ID == u.lastAuthor && m.TS-u.lastTS < 5*60*1000
	u.lastAuthor, u.lastTS = m.From.ID, m.TS

	body := container.New(&tightVBox{spacing: 1})
	if m.ReplyTo != 0 {
		body.Add(u.quoteLine(m.ReplyTo))
	}
	if m.Text != "" {
		lbl := widget.NewLabel(m.Text)
		lbl.Wrapping = fyne.TextWrapWord
		body.Add(lbl)
	}
	if m.Img != "" {
		body.Add(u.imageBubble(m.Img))
	}

	var content fyne.CanvasObject
	if grouped {
		// выравниваем по ширине аватарки, чтобы текст шёл ровной колонкой
		content = container.NewBorder(nil, nil, sized(40, 1, canvas.NewRectangle(colMain)), nil, body)
	} else {
		head := container.NewHBox(
			txt(m.From.Name, hexColor(m.From.Color.Hex), 13, true),
			txt(time.UnixMilli(m.TS).Format("15:04"), colDim, 10, false),
		)
		// ответ именно тебе — подписываем явно: в общей ленте это теряется
		if m.ReplyTo != 0 {
			if src, ok := u.msgByID[m.ReplyTo]; ok && src.From.ID == u.me.ID && m.From.ID != u.me.ID {
				head.Add(txt("ответил тебе", colAmber, 10, true))
			}
		}
		content = container.NewBorder(nil, nil,
			container.NewVBox(u.avatar(m.From, 30)), nil,
			container.New(&tightVBox{spacing: 1}, head, body),
		)
	}

	// Действия — только по правому клику. Панель по наведению мигала и
	// требовала слежения за Shift и хендлеров у каждого сообщения; меню
	// проще, предсказуемее и не создаёт лишних виджетов в ленте.
	row := newTapRow(content, nil)
	row.onSecondary = func(pos fyne.Position) { u.msgMenu(m, pos) }
	return row
}

// deleteMsg спрашивает подтверждение: пункт меню нажимается легко, а
// удаление необратимо.
func (u *UI) deleteMsg(m hub.OutMsg) {
	dialog.ShowConfirm("удалить сообщение?", shorten(msgPreview(m), 60), func(ok bool) {
		if ok {
			u.gw.DeleteMsg(m.Ch, m.ID)
		}
	}, u.win)
}

// onMsgDeleted убирает сообщение из ленты. Проще перерисовать канал целиком:
// у соседних сообщений могла измениться склейка в блоки.
func (u *UI) onMsgDeleted(chID string, id int64) {
	fyne.Do(func() {
		if chID != u.curChan {
			return
		}
		delete(u.msgByID, id)
		if u.replyTo == id {
			u.replyTo = 0
			u.renderReplyBar()
		}
		u.gw.RequestHistory(chID)
	})
}

// quoteLine — блок «на что отвечаем». Раньше это была бледная строчка, и
// по ленте было вообще не понять, что кто-то кому-то ответил. Теперь это
// заметная плашка: стрелка, цветной ник автора и сам текст.
func (u *UI) quoteLine(id int64) fyne.CanvasObject {
	src, ok := u.msgByID[id]
	name, preview := "сообщение", "удалено"
	var nameCol color.Color = colDim
	if ok {
		name, preview = src.From.Name, shorten(msgPreview(src), 60)
		nameCol = hexColor(src.From.Color.Hex)
	}

	bg := canvas.NewRectangle(colInput)
	bg.CornerRadius = 5
	bar := canvas.NewRectangle(nameCol)

	line := container.NewHBox(
		txt("↪", colDim, 12, true),
		txt(name, nameCol, 11, true),
		txt(preview, colDim, 11, false),
	)
	return container.NewStack(bg,
		container.NewBorder(nil, nil, sized(3, 16, bar), nil, container.NewPadded(line)))
}

func (u *UI) msgMenu(m hub.OutMsg, pos fyne.Position) {
	reply := fyne.NewMenuItem("ответить", func() {
		u.replyTo = m.ID
		u.renderReplyBar()
		if u.chatEntry != nil {
			u.win.Canvas().Focus(u.chatEntry)
		}
	})
	reply.Icon = iconReply
	items := []*fyne.MenuItem{reply}

	if m.Text != "" {
		items = append(items, fyne.NewMenuItem("скопировать текст", func() {
			u.app.Clipboard().SetContent(m.Text)
		}))
	}
	if m.Img != "" {
		items = append(items, fyne.NewMenuItem("открыть картинку", func() {
			u.showImage(u.api.AbsURL(m.Img))
		}))
	}
	// удалять можно только свои сообщения — чужие не трогает никто
	if m.From.ID == u.me.ID {
		del := fyne.NewMenuItem("удалить", func() { u.deleteMsg(m) })
		del.Icon = iconTrash
		items = append(items, fyne.NewMenuItemSeparator(), del)
	}
	u.showMenuAt(fyne.NewMenu("", items...), pos)
}

/* ---------- картинки ---------- */

// imageBubble — превью картинки; по клику разворачивается на весь экран.
// В кэше держим уменьшенную копию: оригинал на 1600px нужен только в
// полноэкранном просмотре, а в ленте он лишь ел бы память.
func (u *UI) imageBubble(path string) fyne.CanvasObject {
	full := u.api.AbsURL(path)
	img := canvas.NewImageFromResource(nil)
	img.FillMode = canvas.ImageFillContain
	img.SetMinSize(fyne.NewSize(260, 190))

	u.images.fetch(full, full+"|thumb",
		func(raw []byte) []byte { return thumbnail(raw, 520) },
		func(res fyne.Resource) {
			img.Resource = res
			img.Refresh()
		})

	row := newTapRow(container.NewPadded(img), func() { u.showImage(full) })
	return container.NewGridWrap(fyne.NewSize(280, 210), row)
}

// showImage — просмотр во весь экран поверх окна, как в Discord: отдельное
// окно ради картинки — лишний объект в доке и на панели задач.
func (u *UI) showImage(full string) {
	cv := u.win.Canvas()

	img := canvas.NewImageFromResource(nil)
	img.FillMode = canvas.ImageFillContain
	u.images.fetch(full, full, nil, func(res fyne.Resource) {
		img.Resource = res
		img.Refresh()
	})

	var pop *widget.PopUp
	closeBtn := widget.NewButtonWithIcon("", theme.CancelIcon(), func() { pop.Hide() })
	save := widget.NewButtonWithIcon("сохранить", theme.DocumentSaveIcon(), func() {
		res, ok := u.images.get(full)
		if !ok {
			u.toast("картинка ещё качается")
			return
		}
		u.saveImage(res, full)
	})
	openBrowser := widget.NewButtonWithIcon("в браузере", theme.ComputerIcon(), func() {
		if pu, err := url.Parse(full); err == nil {
			u.app.OpenURL(pu)
		}
	})

	bar := container.NewHBox(save, openBrowser, closeBtn)
	// фон кликабельный: щелчок мимо картинки закрывает просмотр
	backdrop := newTapRow(container.NewPadded(img), func() { pop.Hide() })
	content := container.NewStack(
		canvas.NewRectangle(color.NRGBA{0x0A, 0x0C, 0x0F, 0xF2}),
		container.NewBorder(container.NewPadded(container.NewHBox(layoutSpacer(1), bar)), nil, nil, nil, backdrop),
	)

	pop = widget.NewModalPopUp(content, cv)
	pop.Resize(cv.Size())
	pop.Show()
}

func (u *UI) saveImage(res fyne.Resource, full string) {
	name := filepath.Base(full)
	if i := strings.IndexAny(name, "?#"); i >= 0 {
		name = name[:i]
	}
	go func() {
		path, err := nativeSaveDialog(name)
		if err == nil && path != "" {
			if werr := os.WriteFile(path, res.Content(), 0o644); werr != nil {
				u.toast("не сохранилось: " + werr.Error())
				return
			}
			u.toast("сохранил: " + path)
			return
		}
		if err == errNoNativeDialog {
			fyne.Do(func() {
				d := dialog.NewFileSave(func(wc fyne.URIWriteCloser, derr error) {
					if derr != nil || wc == nil {
						return
					}
					defer wc.Close()
					if _, werr := wc.Write(res.Content()); werr != nil {
						u.toast("не сохранилось: " + werr.Error())
						return
					}
					u.toast("сохранил")
				}, u.win)
				d.SetFileName(name)
				d.Show()
			})
		}
	}()
}

/* ---------- отправка ---------- */

func (u *UI) sendChat(text string) {
	text = strings.TrimSpace(text)
	if text == "" || u.curChan == "" {
		return
	}
	u.gw.SendChat(u.curChan, text, "", u.replyTo)
	u.chatEntry.SetText("")
	u.replyTo = 0
	u.renderReplyBar()
}

// pickImage зовёт системный диалог выбора файла, а если его нет —
// откатывается на встроенный фениевский.
func (u *UI) pickImage() {
	if u.curChan == "" {
		u.toast("сначала открой текстовый канал")
		return
	}
	go func() {
		path, err := nativeOpenDialog()
		if err == nil && path != "" {
			u.uploadPath(path)
			return
		}
		if err == errNoNativeDialog {
			fyne.Do(u.pickImageFyne)
		}
	}()
}

func (u *UI) pickImageFyne() {
	d := dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
		if err != nil || rc == nil {
			return
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, 30<<20))
		if err != nil {
			return
		}
		u.upload(rc.URI().Name(), data)
	}, u.win)
	d.SetFilter(storage.NewExtensionFileFilter(imageExts))
	d.Show()
}

var imageExts = []string{".png", ".jpg", ".jpeg", ".gif", ".webp"}

// dropFiles — картинки, брошенные мышкой в окно.
func (u *UI) dropFiles(uris []fyne.URI) {
	if u.curChan == "" {
		u.toast("сначала открой текстовый канал")
		return
	}
	for _, uri := range uris {
		p := uri.Path()
		if p == "" || !isImagePath(p) {
			continue
		}
		go u.uploadPath(p)
	}
}

func isImagePath(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	for _, e := range imageExts {
		if ext == e {
			return true
		}
	}
	return false
}

func (u *UI) uploadPath(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		u.toast("не прочитал файл: " + err.Error())
		return
	}
	u.upload(filepath.Base(path), data)
}

func (u *UI) upload(name string, data []byte) {
	ch := u.curChan
	text := ""
	reply := int64(0)
	fyne.Do(func() {
		if u.chatEntry != nil {
			text = u.chatEntry.Text
			u.chatEntry.SetText("")
		}
		reply = u.replyTo
		u.replyTo = 0
		u.renderReplyBar()
	})
	link, err := u.api.Upload(name, data)
	if err != nil {
		u.toast("картинка не улетела: " + err.Error())
		return
	}
	u.gw.SendChat(ch, text, link, reply)
}

var _ = fmt.Sprintf
