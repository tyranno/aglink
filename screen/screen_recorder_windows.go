//go:build windows

package main

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf16"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"
)

// Workflow recording — the Win32 side. The userwatch low-level hooks forward
// real (non-synthetic) left/right mouse-downs and key-downs here while a
// recording runs. The hook callback only snapshots cheap local state (time,
// point, modifiers, foreground window, whether the focused Win32 control is
// an ES_PASSWORD edit) and never blocks; a processor goroutine resolves each
// event (UIA element under the click, typed character, password/IME state)
// and coalesceRecording turns the result into steps.

var (
	modImm32 = windows.NewLazySystemDLL("imm32.dll")

	procGetAsyncKeyState    = modUser32.NewProc("GetAsyncKeyState")
	procGetKeyStateRec      = modUser32.NewProc("GetKeyState")
	procToUnicodeEx         = modUser32.NewProc("ToUnicodeEx")
	procGetKeyboardLayout   = modUser32.NewProc("GetKeyboardLayout")
	procGetGUIThreadInfo    = modUser32.NewProc("GetGUIThreadInfo")
	procGetAncestor         = modUser32.NewProc("GetAncestor")
	procSendMessageTimeoutW = modUser32.NewProc("SendMessageTimeoutW")
	procImmGetDefaultIMEWnd = modImm32.NewProc("ImmGetDefaultIMEWnd")
)

const (
	wmKeyDown       = 0x0100
	wmSysKeyDown    = 0x0104
	wmLButtonDown   = 0x0201
	wmRButtonDown   = 0x0204
	gaRoot          = 2
	esPassword      = 0x0020
	wmIMEControl    = 0x0283
	imcGetConvMode  = 0x0001
	imcGetOpenState = 0x0005
	smtoAbortIfHung = 0x0002

	recordMaxSecondsDefault = 120
	recordMaxSecondsCap     = 600
)

// rawRecEvent is what a hook callback captures.
type rawRecEvent struct {
	mouse         bool
	msg           uintptr
	x, y          int32
	vk, scan      uint32
	mods          recMods
	caps          bool
	fg            uintptr
	focusPassword bool
	at            time.Time
}

// recordSink receives hook events while a recording runs (nil otherwise).
var recordSink atomic.Pointer[chan rawRecEvent]

var recordingActive atomic.Bool

// recordDropped counts hook events lost to a full sink during a recording.
var recordDropped atomic.Int64

func keyDown(vk uintptr) bool {
	r, _, _ := procGetAsyncKeyState.Call(vk)
	return r&0x8000 != 0
}

func currentRecMods() recMods {
	return recMods{
		Ctrl:  keyDown(0x11),
		Alt:   keyDown(0x12),
		Shift: keyDown(0x10),
		Win:   keyDown(0x5B) || keyDown(0x5C),
	}
}

func pushRecordEvent(ev rawRecEvent) {
	if ch := recordSink.Load(); ch != nil {
		select {
		case *ch <- ev:
		default: // never block the hook; a flood just drops events
			recordDropped.Add(1)
		}
	}
}

// recordMouseFromHook is called from lowLevelMouseProc for real input.
func recordMouseFromHook(wParam uintptr, h *msllHookStruct) {
	if recordSink.Load() == nil || (wParam != wmLButtonDown && wParam != wmRButtonDown) {
		return
	}
	pushRecordEvent(rawRecEvent{mouse: true, msg: wParam, x: h.Pt.X, y: h.Pt.Y, mods: currentRecMods(), at: time.Now()})
}

// guiThreadInfo mirrors GUITHREADINFO.
type guiThreadInfo struct {
	cbSize                                 uint32
	flags                                  uint32
	hwndActive, hwndFocus, hwndCapture     uintptr
	hwndMenuOwner, hwndMoveSize, hwndCaret uintptr
	rcCaret                                rect
}

// recordKeyFromHook is called from lowLevelKeyboardProc for real input.
func recordKeyFromHook(wParam uintptr, h *kbdllHookStruct) {
	if recordSink.Load() == nil || (wParam != wmKeyDown && wParam != wmSysKeyDown) {
		return
	}
	fg, _, _ := procGetForegroundWindow.Call()
	ev := rawRecEvent{msg: wParam, vk: h.VkCode, scan: h.ScanCode, mods: currentRecMods(), fg: fg, at: time.Now()}
	if r, _, _ := procGetKeyStateRec.Call(0x14); r&1 != 0 {
		ev.caps = true
	}
	if fg != 0 {
		tid, _, _ := procGetWindowThreadPID.Call(fg, 0)
		gi := guiThreadInfo{cbSize: uint32(unsafe.Sizeof(guiThreadInfo{}))}
		if ok, _, _ := procGetGUIThreadInfo.Call(tid, uintptr(unsafe.Pointer(&gi))); ok != 0 && gi.hwndFocus != 0 {
			if strings.Contains(strings.ToLower(getClassName(gi.hwndFocus)), "edit") && windowStyle(gi.hwndFocus)&esPassword != 0 {
				ev.focusPassword = true
			}
		}
	}
	pushRecordEvent(ev)
}

