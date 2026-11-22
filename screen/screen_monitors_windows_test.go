//go:build windows

package main

import (
	"strings"
	"testing"
	"time"
	"unsafe"
)

func TestCaptureMappingExact(t *testing.T) {
	m := captureMapping{OriginX: -1920, OriginY: 0, W: 800, H: 600, ImgW: 800, ImgH: 600}
	if !m.exact() || m.scale() != 1 {
		t.Fatalf("exact=%v scale=%v", m.exact(), m.scale())
	}
	if x, y := m.toScreen(10, 20); x != -1910 || y != 20 {
		t.Errorf("toScreen = %d,%d", x, y)
	}
	c := m.caption()
	for _, want := range []string{"origin=(-1920,0)", "size=800x600", "scale=1", "click(x=-1920+ix, y=0+iy)"} {
		if !strings.Contains(c, want) {
			t.Errorf("caption %q lacks %q", c, want)
		}
	}
}

func TestCaptureMappingScaled(t *testing.T) {
	// A 3840x2160 screen capped to 1280 wide: scale 1/3.
	m := captureMapping{OriginX: 0, OriginY: 0, W: 3840, H: 2160, ImgW: 1280, ImgH: 720}
	if m.exact() {
		t.Fatal("scaled mapping reported exact")
	}
	if x, y := m.toScreen(640, 360); x != 1920 || y != 1080 {
		t.Errorf("center maps to %d,%d want 1920,1080", x, y)
	}
	c := m.caption()
	for _, want := range []string{"origin=(0,0)", "size=3840x2160", "image=1280x720", "scale=0.3333", "screen_x = 0 + img_x/0.3333", "±3 px"} {
		if !strings.Contains(c, want) {
			t.Errorf("caption %q lacks %q", c, want)
		}
	}
	// Offset origin and a mild scale (window capped to the vision edge).
	m = captureMapping{OriginX: 100, OriginY: 50, W: 2000, H: 1000, ImgW: 1568, ImgH: 784}
	if x, y := m.toScreen(784, 392); x != 1100 || y != 550 {
		t.Errorf("toScreen = %d,%d want 1100,550", x, y)
	}
	if roundErrPx(1) != 0 || roundErrPx(0.5) != 2 || roundErrPx(0.784) != 2 {
		t.Error("roundErrPx")
	}
}

func TestOrderMonitors(t *testing.T) {
	ms := []monitor{
		{Rect: rect{Left: 1920, Top: 0, Right: 3840, Bottom: 1080}},
		{Rect: rect{Left: -1280, Top: 100, Right: 0, Bottom: 1124}},
		{Rect: rect{Left: 0, Top: 0, Right: 1920, Bottom: 1080}, Primary: true},
		{Rect: rect{Left: 1920, Top: -1080, Right: 3840, Bottom: 0}},
	}
	orderMonitors(ms)
	if !ms[0].Primary || ms[1].Rect.Left != -1280 || ms[2].Rect.Top != -1080 || ms[3].Rect.Top != 0 {
		t.Errorf("order = %+v", ms)
	}
	d := describeMonitors(ms[:2])
	if d != "1=(0,0 1920x1080 primary), 2=(-1280,100 1280x1024)" {
		t.Errorf("describeMonitors = %q", d)
	}
}

func TestUIPIExemptCombo(t *testing.T) {
	exempt := []string{"win+d", "Win+Shift+S", "volumeup", "mute", "ctrl+alt+del", "printscreen", "alt+prtsc", "playpause"}
	for _, c := range exempt {
		if !uipiExemptCombo(c) {
			t.Errorf("%q should be exempt", c)
		}
	}
	for _, c := range []string{"enter", "ctrl+c", "alt+f4", "ctrl+shift+s", "a"} {
		if uipiExemptCombo(c) {
			t.Errorf("%q should not be exempt", c)
		}
	}
}

func TestDragParams(t *testing.T) {
	if h, s := dragParams(0, 0); h != dragHoldDefault || s != dragStepsDefault {
		t.Errorf("defaults: %v %d", h, s)
	}
	if h, s := dragParams(500, 40); h != 500*time.Millisecond || s != 40 {
		t.Errorf("explicit: %v %d", h, s)
	}
	if h, s := dragParams(999999, 99999); h != dragHoldMax || s != dragStepsMax {
		t.Errorf("clamped: %v %d", h, s)
	}
}

// utf16TestBuf is package-level so its address stays valid as a uintptr
// (a local could live on a stack that moves).
var utf16TestBuf = []uint16{'h', 'i', 0, 'x'}

func uintptrOf(p *uint16) uintptr { return uintptr(unsafe.Pointer(p)) }

func TestUTF16Bounded(t *testing.T) {
	buf := utf16TestBuf
	if got := utf16Bounded(uintptrOf(&buf[0]), len(buf)); got != "hi" {
		t.Errorf("NUL-terminated = %q", got)
	}
	// No NUL within the block: stop at its end instead of reading past it.
	if got := utf16Bounded(uintptrOf(&buf[0]), 2); got != "hi" {
		t.Errorf("bounded = %q", got)
	}
	if utf16Bounded(0, 4) != "" {
		t.Error("nil pointer")
	}
}
