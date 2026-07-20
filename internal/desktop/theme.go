package desktop

import (
	"fmt"
	"image/color"
	"io"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	fynedesktop "fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/slime4ik/backmess/internal/auth"
)

// Палитра: тёмная, спокойная, с одним акцентом. Ориентир — Discord, но без
// попытки его копировать один в один.
var (
	colRail    = color.NRGBA{0x0A, 0x0C, 0x0F, 0xFF} // полоса групп слева
	colSide    = color.NRGBA{0x12, 0x15, 0x1A, 0xFF} // список каналов
	colMain    = color.NRGBA{0x17, 0x1A, 0x20, 0xFF} // чат
	colInput   = color.NRGBA{0x1E, 0x22, 0x29, 0xFF}
	colHover   = color.NRGBA{0xFF, 0xFF, 0xFF, 0x0E}
	colActive  = color.NRGBA{0xFF, 0xFF, 0xFF, 0x16}
	colLine    = color.NRGBA{0x26, 0x2C, 0x36, 0xFF}
	colText    = color.NRGBA{0xE9, 0xED, 0xF3, 0xFF}
	colDim     = color.NRGBA{0x8B, 0x96, 0xA8, 0xFF}
	colAmber   = color.NRGBA{0xFF, 0xB4, 0x54, 0xFF}
	colGreen   = color.NRGBA{0x3B, 0xA5, 0x5D, 0xFF}
	colRed     = color.NRGBA{0xED, 0x42, 0x45, 0xFF}
	colYellow  = color.NRGBA{0xFA, 0xA8, 0x1A, 0xFF}
	colSpeak   = color.NRGBA{0x3B, 0xA5, 0x5D, 0xFF}
	colOffline = color.NRGBA{0x4A, 0x52, 0x60, 0xFF}
)

// darkTheme — тёмная тема со своими фонами: дефолтная фениевская светлее
// и «пластиковее», чем хочется для голосовалки.
type darkTheme struct{}

func (darkTheme) Color(n fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	switch n {
	case theme.ColorNameBackground:
		return colMain
	case theme.ColorNameInputBackground, theme.ColorNameInputBorder:
		return colInput
	case theme.ColorNameForeground:
		return colText
	case theme.ColorNamePlaceHolder, theme.ColorNameDisabled:
		return colDim
	case theme.ColorNamePrimary, theme.ColorNameFocus:
		return colAmber
	case theme.ColorNameHover:
		return colHover
	case theme.ColorNameSeparator:
		return colLine
	case theme.ColorNameError:
		return colRed
	case theme.ColorNameSuccess:
		return colGreen
	}
	return theme.DefaultTheme().Color(n, theme.VariantDark)
}
func (darkTheme) Font(s fyne.TextStyle) fyne.Resource     { return theme.DefaultTheme().Font(s) }
func (darkTheme) Icon(n fyne.ThemeIconName) fyne.Resource { return theme.DefaultTheme().Icon(n) }
func (darkTheme) Size(n fyne.ThemeSizeName) float32 {
	switch n {
	case theme.SizeNameInputBorder:
		return 0
	case theme.SizeNamePadding:
		return 4
	}
	return theme.DefaultTheme().Size(n)
}

func hexColor(hex string) color.Color {
	var r, g, b uint8
	if _, err := fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b); err != nil {
		return colDim
	}
	return color.NRGBA{R: r, G: g, B: b, A: 0xFF}
}

/* ---------- мелкие строительные блоки ---------- */

func txt(s string, c color.Color, size float32, bold bool) *canvas.Text {
	t := canvas.NewText(s, c)
	t.TextSize = size
	t.TextStyle = fyne.TextStyle{Bold: bold}
	return t
}

func mono(s string, c color.Color, size float32) *canvas.Text {
	t := canvas.NewText(s, c)
	t.TextSize = size
	t.TextStyle = fyne.TextStyle{Monospace: true}
	return t
}

// filled — прямоугольная подложка нужного цвета под содержимым.
func filled(c color.Color, obj fyne.CanvasObject) *fyne.Container {
	return container.NewStack(canvas.NewRectangle(c), obj)
}