// recordedChar is the character a key-down types in the foreground window's
// keyboard layout (0 for none / dead keys). ToUnicodeEx flag 4 leaves the
// kernel keyboard state untouched so the user's own typing is unaffected.
func recordedChar(r rawRecEvent) rune {
	if r.vk == 0xE7 { // VK_PACKET carries the character in the scan code
		return rune(r.scan)
	}
	var ks [256]byte
	if r.mods.Shift {
		ks[0x10] = 0x80
	}
	if r.mods.Ctrl {
		ks[0x11] = 0x80
	}
	if r.mods.Alt {
		ks[0x12] = 0x80
	}
	if r.caps {
		ks[0x14] = 0x01
	}
	tid, _, _ := procGetWindowThreadPID.Call(r.fg, 0)
	hkl, _, _ := procGetKeyboardLayout.Call(tid)
	var buf [8]uint16
	n, _, _ := procToUnicodeEx.Call(uintptr(r.vk), uintptr(r.scan), uintptr(unsafe.Pointer(&ks[0])),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 4, hkl)
	switch int32(n) {
	case 1:
		return rune(buf[0])
	case 2:
		if rs := utf16.Decode(buf[:2]); len(rs) == 1 {
			return rs[0]
		}
	}
	return 0
}

// imeNativeMode reports whether hwnd's IME is open in native (e.g. Hangul)
// conversion mode — then letter keys are composed by the IME and the hook's
// character is not what lands in the field.
func imeNativeMode(hwnd uintptr) bool {
	if hwnd == 0 || procImmGetDefaultIMEWnd.Find() != nil {
		return false
	}
	ime, _, _ := procImmGetDefaultIMEWnd.Call(hwnd)
	if ime == 0 {
		return false
	}
	query := func(cmd uintptr) (uintptr, bool) {
		var res uintptr
		ok, _, _ := procSendMessageTimeoutW.Call(ime, wmIMEControl, cmd, 0, smtoAbortIfHung, 100, uintptr(unsafe.Pointer(&res)))
		return res, ok != 0
	}
	if open, ok := query(imcGetOpenState); !ok || open == 0 {
		return false
	}
	mode, ok := query(imcGetConvMode)
	return ok && mode&1 != 0 // IME_CMODE_NATIVE
}

// uiaRecordElementAt reads what a recorded click hit. ok=false when UIA
// found nothing.
func uiaRecordElementAt(x, y int32) (recElem, bool) {
	var out recElem
	_, err := uiaDo(func(uia *ole.IUnknown) (string, error) {
		el, err := elementFromPoint(uia, x, y)
		if err != nil {
			return "", err
		}
		defer release(el)
		ct := elemInt32(el, elemGetCurrentControlType)
		out.Name = elemString(el, elemGetCurrentName)
		out.AutomationID = elemString(el, elemGetCurrentAutomationId)
		out.Password = elemInt32(el, elemGetCurrentIsPassword) != 0
		out.Editable = ct == 50004 || ct == 50030 || (ct == 50003 && valueCapable(el))
		out.Invokable = invokeCapable(el)
		return "", nil
	})
	return out, err == nil
}

// uiaFocusedIsPassword reports whether the focused UIA element is a password field.
func uiaFocusedIsPassword() bool {
	r, err := uiaDo(func(uia *ole.IUnknown) (string, error) {
		var el *ole.IUnknown
		if hr := vcall(uia, uiaGetFocusedElement, uintptr(unsafe.Pointer(&el))); failed(hr) || el == nil {
			return "", fmt.Errorf("no focused element")
		}
		defer release(el)
		if elemInt32(el, elemGetCurrentIsPassword) != 0 {
			return "yes", nil
		}
		return "", nil
	})
	return err == nil && r == "yes"
}

// resolveRecordedClick turns a mouse-down into a click event.
func resolveRecordedClick(r rawRecEvent) recEvent {
	ev := recEvent{Kind: recClick, At: r.at, X: int(r.x), Y: int(r.y), Button: "left", Mods: r.mods.prefix(true)}
	if r.msg == wmRButtonDown {
		ev.Button = "right"
	}
	// UIA first: a click often closes what it hit (an OK button), so read it
	// before anything else.
	if el, ok := uiaRecordElementAt(r.x, r.y); ok {
		ev.Elem = el
	}
	child, _, _ := procWindowFromPoint.Call(packPoint(r.x, r.y))
	root := child
	if child != 0 {
		if a, _, _ := procGetAncestor.Call(child, gaRoot); a != 0 {
			root = a
		}
	}
	ev.Window = strings.TrimSpace(getWindowText(root))
	if child != root && child != 0 && isButtonClass(getClassName(child)) {
		label := getWindowText(child)
		needle := strings.ToLower(strings.TrimSpace(label))
		if needle != "" {
			idx := 0
			for _, c := range enumControls(root, false) {
				if !strings.Contains(strings.ToLower(c.Text), needle) {
					continue
				}
				if c.HWND == child {
					ev.Elem.Win32Button, ev.Elem.Win32Label, ev.Elem.Win32Nth = true, label, idx
					break
				}
				idx++
			}
		}
	}
	return ev
}

