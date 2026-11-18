package trayicon

import "testing"

func TestPad(t *testing.T) {
	out, err := Pad(solidIcon(t, 64), 0.9)
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, out)
	if b := img.Bounds(); b.Dx() != 71 || b.Dy() != 71 {
		t.Fatalf("padded size = %v, want 71x71", b.Size())
	}
	// 71-64 = 7: 3 transparent pixels before the glyph, 4 after.
	for _, tc := range []struct {
		x, y   int
		opaque bool
	}{
		{0, 0, false}, {2, 35, false}, {3, 3, true}, {66, 66, true}, {67, 35, false}, {70, 70, false},
	} {
		_, _, _, a := img.At(tc.x, tc.y).RGBA()
		if (a != 0) != tc.opaque {
			t.Errorf("pixel (%d, %d) alpha = %d, want opaque=%v", tc.x, tc.y, a, tc.opaque)
		}
	}
	if _, err := Pad(solidIcon(t, 64), 0); err == nil {
		t.Error("Pad with scale 0 succeeded")
	}
}
