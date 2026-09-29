package trayicon

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// solidIcon encodes a size×size opaque white PNG, standing in for the tray
// icon so the tests can tell "the badge changed this pixel" from "it didn't".
func solidIcon(t *testing.T, size int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode test icon: %v", err)
	}
	return buf.Bytes()
}

func decode(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return img
}

func TestBadgeKeepsSizeAndMarksTheCornerInTheGivenColour(t *testing.T) {
	const size = 64
	for name, want := range map[string]color.NRGBA{
		"update": UpdateBlue,
		"paused": PausedYellow,
	} {
		t.Run(name, func(t *testing.T) {
			badged := decode(t, mustBadge(t, solidIcon(t, size), want))

			if got := badged.Bounds(); got.Dx() != size || got.Dy() != size {
				t.Fatalf("badged icon is %v, want %dx%d", got, size, size)
			}

			// The dot's centre: right of centre, above it (see Badge's geometry).
			r, ring := badgeGeometry(size)
			offset := int(r + ring)
			pr, pg, pb, a := badged.At(size-offset, offset).RGBA()
			if a != 0xffff {
				t.Fatalf("dot centre is not opaque: alpha %d", a)
			}
			wantR, wantG, wantB, _ := want.RGBA()
			if pr != wantR || pg != wantG || pb != wantB {
				t.Fatalf("dot centre is %d,%d,%d, want %d,%d,%d", pr, pg, pb, wantR, wantG, wantB)
			}

			// The opposite corner is untouched, so the badge hasn't eaten the glyph.
			if _, _, _, a := badged.At(1, size-2).RGBA(); a != 0xffff {
				t.Fatalf("bottom-left corner lost its alpha (%d); the badge should only touch the top right", a)
			}
		})
	}
}

func TestBadgeClearsARingAroundTheDot(t *testing.T) {
	const size = 64
	badged := decode(t, mustBadge(t, solidIcon(t, size), UpdateBlue))

	// Just outside the dot but inside the punched-out ring: the icon's own
	// pixels there are cleared, which is what keeps the dot legible over the
	// glyph underneath.
	r, ring := badgeGeometry(size)
	cx, cy := float64(size)-r-ring, r+ring
	x, y := int(cx), int(cy-r-ring/2)
	if _, _, _, a := badged.At(x, y).RGBA(); a != 0 {
		t.Fatalf("pixel (%d,%d) in the ring has alpha %d, want it cleared to 0", x, y, a)
	}
}

func TestBadgeRejectsNonPNG(t *testing.T) {
	if _, err := Badge([]byte("not a png"), UpdateBlue); err == nil {
		t.Fatal("expected an error for input that isn't a PNG")
	}
}

func TestDot(t *testing.T) {
	const size = 16
	img := decode(t, mustDot(t, size, UpdateBlue))

	if got := img.Bounds(); got.Dx() != size || got.Dy() != size {
		t.Fatalf("dot is %v, want %dx%d", got, size, size)
	}
	if _, _, _, a := img.At(size/2, size/2).RGBA(); a != 0xffff {
		t.Fatalf("dot centre is not opaque: alpha %d", a)
	}
	// The corners fall outside the disc, so a dot beside a menu label doesn't
	// show up as a blue square.
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Fatalf("dot corner has alpha %d, want it transparent", a)
	}
}

func TestDotRejectsNonPositiveSize(t *testing.T) {
	if _, err := Dot(0, UpdateBlue); err == nil {
		t.Fatal("expected an error for size 0")
	}
}

func mustBadge(t *testing.T, icon []byte, c color.NRGBA) []byte {
	t.Helper()
	badged, err := Badge(icon, c)
	if err != nil {
		t.Fatalf("Badge: %v", err)
	}
	return badged
}

func mustDot(t *testing.T, size int, c color.NRGBA) []byte {
	t.Helper()
	dot, err := Dot(size, c)
	if err != nil {
		t.Fatalf("Dot: %v", err)
	}
	return dot
}
