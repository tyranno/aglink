//go:build windows

package main

import (
	"fmt"
	"sort"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Monitor enumeration for screenshot's `monitor` argument, and the caption
// math every capture tool shares: where the image sits on the screen and how
// far it was scaled, so an image pixel can be mapped back to a click point.

var (
	procEnumDisplayMonitors = modUser32Cap.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW     = modUser32Cap.NewProc("GetMonitorInfoW")
)

const monitorInfoFPrimary = 1

// monitorInfoW mirrors the Win32 MONITORINFO struct.
type monitorInfoW struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

// monitor is one display in virtual-screen pixels.
type monitor struct {
	Rect    rect
	Primary bool
}

var (
	monitorMu    sync.Mutex
	monitorFound []monitor
	// monitorEnumProc is created once: Windows callbacks are a limited resource
	// (windows.NewCallback never frees them).
	monitorEnumProc = windows.NewCallback(func(hmon, hdc, lprc, lparam uintptr) uintptr {
		mi := monitorInfoW{Size: uint32(unsafe.Sizeof(monitorInfoW{}))}
		if r, _, _ := procGetMonitorInfoW.Call(hmon, uintptr(unsafe.Pointer(&mi))); r != 0 {
			monitorFound = append(monitorFound, monitor{Rect: mi.Monitor, Primary: mi.Flags&monitorInfoFPrimary != 0})
		}
		return 1 // continue
	})
)

// listMonitors returns the displays in screenshot's numbering (orderMonitors).
func listMonitors() ([]monitor, error) {
	ensureDPIAware()
	monitorMu.Lock()
	defer monitorMu.Unlock()
	monitorFound = nil
	if r, _, e := procEnumDisplayMonitors.Call(0, 0, monitorEnumProc, 0); r == 0 {
		return nil, fmt.Errorf("EnumDisplayMonitors: %v", e)
	}
	ms := append([]monitor(nil), monitorFound...)
	orderMonitors(ms)
	return ms, nil
}

// orderMonitors sorts displays into the user-facing 1-based numbering: the
// primary first, then left to right, top to bottom — stable across calls,
// unlike EnumDisplayMonitors' own order.
func orderMonitors(ms []monitor) {
	sort.SliceStable(ms, func(i, j int) bool {
		if ms[i].Primary != ms[j].Primary {
			return ms[i].Primary
		}
		if ms[i].Rect.Left != ms[j].Rect.Left {
			return ms[i].Rect.Left < ms[j].Rect.Left
		}
		return ms[i].Rect.Top < ms[j].Rect.Top
	})
}

// describeMonitors lists displays for an out-of-range monitor error.
func describeMonitors(ms []monitor) string {
	s := ""
	for i, m := range ms {
		if i > 0 {
			s += ", "
		}
		p := ""
		if m.Primary {
			p = " primary"
		}
		s += fmt.Sprintf("%d=(%d,%d %dx%d%s)", i+1, m.Rect.Left, m.Rect.Top, m.Rect.Right-m.Rect.Left, m.Rect.Bottom-m.Rect.Top, p)
	}
	return s
}

// captureMapping is where a captured image came from on the screen: a W×H
// screen rectangle at (OriginX,OriginY), delivered as an ImgW×ImgH image.
type captureMapping struct {
	OriginX, OriginY int
	W, H             int
	ImgW, ImgH       int
}

// scale is image pixels per screen pixel (1 = untouched, 0.5 = half size).
func (m captureMapping) scale() float64 {
	if m.W <= 0 || m.ImgW <= 0 {
		return 1
	}
	return float64(m.ImgW) / float64(m.W)
}

// exact reports a 1:1 image→screen mapping.
func (m captureMapping) exact() bool {
	return m.ImgW == m.W && m.ImgH == m.H
}

// toScreen maps image pixel (ix,iy) to screen coordinates:
// screen = origin + img/scale, per axis (rounded to the nearest pixel).
func (m captureMapping) toScreen(ix, iy int) (int, int) {
	sx, sy := 1.0, 1.0
	if m.W > 0 && m.ImgW > 0 {
		sx = float64(m.ImgW) / float64(m.W)
	}
	if m.H > 0 && m.ImgH > 0 {
		sy = float64(m.ImgH) / float64(m.H)
	}
	return m.OriginX + int(float64(ix)/sx+0.5), m.OriginY + int(float64(iy)/sy+0.5)
}

// caption is the coordinate part of a capture caption.
func (m captureMapping) caption() string {
	if m.exact() {
		return fmt.Sprintf("origin=(%d,%d) size=%dx%d scale=1 — image pixel (ix,iy) is screen (%d+ix, %d+iy), so click(x=%d+ix, y=%d+iy).",
			m.OriginX, m.OriginY, m.W, m.H, m.OriginX, m.OriginY, m.OriginX, m.OriginY)
	}
	s := m.scale()
	return fmt.Sprintf("origin=(%d,%d) size=%dx%d image=%dx%d scale=%.4g — screen_x = %d + img_x/%.4g, screen_y = %d + img_y/%.4g "+
		"(±%d px; for small targets prefer snapshot/element_at rectangles, or capture_region a smaller area at scale 1).",
		m.OriginX, m.OriginY, m.W, m.H, m.ImgW, m.ImgH, s, m.OriginX, s, m.OriginY, s, roundErrPx(s))
}

// roundErrPx is the worst-case screen-pixel error of reading a coordinate off
// an image at scale s (one image pixel covers 1/s screen pixels).
func roundErrPx(s float64) int {
	if s <= 0 || s >= 1 {
		return 0
	}
	return int(1/s + 0.999)
}
