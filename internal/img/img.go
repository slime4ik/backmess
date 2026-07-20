// Package img — сжатие картинок для чата: длинная сторона ужимается до 1600px,
// перекодирование в JPEG q82. Анимированные GIF не пережимаются (иначе потеряют анимацию).
package img

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	maxSide     = 1600
	jpegQuality = 82
)

var ErrUnsupported = errors.New("unsupported image format")

// Compress возвращает сжатые байты и расширение файла (".jpg" или ".gif").
func Compress(data []byte) ([]byte, string, error) {
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", ErrUnsupported
	}

	if format == "gif" {
		// многокадровый gif отдаём как есть, однокадровый пережимаем как обычную картинку
		g, err := gif.DecodeAll(bytes.NewReader(data))
		if err == nil && len(g.Image) > 1 {
			return data, ".gif", nil
		}
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("decode: %w", err)
	}

	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > maxSide || h > maxSide {
		if w >= h {
			h = h * maxSide / w
			w = maxSide
		} else {
			w = w * maxSide / h
			h = maxSide
		}
		if w < 1 {
			w = 1
		}
		if h < 1 {
			h = 1
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
		src = dst
	}

	var out bytes.Buffer
	if err := jpeg.Encode(&out, src, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, "", err
	}
	// если сжатие не помогло (уже мелкий jpeg) — оставляем оригинал
	if format == "jpeg" && out.Len() >= len(data) {
		return data, ".jpg", nil
	}
	return out.Bytes(), ".jpg", nil
}
