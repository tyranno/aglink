//go:build windows

package main

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"
)

// show_numbers / click_number — the Windows side: UIA enumeration of a
// window's actionable elements, the click-through badge overlay, the
// numbered capture, and acting on a number. Pure logic is in
// screen_marks_logic_windows.go.

const (
	defaultMarkMax     = 150
	ceilingMarkMax     = 300
	defaultMarkSeconds = 10
	maxMarkSeconds     = 120
	// markScanLimit bounds the UIA walk: each element costs several
	// cross-process COM calls, and a browser page can expose tens of thousands.
	markScanLimit = 6000

	elemGetCurrentIsOffscreen = 38 // IUIAutomationElement::get_CurrentIsOffscreen
	uiaGetControlViewCond     = 18 // IUIAutomation::get_ControlViewCondition
)

var (
	procIsWindowM        = modUser32.NewProc("IsWindow")
	procGetDpiForWindowM = modUser32.NewProc("GetDpiForWindow")

	marks = newMarkStore()

	// markHeld keeps the UIA elements behind each window's latest numbering
	// (index = markItem.Elem) so click_number can invoke the very element that
	// was numbered. Touched only on the UIA STA thread (inside uiaDo).
	markHeld = map[uintptr][]*ole.IUnknown{}
)

// markWindowAlive reports a window's current rect, false when it is gone or
// minimized (a minimized window's rect is an off-screen placeholder).
func markWindowAlive(hwnd uintptr) (markRect, bool) {
	if r, _, _ := procIsWindowM.Call(hwnd); r == 0 {
		return markRect{}, false
	}
	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		return markRect{}, false
	}
	rc := windowRect(hwnd)
	return markRect{int(rc.Left), int(rc.Top), int(rc.Right), int(rc.Bottom)}, true
}

// virtualScreenRect is the bounding box of all monitors.
func virtualScreenRect() markRect {
	x, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	y, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	w, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	h, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
	l, t := int(int32(x)), int(int32(y))
	return markRect{l, t, l + int(int32(w)), t + int(int32(h))}
}

func intersectRect(a, b markRect) markRect {
	r := markRect{max(a.L, b.L), max(a.T, b.T), min(a.R, b.R), min(a.B, b.B)}
	if r.Empty() {
		return markRect{}
	}
	return r
}

// markTargetWindow resolves show_numbers' window argument ("" = foreground).
func markTargetWindow(window string) (uintptr, error) {
	if strings.TrimSpace(window) != "" {
		return resolveTargetWindow(window)
	}
	fg, _, _ := procGetForegroundWindow.Call()
	if fg == 0 {
		return 0, fmt.Errorf("no foreground window — pass window=")
	}
	return fg, nil
}