// fixedWidth — колонка постоянной ширины (рельса групп, список каналов,
// список участников). Border-лейаут отдаёт боковинам их MinSize по ширине.
type fixedWidth struct{ w float32 }

func (f *fixedWidth) MinSize(objs []fyne.CanvasObject) fyne.Size {
	h := float32(0)
	for _, o := range objs {
		if m := o.MinSize().Height; m > h {
			h = m
		}
	}
	return fyne.NewSize(f.w, h)
}

func (f *fixedWidth) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objs {
		o.Resize(fyne.NewSize(f.w, size.Height))
		o.Move(fyne.NewPos(0, 0))
	}
}

func column(w float32, obj fyne.CanvasObject) *fyne.Container {
	return container.New(&fixedWidth{w: w}, obj)
}

/* ---------- кликабельная строка ---------- */

// tapRow — строка списка: подсветка под курсором, активное состояние,
// правый клик для контекстного меню. Штатные кнопки Fyne для такого
// выглядят как кнопки, а нужен именно список.
type tapRow struct {
	widget.BaseWidget
	content     fyne.CanvasObject
	onTap       func()
	onSecondary func(fyne.Position)
	active      bool
	hover       bool
	bg          *canvas.Rectangle
}

func newTapRow(content fyne.CanvasObject, onTap func()) *tapRow {
	r := &tapRow{content: content, onTap: onTap, bg: canvas.NewRectangle(color.Transparent)}
	r.bg.CornerRadius = 6
	r.ExtendBaseWidget(r)
	return r
}

func (r *tapRow) SetActive(a bool) {
	if r.active != a {
		r.active = a
		r.applyBG()
	}
}

func (r *tapRow) applyBG() {
	switch {
	case r.active:
		r.bg.FillColor = colActive
	case r.hover:
		r.bg.FillColor = colHover
	default:
		r.bg.FillColor = color.Transparent
	}
	r.bg.Refresh()
}

func (r *tapRow) CreateRenderer() fyne.WidgetRenderer {
	r.applyBG()
	return widget.NewSimpleRenderer(container.NewStack(r.bg, r.content))
}

func (r *tapRow) Tapped(*fyne.PointEvent) {
	if r.onTap != nil {
		r.onTap()
	}
}

func (r *tapRow) TappedSecondary(e *fyne.PointEvent) {
	if r.onSecondary != nil {
		r.onSecondary(e.AbsolutePosition)
	}
}

func (r *tapRow) MouseIn(*fynedesktop.MouseEvent)    { r.hover = true; r.applyBG() }
func (r *tapRow) MouseOut()                          { r.hover = false; r.applyBG() }
func (r *tapRow) MouseMoved(*fynedesktop.MouseEvent) {}
func (r *tapRow) Cursor() fynedesktop.Cursor         { return fynedesktop.PointerCursor }

/* ---------- аватарки ---------- */

// avatarCache держит уже скачанные картинки: аватарки повторяются в каждом
// сообщении, качать их заново на каждую строку — гарантированные тормоза.
type avatarCache struct {
	mu   sync.Mutex
	data map[string]fyne.Resource
	busy map[string]bool
	api  *API
}

func newAvatarCache(api *API) *avatarCache {
	return &avatarCache{data: map[string]fyne.Resource{}, busy: map[string]bool{}, api: api}
}

func (c *avatarCache) get(url string) (fyne.Resource, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.data[url]
	return r, ok
}

// fetch скачивает картинку и зовёт done в UI-потоке (через fyne.Do у вызывающего).
func (c *avatarCache) fetch(url string, done func(fyne.Resource)) {
	if url == "" {
		return
	}
	if r, ok := c.get(url); ok {
		done(r)
		return
	}
	c.mu.Lock()
	if c.busy[url] {
		c.mu.Unlock()
		return
	}
	c.busy[url] = true
	c.mu.Unlock()

	go func() {
		resp, err := c.api.hc.Get(url)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return
		}
		res := fyne.NewStaticResource(url, b)
		c.mu.Lock()
		c.data[url] = res
		delete(c.busy, url)
		c.mu.Unlock()
		fyne.Do(func() { done(res) })
	}()
}

