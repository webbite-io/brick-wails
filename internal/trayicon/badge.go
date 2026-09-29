// Package trayicon draws the small status dots the system tray icon and its
// menu items carry: a blue one for a waiting update, a yellow one for paused
// sync. Badging the icon at runtime keeps one PNG per theme in the repo
// instead of one per theme per state.
package trayicon

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
)

// The palette the frontend uses for the same two states — the accent blue of
// its primary buttons (.btn-primary) and the yellow of its paused status dot
// (.dot.state-paused), both in frontend/public/style.css.
var (
	UpdateBlue   = color.NRGBA{R: 0x3b, G: 0x82, B: 0xf6, A: 0xff}
	PausedYellow = color.NRGBA{R: 0xea, G: 0xb3, B: 0x08, A: 0xff}
)

// Sizes as a fraction of the icon they're drawn on, so the badge scales with
// whatever resolution the tray icon is supplied at.
const (
	// badgeRadius is deliberately large: the tray draws the icon at 16–22px,
	// where a subtler dot disappears.
	badgeRadius = 0.2
	// badgeRing is the gap punched out of the icon around the dot, so the
	// badge stays legible over the glyph underneath.
	badgeRing = 0.055
	// dotFill leaves a little breathing room inside a standalone Dot.
	dotFill = 0.8
)

// Badge returns iconPNG with a dot of c in its upper-right corner.
func Badge(iconPNG []byte, c color.NRGBA) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(iconPNG))
	if err != nil {
		return nil, fmt.Errorf("decode icon: %w", err)
	}
	b := src.Bounds()
	img := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(img, img.Bounds(), src, b.Min, draw.Src)

	r, ring := badgeGeometry(min(b.Dx(), b.Dy()))
	// Inset by the ring width, so the punched-out gap stops exactly at the
	// icon's edge rather than being clipped by it.
	drawDot(img, float64(b.Dx())-r-ring, r+ring, r, ring, c)
	return encodePNG(img)
}

// Dot returns a standalone dot PNG of c, size×size: the same mark a badged
// tray icon carries, for a menu item standing for the same state
// (application.MenuItem.SetBitmap).
func Dot(size int, c color.NRGBA) ([]byte, error) {
	if size <= 0 {
		return nil, fmt.Errorf("dot size must be positive, got %d", size)
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	centre := float64(size) / 2
	drawDot(img, centre, centre, centre*dotFill, 0, c)
	return encodePNG(img)
}

// badgeGeometry returns the dot's radius and the width of the ring punched out
// around it, for an icon of the given size.
func badgeGeometry(size int) (r, ring float64) {
	return float64(size) * badgeRadius, float64(size) * badgeRing
}

// drawDot paints an anti-aliased disc of c centred on (cx, cy), after clearing
// a ring of that width around it. Clearing first is what lets the disc be
// written straight over the top: every pixel it touches has been made fully
// transparent, so there's nothing left to blend with.
func drawDot(img *image.NRGBA, cx, cy, r, ring float64, c color.NRGBA) {
	outer := r + ring
	area := image.Rect(
		int(math.Floor(cx-outer)), int(math.Floor(cy-outer)),
		int(math.Ceil(cx+outer))+1, int(math.Ceil(cy+outer))+1,
	).Intersect(img.Bounds())

	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			if ring > 0 {
				if cov := coverage(x, y, cx, cy, outer); cov > 0 {
					p := img.NRGBAAt(x, y)
					p.A = uint8(math.Round(float64(p.A) * (1 - cov)))
					img.SetNRGBA(x, y, p)
				}
			}
			if cov := coverage(x, y, cx, cy, r); cov > 0 {
				dot := c
				dot.A = uint8(math.Round(float64(c.A) * cov))
				img.SetNRGBA(x, y, dot)
			}
		}
	}
}

// coverage returns how much of the pixel at (x, y) falls inside the circle of
// radius r centred on (cx, cy) — 0 outside, 1 fully inside — sampled on a 4×4
// grid so the edge comes out anti-aliased rather than stair-stepped.
func coverage(x, y int, cx, cy, r float64) float64 {
	const n = 4
	inside := 0
	for sy := 0; sy < n; sy++ {
		for sx := 0; sx < n; sx++ {
			dx := float64(x) + (float64(sx)+0.5)/n - cx
			dy := float64(y) + (float64(sy)+0.5)/n - cy
			if dx*dx+dy*dy <= r*r {
				inside++
			}
		}
	}
	return float64(inside) / (n * n)
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode icon: %w", err)
	}
	return buf.Bytes(), nil
}