// enumerateMarks walks hwnd's UIA tree for visible, enabled, actionable
// elements inside clip, numbers them, and stores the held elements.
func enumerateMarks(hwnd uintptr, clip markRect, maxN int) (items []markItem, truncated int, err error) {
	_, err = uiaDo(func(uia *ole.IUnknown) (string, error) {
		root, err := uiaRoot(uia, hwnd)
		if err != nil {
			return "", err
		}
		defer release(root)

		var cond *ole.IUnknown
		if hr := vcall(uia, uiaGetControlViewCond, uintptr(unsafe.Pointer(&cond))); failed(hr) || cond == nil {
			if hr := vcall(uia, uiaCreateTrueCondition, uintptr(unsafe.Pointer(&cond))); failed(hr) || cond == nil {
				return "", fmt.Errorf("UIA: no search condition (hr=0x%x)", uint32(hr))
			}
		}
		defer release(cond)

		findAll := func() (*ole.IUnknown, int32) {
			var arr *ole.IUnknown
			if hr := vcall(root, elemFindAll, uintptr(treeScopeDescendants),
				uintptr(unsafe.Pointer(cond)), uintptr(unsafe.Pointer(&arr))); failed(hr) || arr == nil {
				return nil, 0
			}
			var l int32
			vcall(arr, arrGetLength, uintptr(unsafe.Pointer(&l)))
			return arr, l
		}
		arr, length := findAll()
		// Chromium-based windows often build their tree only on the first query.
		if length < uiaSnapshotSparseThreshold {
			time.Sleep(uiaSnapshotRetryDelay)
			if arr2, l2 := findAll(); l2 > length {
				if arr != nil {
					release(arr)
				}
				arr, length = arr2, l2
			} else if arr2 != nil {
				release(arr2)
			}
		}
		if arr == nil {
			return "", fmt.Errorf("UIA: FindAll failed for %s", uiaTargetLabel(hwnd))
		}
		defer release(arr)

		var cands []markItem
		var elems []*ole.IUnknown
		for i := int32(0); i < length && i < markScanLimit; i++ {
			var el *ole.IUnknown
			if hr := vcall(arr, arrGetElement, uintptr(i), uintptr(unsafe.Pointer(&el))); failed(hr) || el == nil {
				continue
			}
			keep := false
			func() {
				var rc rect
				if hr := vcall(el, elemGetCurrentBoundingRect, uintptr(unsafe.Pointer(&rc))); failed(hr) {
					return
				}
				r := markRect{int(rc.Left), int(rc.Top), int(rc.Right), int(rc.Bottom)}
				if r.Empty() || !clip.contains(r.CX(), r.CY()) {
					return
				}
				ct := elemInt32(el, elemGetCurrentControlType)
				if !markActionable(ct, true, true) { // a container type: never numbered
					return
				}
				if elemInt32(el, elemGetCurrentIsOffscreen) != 0 || elemInt32(el, elemGetCurrentIsEnabled) == 0 {
					return
				}
				inv := invokeCapable(el)
				if !markActionable(ct, inv, valueCapable(el)) {
					return
				}
				name := elemString(el, elemGetCurrentName)
				if strings.TrimSpace(name) == "" {
					name = elemString(el, elemGetCurrentAutomationId)
				}
				cands = append(cands, markItem{Type: controlTypeName(ct), Name: name, Rect: r, Invokable: inv, Elem: len(elems)})
				elems = append(elems, el)
				keep = true
			}()
			if !keep {
				release(el)
			}
		}

		items, truncated = numberMarks(cands, clip, maxN)
		// Keep the numbered elements, release the rest, and re-index.
		used := make([]bool, len(elems))
		held := make([]*ole.IUnknown, 0, len(items))
		for i := range items {
			used[items[i].Elem] = true
			held = append(held, elems[items[i].Elem])
			items[i].Elem = len(held) - 1
		}
		for i, el := range elems {
			if !used[i] {
				release(el)
			}
		}
		for _, el := range markHeld[hwnd] {
			release(el)
		}
		markHeld[hwnd] = held
		return "", nil
	})
	return items, truncated, err
}

// heldElemRect reads the current rect of a held element; ok=false when the
// element is gone (UIA_E_ELEMENTNOTAVAILABLE) or has no box any more.
func heldElemRect(hwnd uintptr, idx int) (r markRect, ok bool, held bool) {
	_, _ = uiaDo(func(uia *ole.IUnknown) (string, error) {
		hs := markHeld[hwnd]
		if idx < 0 || idx >= len(hs) || hs[idx] == nil {
			return "", nil
		}
		held = true
		var rc rect
		if hr := vcall(hs[idx], elemGetCurrentBoundingRect, uintptr(unsafe.Pointer(&rc))); failed(hr) {
			return "", nil
		}
		r = markRect{int(rc.Left), int(rc.Top), int(rc.Right), int(rc.Bottom)}
		ok = !r.Empty()
		return "", nil
	})
	return r, ok, held
}

// invokeHeld activates a held element through its UIA pattern.
func invokeHeld(hwnd uintptr, idx int, label string) error {
	_, err := uiaDo(func(uia *ole.IUnknown) (string, error) {
		hs := markHeld[hwnd]
		if idx < 0 || idx >= len(hs) || hs[idx] == nil {
			return "", fmt.Errorf("element is no longer held")
		}
		return "", activateElement(hs[idx], label)
	})
	return err
}

