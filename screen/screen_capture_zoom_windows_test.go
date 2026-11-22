package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// TestZoomDims guards capture_region's zoom policy: magnify by the requested
// factor, clamp it to maxCaptureZoom, and lower it so the image stays within
// the vision long-edge cap.
func TestZoomDims(t *testing.T) {
	cases := []struct {
		w, h    int
		zoom    float64
		maxLong int
		dw, dh  int
		eff     float64
	}{
		{100, 40, 3, 1568, 300, 120, 3},
		{100, 40, 9, 1568, 400, 160, maxCaptureZoom}, // clamped to the max factor
		{800, 200, 4, 1600, 1600, 400, 2},            // lowered to fit the cap
		{2000, 100, 2, 1568, 2000, 100, 1},           // already over the cap: no zoom
		{100, 40, 1, 1568, 100, 40, 1},
		{0, 40, 2, 1568, 0, 40, 1},
	}
	for _, c := range cases {
		dw, dh, eff := zoomDims(c.w, c.h, c.zoom, c.maxLong)
		if dw != c.dw || dh != c.dh || eff != c.eff {
			t.Errorf("zoomDims(%d,%d,%g,%d) = (%d,%d,%g), want (%d,%d,%g)", c.w, c.h, c.zoom, c.maxLong, dw, dh, eff, c.dw, c.dh, c.eff)
		}
	}
}

// TestZoomPNGNearest checks the magnified image keeps hard pixel edges: each
// source pixel becomes a zoom×zoom block of the same color.
func TestZoomPNGNearest(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.Set(0, 0, color.RGBA{255, 0, 0, 255})
	src.Set(1, 0, color.RGBA{0, 0, 255, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	out, w, h, eff, err := zoomPNG(buf.Bytes(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if w != 6 || h != 3 || eff != 3 {
		t.Fatalf("zoomPNG size = %dx%d eff=%g, want 6x3 eff=3", w, h, eff)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if r, _, _, _ := img.At(2, 2).RGBA(); r>>8 != 255 {
		t.Errorf("pixel (2,2) should still be red")
	}
	if _, _, b, _ := img.At(3, 0).RGBA(); b>>8 != 255 {
		t.Errorf("pixel (3,0) should be blue")
	}
}
