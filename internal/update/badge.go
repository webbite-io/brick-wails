package update

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
)

// badgeColour is the accent blue the frontend uses for its primary buttons
// (.btn-primary, frontend/public/startup.css), so the "an update is waiting"
// dot reads as the same blue wherever it shows up.
var badgeColour = color.NRGBA{R: 0x3b, G: 0x82, B: 0xf6, A: 0xff}

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

// Badge returns iconPNG with a blue dot in its upper-right corner: the tray
// icon's "an update is waiting" state.
func Badge(iconPNG []byte) ([]byte, error) {
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
	drawDot(img, float64(b.Dx())-r-ring, r+ring, r, ring)
	return encodePNG(img)
}

// badgeGeometry returns the dot's radius and the width of the ring punched out
// around it, for an icon of the given size.
func badgeGeometry(size int) (r, ring float64) {
	return float64(size) * badgeRadius, float64(size) * badgeRing
}

// Dot returns a standalone blue dot PNG, size×size: the same mark the badged
// tray icon carries, for the menu item offering the update
// (application.MenuItem.SetBitmap).
func Dot(size int) ([]byte, error) {
	if size <= 0 {
		return nil, fmt.Errorf("dot size must be positive, got %d", size)
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	centre := float64(size) / 2
	drawDot(img, centre, centre, centre*dotFill, 0)
	return encodePNG(img)
}

// drawDot paints an anti-aliased disc of badgeColour centred on (cx, cy),
// after clearing a ring of that width around it. Clearing first is what lets
// the disc be written straight over the top: every pixel it touches has been
// made fully transparent, so there's nothing left to blend with.
func drawDot(img *image.NRGBA, cx, cy, r, ring float64) {
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
				c := badgeColour
				c.A = uint8(math.Round(float64(c.A) * cov))
				img.SetNRGBA(x, y, c)
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