// showNumbers implements the show_numbers tool. png is nil unless withCapture.
func showNumbers(window string, seconds, maxN int, withCapture bool) (text string, pngOut []byte, caption string, err error) {
	ensureDPIAware()
	hwnd, err := markTargetWindow(window)
	if err != nil {
		return "", nil, "", err
	}
	if isWindowOnAnotherDesktop(hwnd) {
		_ = bringToFront(hwnd) // like capture_window: the overlay must be on the visible desktop
		time.Sleep(300 * time.Millisecond)
	}
	winRect, ok := markWindowAlive(hwnd)
	if !ok {
		return "", nil, "", fmt.Errorf("window %s is minimized or gone; focus_window it first", uiaTargetLabel(hwnd))
	}
	clip := intersectRect(winRect, virtualScreenRect())
	if clip.Empty() {
		return "", nil, "", fmt.Errorf("window %s is off screen", uiaTargetLabel(hwnd))
	}
	hideMarksOverlay()
	items, truncated, err := enumerateMarks(hwnd, clip, maxN)
	if err != nil {
		return "", nil, "", err
	}
	title := getWindowText(hwnd)
	marks.put(&markSet{HWND: hwnd, Title: title, WinRect: winRect, Created: time.Now(), Items: items})

	label := fmt.Sprintf("window %q (hwnd=0x%x)", title, hwnd)
	text = markListText(label, items, truncated)
	if len(items) == 0 {
		text = fmt.Sprintf("No actionable UI Automation elements visible in %s — this window does not expose them (game, remote desktop, canvas). Try ocr / click_text, or capture_window.", label)
	}
	if fg, _, _ := procGetForegroundWindow.Call(); fg != hwnd && len(items) > 0 {
		text += "\nNote: the window is not in the foreground — badges are drawn on top of whatever covers it; a covered element can still be invoked in the background, but a mouse fallback would hit the covering window."
	}
	if seconds > 0 && len(items) > 0 {
		showMarksOverlay(hwnd, clip, items, seconds)
	}
	if !withCapture {
		return text, nil, "", nil
	}
	pngOut, caption, err = captureWithMarks(hwnd, items)
	if err != nil {
		return text + "\n(capture failed: " + err.Error() + ")", nil, "", nil
	}
	return text, pngOut, caption, nil
}

// captureWithMarks captures the window with the badges drawn into the image,
// capped to the vision long edge like capture_window.
func captureWithMarks(hwnd uintptr, items []markItem) ([]byte, string, error) {
	raw, left, top, w, h, err := captureWindow(fmt.Sprintf("0x%x", hwnd))
	if err != nil {
		return nil, "", err
	}
	src, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}
	img := image.NewRGBA(src.Bounds())
	draw.Draw(img, img.Bounds(), src, src.Bounds().Min, draw.Src)
	dw, dh, scaled := cappedDims(w, h, visionLongEdgeCap)
	f := 1.0
	if scaled {
		f = float64(dw) / float64(w)
	}
	drawMarkBadges(img, left, top, items, markPixelScale(f))
	var out image.Image = img
	if scaled {
		out = downscaleNearest(img, dw, dh)
	}
	enc, err := encodePNG(out)
	if err != nil {
		return nil, "", err
	}
	caption := fmt.Sprintf("Window %q with numbered badges (origin %d,%d, %dx%d). Act by number with click_number(n).", getWindowText(hwnd), left, top, w, h)
	if scaled {
		caption = fmt.Sprintf("Window %q with numbered badges, downscaled to %dx%d (from %dx%d). Act by number with click_number(n) — do not compute coordinates from this image.", getWindowText(hwnd), dw, dh, w, h)
	}
	return enc, caption, nil
}

