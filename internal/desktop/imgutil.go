package desktop

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"math"

	_ "image/gif"
	_ "image/jpeg"

	xdraw "golang.org/x/image/draw"
)

// Обработка картинок на клиенте решает две задачи сразу: круглые аватарки
// (Fyne не умеет обрезать по маске) и экономию памяти — вместо оригинала на
// 1600px в кэше лежит уменьшенная копия ровно того размера, в котором её видно.

// circleAvatar вписывает картинку в круг заданного размера.
func circleAvatar(raw []byte, size int) []byte {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))

	// заполняем квадрат, обрезая по короткой стороне (как object-fit: cover)
	b := src.Bounds()
	side := min(b.Dx(), b.Dy())
	crop := image.Rect(
		b.Min.X+(b.Dx()-side)/2,
		b.Min.Y+(b.Dy()-side)/2,
		b.Min.X+(b.Dx()-side)/2+side,
		b.Min.Y+(b.Dy()-side)/2+side,
	)
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, xdraw.Over, nil)

	// маска: за пределами круга — прозрачность, по краю сглаживаем
	r := float64(size) / 2
	for y := range size {
		for x := range size {
			d := math.Hypot(float64(x)+0.5-r, float64(y)+0.5-r)
			var a float64
			switch {
			case d <= r-1:
				a = 1
			case d >= r:
				a = 0
			default:
				a = r - d
			}
			if a >= 1 {
				continue
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i+3] = uint8(float64(dst.Pix[i+3]) * a)
		}
	}
	return encodePNG(dst)
}

// thumbnail уменьшает картинку так, чтобы длинная сторона была не больше max.
// Картинки в чате показываются превью ~280px — держать в памяти оригинал
// незачем.
func thumbnail(raw []byte, max int) []byte {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	b := src.Bounds()
	if b.Dx() <= max && b.Dy() <= max {
		return nil // уже мелкая, оставляем как есть
	}
	scale := float64(max) / float64(maxInt(b.Dx(), b.Dy()))
	w := int(float64(b.Dx()) * scale)
	h := int(float64(b.Dy()) * scale)
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Src, nil)
	return encodePNG(dst)
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var _ = draw.Draw // image/draw нужен транзитивно для декодеров
