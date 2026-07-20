package desktop

import (
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

	u.updateChatHeader()
	header := container.NewVBox(
		container.NewPadded(container.NewHBox(txt("#", colDim, 15, false), u.chanTitle)),
		canvas.NewLine(colLine),
	)
	return container.NewBorder(header, container.NewPadded(input), nil, nil, u.msgScroll)
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

/* ---------- сообщения ---------- */

func (u *UI) renderHistory(chID string, msgs []hub.OutMsg) {
	if chID != u.curChan {
		return
	}
	u.msgsBox.Objects = nil
	u.lastAuthor, u.lastTS = "", 0
	for _, m := range msgs {
		u.msgsBox.Add(u.msgWidget(m))
	}
	u.msgsBox.Refresh()
	u.msgScroll.ScrollToBottom()
}

func (u *UI) onChat(m hub.OutMsg) {
	mine := m.From.ID == u.me.ID
	// уведомляем только про чужие сообщения в других каналах: пиликать на
	// канал, который открыт прямо сейчас, — раздражает
	if !mine && m.Ch != u.curChan {
		u.app.SendNotification(fyne.NewNotification(m.From.Name+" · "+u.chanName(m.Ch), notifyBody(m)))
		u.audio.Beep([]float64{740}, 0.06, 0.05)
	}
	fyne.Do(func() {
		if m.Ch != u.curChan {
			u.unread[m.Ch] = true
			u.renderChannels()
			u.renderRail()
			return
		}
		u.msgsBox.Add(u.msgWidget(m))
		u.msgsBox.Refresh()
		u.msgScroll.ScrollToBottom()
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

func notifyBody(m hub.OutMsg) string {
	if m.Text != "" {
		return m.Text
	}
	return "картинка"
}

// msgWidget рисует сообщение. Подряд идущие сообщения одного автора
// склеиваются в блок (как в Discord) — иначе аватарка и ник повторяются
// на каждой строчке и лента превращается в кашу.
func (u *UI) msgWidget(m hub.OutMsg) fyne.CanvasObject {
	grouped := m.From.ID == u.lastAuthor && m.TS-u.lastTS < 5*60*1000
	u.lastAuthor, u.lastTS = m.From.ID, m.TS

	body := container.NewVBox()
	if m.Text != "" {
		lbl := widget.NewLabel(m.Text)
		lbl.Wrapping = fyne.TextWrapWord
		body.Add(lbl)
	}
	if m.Img != "" {
		body.Add(u.imageBubble(m.Img))
	}

	if grouped {
		// выравниваем по ширине аватарки, чтобы текст шёл ровной колонкой
		return container.NewBorder(nil, nil, sized(38, 1, canvas.NewRectangle(colMain)), nil, body)
	}
	head := container.NewHBox(
		txt(m.From.Name, hexColor(m.From.Color.Hex), 13, true),
		txt(time.UnixMilli(m.TS).Format("15:04"), colDim, 10, false),
	)
	return container.NewPadded(container.NewBorder(nil, nil,
		container.NewVBox(u.avatar(m.From, 32)), nil,
		container.NewVBox(head, body),
	))
}

func transparent() *canvas.Rectangle {
	r := canvas.NewRectangle(colMain)
	r.FillColor = colMain
	return r
}

// imageBubble — превью картинки; по клику открывается просмотрщик.
func (u *UI) imageBubble(path string) fyne.CanvasObject {
	full := u.api.AbsURL(path)
	img := canvas.NewImageFromResource(nil)
	img.FillMode = canvas.ImageFillContain
	img.SetMinSize(fyne.NewSize(260, 190))

	u.avatars.fetch(full, func(res fyne.Resource) {
		img.Resource = res
		img.Refresh()
	})

	row := newTapRow(container.NewPadded(img), func() { u.showImage(full) })
	return container.NewGridWrap(fyne.NewSize(280, 210), row)
}

// showImage — просмотрщик в отдельном окне: раньше картинку можно было
// только открыть ссылкой в браузере, что для чата странно.
func (u *UI) showImage(full string) {
	w := u.app.NewWindow("картинка")
	w.Resize(fyne.NewSize(900, 640))

	img := canvas.NewImageFromResource(nil)
	img.FillMode = canvas.ImageFillContain
	u.avatars.fetch(full, func(res fyne.Resource) {
		img.Resource = res
		img.Refresh()
	})

	openBrowser := widget.NewButtonWithIcon("открыть в браузере", theme.ComputerIcon(), func() {
		if pu, err := url.Parse(full); err == nil {
			u.app.OpenURL(pu)
		}
	})
	save := widget.NewButtonWithIcon("сохранить", theme.DocumentSaveIcon(), func() {
		res, ok := u.avatars.get(full)
		if !ok {
			u.toast("картинка ещё качается")
			return
		}
		u.saveImage(res, full, w)
	})
	bar := container.NewHBox(openBrowser, save)

	w.SetContent(filled(colMain, container.NewBorder(nil, container.NewPadded(bar), nil, nil,
		container.NewPadded(img))))
	w.Show()
}

func (u *UI) saveImage(res fyne.Resource, full string, parent fyne.Window) {
	name := filepath.Base(full)
	if path, err := nativeSaveDialog(name); err == nil && path != "" {
		if err := os.WriteFile(path, res.Content(), 0o644); err != nil {
			u.toast("не сохранилось: " + err.Error())
			return
		}
		u.toast("сохранил: " + path)
		return
	}
	// нативного диалога нет — просим Fyne
	d := dialog.NewFileSave(func(wc fyne.URIWriteCloser, err error) {
		if err != nil || wc == nil {
			return
		}
		defer wc.Close()
		if _, err := wc.Write(res.Content()); err != nil {
			u.toast("не сохранилось: " + err.Error())
			return
		}
		u.toast("сохранил")
	}, parent)
	d.SetFileName(name)
	d.Show()
}

/* ---------- отправка ---------- */

func (u *UI) sendChat(text string) {
	text = strings.TrimSpace(text)
	if text == "" || u.curChan == "" {
		return
	}
	u.gw.SendChat(u.curChan, text, "")
	u.chatEntry.SetText("")
}

/* ---------- картинки ---------- */

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
	fyne.Do(func() {
		if u.chatEntry != nil {
			text = u.chatEntry.Text
			u.chatEntry.SetText("")
		}
	})
	link, err := u.api.Upload(name, data)
	if err != nil {
		u.toast("картинка не улетела: " + err.Error())
		return
	}
	u.gw.SendChat(ch, text, link)
}
