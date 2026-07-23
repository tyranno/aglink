//go:build windows

package main

import (
	"image"
	"testing"
)

// resolveScreenshotLongEdge honors a sane AGLINK_SCREENSHOT_MAX_EDGE override and
// falls back to the default when unset or out of range.
func TestResolveScreenshotLongEdge(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int
	}{
		{"unset → default", "", defaultScreenshotLongEdge},
		{"valid override", "1024", 1024},
		{"non-numeric → default", "abc", defaultScreenshotLongEdge},
		{"too small → default", "100", defaultScreenshotLongEdge},
		{"too large → default", "99999", defaultScreenshotLongEdge},
		{"lower bound ok", "320", 320},
		{"upper bound ok", "4096", 4096},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGLINK_SCREENSHOT_MAX_EDGE", tc.env) // "" reads back as unset
			if got := resolveScreenshotLongEdge(); got != tc.want {
				t.Errorf("env=%q: got %d, want %d", tc.env, got, tc.want)
			}
		})
	}
}

// TestCappedDims covers the screenshot cap policy: shrink only when the longer
// edge exceeds the budget, preserve aspect ratio, and never upscale.
func TestCappedDims(t *testing.T) {
	cases := []struct {
		name           string
		w, h, maxLong  int
		wantDW, wantDH int
		wantScale      bool
	}{
		{"1080p capped to 1280", 1920, 1080, 1280, 1280, 720, true},
		{"4k capped to 1280", 3840, 2160, 1280, 1280, 720, true},
		{"portrait caps on height", 1080, 1920, 1280, 720, 1280, true},
		{"already small — no scale", 1000, 700, 1280, 1000, 700, false},
		{"exactly at cap — no scale", 1280, 800, 1280, 1280, 800, false},
		{"maxLong<=0 disables", 3840, 2160, 0, 3840, 2160, false},
		{"degenerate size", 0, 0, 1280, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dw, dh, scaled := cappedDims(c.w, c.h, c.maxLong)
			if scaled != c.wantScale || dw != c.wantDW || dh != c.wantDH {
				t.Errorf("cappedDims(%d,%d,%d) = (%d,%d,%v), want (%d,%d,%v)",
					c.w, c.h, c.maxLong, dw, dh, scaled, c.wantDW, c.wantDH, c.wantScale)
			}
			// A capped result must never exceed the budget on either edge.
			if scaled && (dw > c.maxLong || dh > c.maxLong) {
				t.Errorf("capped dims %dx%d exceed maxLong=%d", dw, dh, c.maxLong)
			}
		})
	}
}

// TestDownscaleNearest verifies the shared scaler produces the requested target
// size and samples source pixels (a 2x2 solid-quadrant image halved keeps one
// pixel per quadrant color).
func TestDownscaleNearest(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	// Fill four 2x2 quadrants with distinct grays so sampling is checkable.
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			v := uint8(0)
			switch {
			case x < 2 && y < 2:
				v = 10
			case x >= 2 && y < 2:
				v = 20
			case x < 2 && y >= 2:
				v = 30
			default:
				v = 40
			}
			src.Pix[src.PixOffset(x, y)+0] = v
			src.Pix[src.PixOffset(x, y)+1] = v
			src.Pix[src.PixOffset(x, y)+2] = v
			src.Pix[src.PixOffset(x, y)+3] = 0xFF
		}
	}
	dst := downscaleNearest(src, 2, 2)
	if dst.Bounds().Dx() != 2 || dst.Bounds().Dy() != 2 {
		t.Fatalf("downscaleNearest size = %v, want 2x2", dst.Bounds())
	}
	// Each destination pixel should carry one of the four source quadrant values.
	got := map[uint8]bool{}
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			got[dst.Pix[dst.PixOffset(x, y)]] = true
		}
	}
	for _, want := range []uint8{10, 20, 30, 40} {
		if !got[want] {
			t.Errorf("downscaled image missing quadrant value %d (sampled: %v)", want, got)
		}
	}
}