// clickNumber implements click_number.
func clickNumber(window string, n int, double, right, background bool) (string, error) {
	var hwnd uintptr
	if strings.TrimSpace(window) != "" {
		h, err := resolveTargetWindow(window)
		if err != nil {
			return "", err
		}
		hwnd = h
	}
	hideMarksOverlay()
	ms, it, err := marks.lookup(hwnd, n, time.Now(), markWindowAlive)
	if err != nil {
		return "", err
	}
	desc := fmt.Sprintf("#%d [%s] %q", it.N, it.Type, onelinePreview(it.Name, 60))
	target := it.Rect
	if cur, ok, held := heldElemRect(ms.HWND, it.Elem); held {
		if !ok {
			return "", fmt.Errorf("%w: element %s is gone from %q — call show_numbers again", errMarksStale, desc, ms.Title)
		}
		if elementMoved(it.Rect, cur) {
			return "", fmt.Errorf("%w: element %s moved (content scrolled or changed) — call show_numbers again", errMarksStale, desc)
		}
		target = cur
	}
	fallback := ""
	if background && !double && !right && it.Invokable {
		if err := beginBackgroundOp(); err != nil {
			return "", err
		}
		if err := invokeHeld(ms.HWND, it.Elem, it.Name); err == nil {
			if iconic, _, _ := procIsIconic.Call(ms.HWND); iconic == 0 {
				showAgentCursor(target.CX(), target.CY())
			}
			return fmt.Sprintf("clicked %s via UIA invoke (background: window not focused, cursor untouched — focus_window before typing into it)", desc), nil
		} else {
			fallback = fmt.Sprintf(" (UIA invoke failed: %v)", err)
		}
	} else if background && !double && !right {
		fallback = " (element exposes no UIA invoke)"
	}
	if fg, _, _ := procGetForegroundWindow.Call(); fg != ms.HWND {
		if err := bringToFront(ms.HWND); err != nil {
			return "", fmt.Errorf("focus %q: %w", ms.Title, err)
		}
		time.Sleep(120 * time.Millisecond)
	}
	x, y := target.CX(), target.CY()
	how := "left-click"
	switch {
	case double:
		how = "double-click"
		err = mouseDouble(x, y)
	case right:
		how = "right-click"
		err = mouseClick(x, y, "right")
	default:
		err = mouseClick(x, y, "left")
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s at (%d,%d) via focus+mouse%s%s", how, desc, x, y, fallback, uipiWarning(ms.HWND, "click")), nil
}

// ---- badge overlay ----

const (
	lwaColorKey     = 0x1
	dtCenter        = 0x1
	dtCalcRect      = 0x400
	markOverlayKey  = 0x00FF00FF // magenta: the transparent colour key
	markOverlayFill = 0x007A1F3A // #3A1F7A dark purple (COLORREF 0x00BBGGRR)
	markOverlayEdge = agentCursorColor
	markOverlayText = 0x00FFFFFF
	markOverlayTmr  = 1
)

var (
	markOvClassOnce sync.Once
	markOvClassOK   bool
	markOvClassName = windows.StringToUTF16Ptr("AglinkNumberMarks")
	markOvKeyBrush  uintptr
	markOvFillBrush uintptr
	markOvEdgePen   uintptr

	markOvMu    sync.Mutex
	markOvHwnd  uintptr
	markOvItems []markItem
	markOvClip  markRect
	markOvFont  uintptr
)

// markOverlayShow runs the overlay; a var so tests never open windows.
var markOverlayShow = runMarksOverlay

// showMarksOverlay shows badges for items over clip for seconds, replacing any
// current overlay, and returns once the window exists.
func showMarksOverlay(target uintptr, clip markRect, items []markItem, seconds int) {
	hideMarksOverlay()
	ready := make(chan struct{})
	go markOverlayShow(target, clip, items, seconds, ready)
	select {
	case <-ready:
	case <-time.After(time.Second):
	}
}

// hideMarksOverlay closes the overlay if one is up (cross-thread safe: the
// owning thread handles WM_CLOSE and destroys its own window).
func hideMarksOverlay() {
	markOvMu.Lock()
	h := markOvHwnd
	markOvHwnd = 0
	markOvMu.Unlock()
	if h != 0 {
		procPostMessage.Call(h, wmClose, 0, 0)
	}
}

func registerMarksOverlayClass() bool {
	markOvClassOnce.Do(func() {
		hInst, _, _ := procGetModuleHandleN.Call(0)
		markOvKeyBrush, _, _ = procCreateSolidBrushN.Call(markOverlayKey)
		markOvFillBrush, _, _ = procCreateSolidBrushN.Call(markOverlayFill)
		markOvEdgePen, _, _ = procCreatePenN.Call(psSolid, 1, uintptr(markOverlayEdge))
		wc := wndClassExW{
			cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
			lpfnWndProc:   syscall.NewCallback(marksOverlayWndProc),
			hInstance:     hInst,
			hbrBackground: markOvKeyBrush,
			lpszClassName: markOvClassName,
		}
		atom, _, _ := procRegisterClassExN.Call(uintptr(unsafe.Pointer(&wc)))
		markOvClassOK = atom != 0
	})
	return markOvClassOK
}

func marksOverlayWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch uint32(msg) {
	case wmPaint:
		var ps paintStruct
		hdc, _, _ := procBeginPaintN.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		if hdc != 0 {
			paintMarks(hdc)
			procEndPaintN.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		}
		return 0
	case wmTimer:
		procKillTimerN.Call(hwnd, wParam)
		procDestroyWindowN.Call(hwnd)
		return 0
	case wmDestroy:
		markOvMu.Lock()
		if markOvHwnd == hwnd {
			markOvHwnd = 0
		}
		markOvMu.Unlock()
		procPostQuitMessageN.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcN.Call(hwnd, msg, wParam, lParam)
	return r
}

// paintMarks draws one rounded badge per item at its top-left.
func paintMarks(hdc uintptr) {
	markOvMu.Lock()
	items, clip, font := markOvItems, markOvClip, markOvFont
	markOvMu.Unlock()
	local := markRect{0, 0, clip.W(), clip.H()}
	oldBrush, _, _ := procSelectObjectN.Call(hdc, markOvFillBrush)
	oldPen, _, _ := procSelectObjectN.Call(hdc, markOvEdgePen)
	var oldFont uintptr
	if font != 0 {
		oldFont, _, _ = procSelectObjectN.Call(hdc, font)
	}
	procSetBkModeN.Call(hdc, bkTransparent)
	procSetTextColorN.Call(hdc, markOverlayText)
	for _, it := range items {
		u := windows.StringToUTF16(fmt.Sprint(it.N))
		var tr rect
		procDrawTextWN.Call(hdc, uintptr(unsafe.Pointer(&u[0])), ^uintptr(0),
			uintptr(unsafe.Pointer(&tr)), uintptr(dtCalcRect|dtSingleLine|dtNoPrefix))
		th := int(tr.Bottom - tr.Top)
		w, h := int(tr.Right-tr.Left)+th/2+4, th+2
		el := markRect{it.Rect.L - clip.L, it.Rect.T - clip.T, it.Rect.R - clip.L, it.Rect.B - clip.T}
		br := badgeRect(el, w, h, local)
		procRoundRectN.Call(hdc, uintptr(br.L), uintptr(br.T), uintptr(br.R), uintptr(br.B), uintptr(h/2), uintptr(h/2))
		txt := rect{int32(br.L), int32(br.T), int32(br.R), int32(br.B)}
		procDrawTextWN.Call(hdc, uintptr(unsafe.Pointer(&u[0])), ^uintptr(0),
			uintptr(unsafe.Pointer(&txt)), uintptr(dtCenter|dtVCenter|dtSingleLine|dtNoPrefix))
	}
	if oldFont != 0 {
		procSelectObjectN.Call(hdc, oldFont)
	}
	procSelectObjectN.Call(hdc, oldPen)
	procSelectObjectN.Call(hdc, oldBrush)
}

// runMarksOverlay creates the overlay over clip on its own OS thread and pumps
// it until the timer or hideMarksOverlay closes it.
func runMarksOverlay(target uintptr, clip markRect, items []markItem, seconds int, ready chan struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer func() {
		select {
		case <-ready:
		default:
			close(ready)
		}
	}()
	ensureDPIAware()
	if !registerMarksOverlayClass() {
		return
	}
	dpi := uintptr(96)
	if procGetDpiForWindowM.Find() == nil {
		if d, _, _ := procGetDpiForWindowM.Call(target); d != 0 {
			dpi = d
		}
	}
	face := windows.StringToUTF16Ptr("Segoe UI")
	px := 13 * dpi / 96
	font, _, _ := procCreateFontWN.Call(^(px)+1, 0, 0, 0, fwBold, 0, 0, 0, 1, 0, 0, 0, 0, uintptr(unsafe.Pointer(face)))
	defer func() {
		if font != 0 {
			procDeleteObjectAC.Call(font)
		}
	}()

	hInst, _, _ := procGetModuleHandleN.Call(0)
	hwnd, _, _ := procCreateWindowExN.Call(
		uintptr(wsExTopmost|wsExLayered|wsExToolWindow|wsExNoActivate|wsExTransparent),
		uintptr(unsafe.Pointer(markOvClassName)), 0, uintptr(wsPopup),
		uintptr(int32(clip.L)), uintptr(int32(clip.T)), uintptr(clip.W()), uintptr(clip.H()),
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return
	}
	markOvMu.Lock()
	markOvHwnd, markOvItems, markOvClip, markOvFont = hwnd, items, clip, font
	markOvMu.Unlock()
	procSetLayeredWinAttrN.Call(hwnd, markOverlayKey, 235, lwaColorKey|lwaAlpha)
	procShowWindowN.Call(hwnd, swShowNoActivate)
	procSetTimerN.Call(hwnd, markOverlayTmr, uintptr(seconds*1000), 0)
	close(ready)

	var msg msgStruct
	for {
		r, _, _ := procGetMessageN.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessageN.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageN.Call(uintptr(unsafe.Pointer(&msg)))
	}
	markOvMu.Lock()
	if markOvFont == font {
		markOvFont = 0
	}
	markOvMu.Unlock()
}
