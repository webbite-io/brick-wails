package trayicon

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
)

// Pad returns iconPNG centred on a larger transparent canvas, so a tray that
// stretches its icon to fill a fixed height (macOS's menu bar) draws the
// glyph at scale of the size it would otherwise. The pixels are copied
// untouched rather than resampled; the tray does the only scaling.
func Pad(iconPNG []byte, scale float64) ([]byte, error) {
	if scale <= 0 || scale > 1 {
		return nil, fmt.Errorf("pad scale must be in (0, 1], got %v", scale)
	}
	src, err := png.Decode(bytes.NewReader(iconPNG))
	if err != nil {
		return nil, fmt.Errorf("decode icon: %w", err)
	}
	b := src.Bounds()
	w := int(math.Round(float64(b.Dx()) / scale))
	h := int(math.Round(float64(b.Dy()) / scale))
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	at := image.Pt((w-b.Dx())/2, (h-b.Dy())/2)
	draw.Draw(img, b.Sub(b.Min).Add(at), src, b.Min, draw.Src)
	return encodePNG(img)
}