// resolveRecordedKey turns a key-down into an event (Kind "" = ignore).
func resolveRecordedKey(r rawRecEvent) recEvent {
	ev := classifyRecordedKey(uint16(r.vk), r.mods, recordedChar(r))
	if ev.Kind == "" {
		return ev
	}
	if ev.Kind == recChar && ev.Char < 0x80 && unicode.IsLetter(ev.Char) && imeNativeMode(r.fg) {
		ev = recEvent{Kind: recIME}
	}
	ev.At = r.at
	ev.Window = strings.TrimSpace(getWindowText(r.fg))
	if ev.Kind == recChar || ev.Kind == recIME || ev.Combo == "backspace" || ev.Combo == "delete" {
		ev.Secret = r.focusPassword || uiaFocusedIsPassword()
	}
	return ev
}

// recordResult is what one recording produced.
type recordResult struct {
	Steps    []seqStep
	Duration time.Duration
	Stopped  string // "stop key", "time limit", "cancelled"
	Dropped  int64
}

// recordWorkflow records the user's real input until stopVK (no ctrl/alt/win)
// is pressed, maxSeconds pass, or ctx ends.
func recordWorkflow(ctx context.Context, maxSeconds int, stopVK uint16, stopName string) (recordResult, error) {
	if !recordingActive.CompareAndSwap(false, true) {
		return recordResult{}, fmt.Errorf("a recording is already running")
	}
	defer recordingActive.Store(false)

	installUserInputWatcher()
	for i := 0; i < 50 && !userWatcherOK.Load(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if !userWatcherOK.Load() {
		return recordResult{}, fmt.Errorf("could not install the low-level input hooks needed for recording")
	}

	ch := make(chan rawRecEvent, 4096)
	recordDropped.Store(0)
	recordSink.Store(&ch)
	defer recordSink.Store(nil)

	showRecordingNotice(recordingNoticeText(stopName), maxSeconds*1000)
	defer hideRecordingNotice()

	start := time.Now()
	timer := time.NewTimer(time.Duration(maxSeconds) * time.Second)
	defer timer.Stop()
	var events []recEvent
	stopped := ""
	for stopped == "" {
		select {
		case <-ctx.Done():
			stopped = "cancelled"
		case <-timer.C:
			stopped = "time limit"
		case r := <-ch:
			if r.mouse {
				events = append(events, resolveRecordedClick(r))
				continue
			}
			if uint16(r.vk) == stopVK && !r.mods.Ctrl && !r.mods.Alt && !r.mods.Win {
				stopped = "stop key"
				continue
			}
			if ev := resolveRecordedKey(r); ev.Kind != "" {
				events = append(events, ev)
			}
		}
	}
	return recordResult{
		Steps:    coalesceRecording(events),
		Duration: time.Since(start),
		Stopped:  stopped,
		Dropped:  recordDropped.Load(),
	}, nil
}

// recordingNoticeText is the red toast's wording while recording.
func recordingNoticeText(stopName string) string {
	return fmt.Sprintf("녹화 중 — %s 키를 누르면 중지합니다", strings.ToUpper(stopName))
}

// clampRecordSeconds bounds max_seconds.
func clampRecordSeconds(n int) int {
	switch {
	case n <= 0:
		return recordMaxSecondsDefault
	case n < 5:
		return 5
	case n > recordMaxSecondsCap:
		return recordMaxSecondsCap
	}
	return n
}

// parseStopKey resolves stop_key to a single non-modifier key.
func parseStopKey(s string) (uint16, string, error) {
	k := strings.ToLower(strings.TrimSpace(s))
	if k == "" {
		k = "f9"
	}
	if strings.Contains(k, "+") {
		return 0, "", fmt.Errorf("stop_key must be a single key (e.g. f9), not a combo")
	}
	vk, ok := vkMap[k]
	if !ok {
		return 0, "", fmt.Errorf("unknown stop_key %q (use a named key such as f8, f9, f10)", s)
	}
	if isModifierVK(vk) {
		return 0, "", fmt.Errorf("stop_key cannot be a modifier")
	}
	return vk, k, nil
}
