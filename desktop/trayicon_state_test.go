package main

import (
	"bytes"
	"image/png"
	"testing"
)

func TestTrayIconFor(t *testing.T) {
	if got := trayIconFor(""); !bytes.Equal(got, trayIcon) {
		t.Fatal("inactive should be the plain icon")
	}
	for phase, col := range trayBadgeColours {
		img, err := png.Decode(bytes.NewReader(trayIconFor(phase)))
		if err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
		b := img.Bounds()
		r, g, bl, _ := img.At(b.Max.X-b.Dx()/4, b.Max.Y-b.Dy()/4).RGBA()
		if uint8(r>>8) != col.R || uint8(g>>8) != col.G || uint8(bl>>8) != col.B {
			t.Errorf("%s: badge centre is %d,%d,%d", phase, r>>8, g>>8, bl>>8)
		}
	}
}
