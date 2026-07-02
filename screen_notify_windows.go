//go:build windows

package main

import (
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Screen-control heads-up notice.
//
// When the agent starts driving the mouse/keyboard while the user may be typing
// or clicking themselves, their real input and our synthetic input can collide.
// So, at the START of a control session, we flash a small, non-blocking, top-most
// overlay in a screen corner ("aglink-screen is controlling the screen") that
// auto-dismisses after a couple of seconds. It is throttled: once a session is
// under way (inputs arriving within controlNoticeGap of each other) no further
// notice is shown; a fresh notice only appears after an idle gap.
//
// Implementation: a self-owned WS_EX_LAYERED|TOPMOST|NOACTIVATE|TRANSPARENT popup
// (user32/gdi32 only — no WinRT, no AppUserModelID registration, no external
// deps). NOACTIVATE + SW_SHOWNOACTIVATE mean it never steals foreground from the
// window we are about to control, and TRANSPARENT lets any real user click pass
// through it. It runs on its own OS-locked goroutine with its own message pump,
// so ensureControlNotice() returns immediately and never delays the input itself.
//
// Set AGLINK_NO_CONTROL_NOTICE=1 to disable (e.g. headless / no user present).

const (
	// controlNoticeGap is the idle time after which a new control session is
	// considered to have started and the notice is shown again.
	controlNoticeGap = 8 * time.Second
	// controlNoticeMS is how long the overlay stays on screen.
	controlNoticeMS = 2200
)

var controlNoticeText = "aglink-screen: 자동 화면 제어 중 — 마우스·키보드를 잠시 멈춰주세요"

var (
	lastSyntheticInput atomic.Int64 // UnixNano of the last synthetic input
	noticeShowing      atomic.Bool  // guards against stacking overlays
	controlNoticeOff   = os.Getenv("AGLINK_NO_CONTROL_NOTICE") != ""
)

// ensureControlNotice is called at the entry of every function that synthesizes
// input. It records the input time and, if this is the first input of a new
// control session (>= controlNoticeGap since the previous one), flashes the
// overlay. It never blocks: the overlay runs on its own goroutine.
func ensureControlNotice() {
	now := time.Now().UnixNano()
	prev := lastSyntheticInput.Swap(now)
	if controlNoticeOff {
		return
	}
	if noticeDue(prev, now) {
		showControlNotice()
	}
}

// noticeDue reports whether a control-start notice should be shown: on the very
// first input (prev == 0) or after at least controlNoticeGap of idle since the
// previous synthetic input.
func noticeDue(prevNano, nowNano int64) bool {
	return prevNano == 0 || nowNano-prevNano >= int64(controlNoticeGap)
}

// showControlNotice flashes the overlay on a dedicated goroutine (fire-and-forget).
func showControlNotice() {
	if !noticeShowing.CompareAndSwap(false, true) {
		return // one already on screen
	}
	go func() {
		defer noticeShowing.Store(false)
		runNoticeWindow()
	}()
}

// ---- Win32 plumbing ----

var (
	modUser32N   = windows.NewLazySystemDLL("user32.dll")
	modGdi32N    = windows.NewLazySystemDLL("gdi32.dll")
	modKernel32N = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExN      = modUser32N.NewProc("RegisterClassExW")
	procCreateWindowExN       = modUser32N.NewProc("CreateWindowExW")
	procDefWindowProcN        = modUser32N.NewProc("DefWindowProcW")
	procShowWindowN           = modUser32N.NewProc("ShowWindow")
	procDestroyWindowN        = modUser32N.NewProc("DestroyWindow")
	procSetLayeredWinAttrN    = modUser32N.NewProc("SetLayeredWindowAttributes")
	procSetTimerN             = modUser32N.NewProc("SetTimer")
	procKillTimerN            = modUser32N.NewProc("KillTimer")
	procGetMessageN           = modUser32N.NewProc("GetMessageW")
	procTranslateMessageN     = modUser32N.NewProc("TranslateMessage")
	procDispatchMessageN      = modUser32N.NewProc("DispatchMessageW")
	procPostQuitMessageN      = modUser32N.NewProc("PostQuitMessage")
	procBeginPaintN           = modUser32N.NewProc("BeginPaint")
	procEndPaintN             = modUser32N.NewProc("EndPaint")
	procDrawTextWN            = modUser32N.NewProc("DrawTextW")
	procGetClientRectN        = modUser32N.NewProc("GetClientRect")
	procSystemParametersInfoN = modUser32N.NewProc("SystemParametersInfoW")

	procCreateSolidBrushN = modGdi32N.NewProc("CreateSolidBrush")
	procCreateFontWN      = modGdi32N.NewProc("CreateFontW")
	procSelectObjectN     = modGdi32N.NewProc("SelectObject")
	procSetTextColorN     = modGdi32N.NewProc("SetTextColor")
	procSetBkModeN        = modGdi32N.NewProc("SetBkMode")

	procGetModuleHandleN = modKernel32N.NewProc("GetModuleHandleW")
)

const (
	wsPopup         = 0x80000000
	wsExTopmost     = 0x00000008
	wsExTransparent = 0x00000020
	wsExToolWindow  = 0x00000080
	wsExLayered     = 0x00080000
	wsExNoActivate  = 0x08000000

	swShowNoActivate = 4
	lwaAlpha         = 0x2

	wmDestroy = 0x0002
	wmPaint   = 0x000F
	wmTimer   = 0x0113

	spiGetWorkArea = 0x0030

	bkTransparent = 1

	dtCenter     = 0x1
	dtVCenter    = 0x4
	dtSingleLine = 0x20
	dtNoPrefix   = 0x800

	hangeulCharset = 129

	noticeTimerID = 1
)

// COLORREF is 0x00BBGGRR.
const (
	noticeBgColor   = 0x002B1E14 // dark slate (BGR of ~#141E2B)
	noticeTextColor = 0x00F0F0F0 // near-white
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type msgStruct struct {
	hwnd    uintptr
	message uint32
	_       uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	ptX     int32
	ptY     int32
	_       uint32
}

type paintStruct struct {
	hdc         uintptr
	fErase      int32
	rcPaint     rect
	fRestore    int32
	fIncUpdate  int32
	rgbReserved [32]byte
}

var (
	noticeClassOnce sync.Once
	noticeClassOK   bool
	noticeClassName = windows.StringToUTF16Ptr("AglinkControlNotice")
	noticeFont      uintptr
)

// registerNoticeClass registers the overlay window class once (with a solid dark
// background brush and a readable font), returning whether registration succeeded.
func registerNoticeClass() bool {
	noticeClassOnce.Do(func() {
		hInst, _, _ := procGetModuleHandleN.Call(0)
		brush, _, _ := procCreateSolidBrushN.Call(uintptr(noticeBgColor))
		// Malgun Gothic renders both Latin and Hangul; height -20 ≈ 15pt @96dpi.
		face := windows.StringToUTF16Ptr("Malgun Gothic")
		noticeFont, _, _ = procCreateFontWN.Call(
			^uintptr(0)-19, // height = -20
			0, 0, 0, 400 /*normal*/, 0, 0, 0,
			hangeulCharset, 0, 0, 0 /*default quality*/, 0,
			uintptr(unsafe.Pointer(face)),
		)
		wc := wndClassExW{
			cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
			lpfnWndProc:   syscall.NewCallback(noticeWndProc),
			hInstance:     hInst,
			hbrBackground: brush,
			lpszClassName: noticeClassName,
		}
		atom, _, _ := procRegisterClassExN.Call(uintptr(unsafe.Pointer(&wc)))
		noticeClassOK = atom != 0
	})
	return noticeClassOK
}

// noticeWndProc paints the text and self-destructs when the dismiss timer fires.
func noticeWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch uint32(msg) {
	case wmPaint:
		var ps paintStruct
		hdc, _, _ := procBeginPaintN.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		if hdc != 0 {
			var rc rect
			procGetClientRectN.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
			rc.Left += 18
			rc.Right -= 18
			procSetBkModeN.Call(hdc, bkTransparent)
			procSetTextColorN.Call(hdc, uintptr(noticeTextColor))
			var oldFont uintptr
			if noticeFont != 0 {
				oldFont, _, _ = procSelectObjectN.Call(hdc, noticeFont)
			}
			txt := windows.StringToUTF16(controlNoticeText)
			// Left-aligned (not centered) so the "aglink-screen" prefix stays
			// visible even if the text is wider than the box on some DPIs.
			procDrawTextWN.Call(hdc, uintptr(unsafe.Pointer(&txt[0])), ^uintptr(0),
				uintptr(unsafe.Pointer(&rc)), uintptr(dtVCenter|dtSingleLine|dtNoPrefix))
			if noticeFont != 0 && oldFont != 0 {
				procSelectObjectN.Call(hdc, oldFont)
			}
			procEndPaintN.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		}
		return 0
	case wmTimer:
		procKillTimerN.Call(hwnd, wParam)
		procDestroyWindowN.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessageN.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcN.Call(hwnd, msg, wParam, lParam)
	return r
}

// runNoticeWindow creates the overlay in the bottom-right of the work area, shows
// it without activation, pumps its messages until the dismiss timer destroys it.
func runNoticeWindow() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if !registerNoticeClass() {
		return
	}

	const w, h, margin = 680, 64, 24
	var wa rect
	x, y := 200, 200
	if r, _, _ := procSystemParametersInfoN.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&wa)), 0); r != 0 {
		x = int(wa.Right) - w - margin
		y = int(wa.Bottom) - h - margin
	}

	hInst, _, _ := procGetModuleHandleN.Call(0)
	hwnd, _, _ := procCreateWindowExN.Call(
		uintptr(wsExTopmost|wsExLayered|wsExToolWindow|wsExNoActivate|wsExTransparent),
		uintptr(unsafe.Pointer(noticeClassName)),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("aglink-screen"))),
		uintptr(wsPopup),
		uintptr(int32(x)), uintptr(int32(y)), uintptr(w), uintptr(h),
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return
	}
	// ~92% opaque overall.
	procSetLayeredWinAttrN.Call(hwnd, 0, 235, lwaAlpha)
	procShowWindowN.Call(hwnd, swShowNoActivate)
	procSetTimerN.Call(hwnd, noticeTimerID, controlNoticeMS, 0)

	var msg msgStruct
	for {
		r, _, _ := procGetMessageN.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 { // 0 = WM_QUIT, -1 = error
			break
		}
		procTranslateMessageN.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageN.Call(uintptr(unsafe.Pointer(&msg)))
	}
}
