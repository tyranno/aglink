//go:build windows

package main

import (
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Agent cursor: a small ring shown for ~600ms where a background operation
// acted, so the user sees what the agent touched while their own mouse stays
// put. Same overlay recipe as the notice toast (layered, topmost, no-activate,
// click-through, own OS thread) but its own window class and per-window state,
// so it never interferes with the toast. AGLINK_SCREEN_AGENT_CURSOR=0 turns it
// off.

const (
	agentCursorSize    = 30
	agentCursorTotalMS = 600
	agentCursorFadeMS  = 250
	agentCursorAlpha   = 220
	agentCursorTimerID = 1

	agentCursorColor = 0x00D6487A // #7A48D6 violet ring (COLORREF 0x00BBGGRR)
	agentCursorInner = 0x00FFFFFF
)

var (
	procCreateEllipticRgnN = modGdi32N.NewProc("CreateEllipticRgn")

	agentCursorClassOnce sync.Once
	agentCursorClassOK   bool
	agentCursorClassName = windows.StringToUTF16Ptr("AglinkAgentCursor")
	agentCursorBrush     uintptr
	agentCursorInnerBr   uintptr

	agentCursorMu    sync.Mutex
	agentCursorStart = map[uintptr]time.Time{}
)

// agentCursorEnabled reads AGLINK_SCREEN_AGENT_CURSOR (default on).
func agentCursorEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AGLINK_SCREEN_AGENT_CURSOR"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// agentCursorAlphaAt is the marker's alpha elapsedMS into its showing: full
// at once, then a fade over the last agentCursorFadeMS.
func agentCursorAlphaAt(elapsedMS int) byte {
	switch {
	case elapsedMS < 0 || elapsedMS >= agentCursorTotalMS:
		return 0
	case elapsedMS < agentCursorTotalMS-agentCursorFadeMS:
		return agentCursorAlpha
	default:
		return byte(agentCursorAlpha * (agentCursorTotalMS - elapsedMS) / agentCursorFadeMS)
	}
}

// agentCursorShow runs one marker window to completion. A var so tests never
// create real windows.
var agentCursorShow = runAgentCursorWindow

// showAgentCursor marks screen point (x,y) without blocking the caller.
func showAgentCursor(x, y int) {
	if !agentCursorEnabled() {
		return
	}
	go agentCursorShow(x, y)
}

func registerAgentCursorClass() bool {
	agentCursorClassOnce.Do(func() {
		hInst, _, _ := procGetModuleHandleN.Call(0)
		agentCursorBrush, _, _ = procCreateSolidBrushN.Call(uintptr(agentCursorColor))
		agentCursorInnerBr, _, _ = procCreateSolidBrushN.Call(uintptr(agentCursorInner))
		wc := wndClassExW{
			cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
			lpfnWndProc:   syscall.NewCallback(agentCursorWndProc),
			hInstance:     hInst,
			hbrBackground: agentCursorBrush,
			lpszClassName: agentCursorClassName,
		}
		atom, _, _ := procRegisterClassExN.Call(uintptr(unsafe.Pointer(&wc)))
		agentCursorClassOK = atom != 0
	})
	return agentCursorClassOK
}

func agentCursorWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch uint32(msg) {
	case wmPaint:
		var ps paintStruct
		hdc, _, _ := procBeginPaintN.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		if hdc != 0 {
			// Violet disc (class background) with a white centre dot.
			old, _, _ := procSelectObjectN.Call(hdc, agentCursorInnerBr)
			const c, r = agentCursorSize / 2, 4
			procEllipseN.Call(hdc, c-r, c-r, c+r, c+r)
			procSelectObjectN.Call(hdc, old)
			procEndPaintN.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		}
		return 0
	case wmTimer:
		agentCursorMu.Lock()
		start, ok := agentCursorStart[hwnd]
		agentCursorMu.Unlock()
		elapsed := int(time.Since(start) / time.Millisecond)
		if !ok || elapsed >= agentCursorTotalMS {
			procKillTimerN.Call(hwnd, wParam)
			procDestroyWindowN.Call(hwnd)
			return 0
		}
		procSetLayeredWinAttrN.Call(hwnd, 0, uintptr(agentCursorAlphaAt(elapsed)), lwaAlpha)
		return 0
	case wmDestroy:
		agentCursorMu.Lock()
		delete(agentCursorStart, hwnd)
		agentCursorMu.Unlock()
		procPostQuitMessageN.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcN.Call(hwnd, msg, wParam, lParam)
	return r
}

// runAgentCursorWindow shows a marker centred on (x,y) on its own OS thread
// and pumps it until its timer destroys it.
func runAgentCursorWindow(x, y int) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ensureDPIAware()
	if !registerAgentCursorClass() {
		return
	}
	hInst, _, _ := procGetModuleHandleN.Call(0)
	left, top := x-agentCursorSize/2, y-agentCursorSize/2
	hwnd, _, _ := procCreateWindowExN.Call(
		uintptr(wsExTopmost|wsExLayered|wsExToolWindow|wsExNoActivate|wsExTransparent),
		uintptr(unsafe.Pointer(agentCursorClassName)),
		0,
		uintptr(wsPopup),
		uintptr(int32(left)), uintptr(int32(top)), agentCursorSize, agentCursorSize,
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return
	}
	if rgn, _, _ := procCreateEllipticRgnN.Call(0, 0, agentCursorSize+1, agentCursorSize+1); rgn != 0 {
		procSetWindowRgnN.Call(hwnd, rgn, 1)
	}
	procSetLayeredWinAttrN.Call(hwnd, 0, agentCursorAlpha, lwaAlpha)
	agentCursorMu.Lock()
	agentCursorStart[hwnd] = time.Now()
	agentCursorMu.Unlock()
	procShowWindowN.Call(hwnd, swShowNoActivate)
	procSetTimerN.Call(hwnd, agentCursorTimerID, 15, 0)

	var msg msgStruct
	for {
		r, _, _ := procGetMessageN.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessageN.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageN.Call(uintptr(unsafe.Pointer(&msg)))
	}
}