// avatar — кружок с аватаркой юзера; пока картинка не приехала (или её нет,
// как у гостей) — цветной круг с первой буквой ника.
func (u *UI) avatar(user auth.User, size float32) fyne.CanvasObject {
	initial := "?"
	if r := []rune(strings.TrimSpace(user.Name)); len(r) > 0 {
		initial = strings.ToUpper(string(r[0]))
	}
	circle := canvas.NewCircle(hexColor(user.Color.Hex))
	letter := txt(initial, color.NRGBA{0x11, 0x14, 0x18, 0xFF}, size*0.5, true)
	letter.Alignment = fyne.TextAlignCenter

	stack := container.NewStack(circle, container.NewCenter(letter))
	holder := container.New(&fixedSize{w: size, h: size}, stack)

	if user.Avatar != "" {
		img := canvas.NewImageFromResource(nil)
		img.FillMode = canvas.ImageFillContain
		u.avatars.fetch(user.Avatar, func(res fyne.Resource) {
			img.Resource = res
			img.Refresh()
			stack.Objects = []fyne.CanvasObject{img}
			stack.Refresh()
		})
	}
	return holder
}

// fixedSize — жёсткий размер (аватарки, иконки, индикаторы).
type fixedSize struct{ w, h float32 }

func (f *fixedSize) MinSize([]fyne.CanvasObject) fyne.Size { return fyne.NewSize(f.w, f.h) }
func (f *fixedSize) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objs {
		o.Resize(fyne.NewSize(f.w, f.h))
		o.Move(fyne.NewPos((size.Width-f.w)/2, (size.Height-f.h)/2))
	}
}

func sized(w, h float32, obj fyne.CanvasObject) *fyne.Container {
	return container.New(&fixedSize{w: w, h: h}, obj)
}

/* ---------- индикатор связи ---------- */

// pingBars — три палочки качества связи плюс миллисекунды, как в Discord:
// сразу видно, это «у меня лагает» или «у него лагает».
type pingBars struct {
	bars  [3]*canvas.Rectangle
	label *canvas.Text
	box   *fyne.Container
}

func newPingBars() *pingBars {
	p := &pingBars{label: mono("—", colDim, 11)}
	heights := []float32{5, 8, 11}
	cells := make([]fyne.CanvasObject, 3)
	for i := range p.bars {
		p.bars[i] = canvas.NewRectangle(colOffline)
		p.bars[i].CornerRadius = 1
		cells[i] = container.NewVBox(layoutSpacer(11-heights[i]), sized(3, heights[i], p.bars[i]))
	}
	p.box = container.NewHBox(cells[0], cells[1], cells[2], p.label)
	return p
}

func layoutSpacer(h float32) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	return sized(3, h, r)
}

// set красит палочки: зелёный до 100 мс, жёлтый до 250, красный дальше;
// ms < 0 означает «связи нет».
func (p *pingBars) set(ms int) {
	on := colGreen
	lit := 3
	switch {
	case ms < 0:
		on, lit = colRed, 0
		p.label.Text = "нет связи"
	case ms < 100:
		on, lit = colGreen, 3
		p.label.Text = fmt.Sprintf("%d мс", ms)
	case ms < 250:
		on, lit = colYellow, 2
		p.label.Text = fmt.Sprintf("%d мс", ms)
	default:
		on, lit = colRed, 1
		p.label.Text = fmt.Sprintf("%d мс", ms)
	}
	for i, b := range p.bars {
		if i < lit {
			b.FillColor = on
		} else {
			b.FillColor = colOffline
		}
		b.Refresh()
	}
	p.label.Color = colDim
	p.label.Refresh()
}

/* ---------- точка статуса ---------- */

func statusDot(online bool, size float32) fyne.CanvasObject {
	c := canvas.NewCircle(colOffline)
	if online {
		c.FillColor = colGreen
	}
	return sized(size, size, c)
}
