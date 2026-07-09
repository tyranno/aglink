//go:build windows

package main

import (
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Real-user-input watcher.
//
// Before this, the ONLY signal we had that a user might collide with our
// synthetic input was ensureControlNotice's blind pause on every new control
// "session" (first input, or after an 8s idle gap) — regardless of whether
// anyone was actually at the keyboard. In an LLM-driven flow, tool calls are
// almost always spaced further apart than that gap (thinking + generation
// time between turns), so nearly every click/type/key call paid the full
// toast+lead-delay tax even when the user had stepped away entirely. That
// fixed tax, repeated across a multi-step flow (e.g. composing an email), is
// what shows up to the user as a very long wait.
//
// A WH_MOUSE_LL/WH_KEYBOARD_LL hook gives us ground truth instead of a blind
// wait: every SendInput event we generate is tagged with syntheticInputTag in
// dwExtraInfo (see mouseEvent/keyEvent in screen_input_windows.go), so the
// hook can tell our own synthetic events apart from genuine user-generated
// ones and timestamp the most recent real one. beginSyntheticInput and
// ensureControlNotice (screen_notify_windows.go) use that timestamp to:
//   - skip the blind lead-delay when no one has touched the mouse/keyboard
//     recently (nothing to warn about — the dominant latency fix), and
//   - yield control back to the user when they ARE active right now, pausing
//     automation until they go quiet instead of racing their input.
//
// The hook only observes: it always calls CallNextHookEx and never blocks or
// swallows an event, so real user input is completely unaffected.

var (
	modUser32Watch = windows.NewLazySystemDLL("user32.dll")

	procSetWindowsHookExW   = modUser32Watch.NewProc("SetWindowsHookExW")
	procCallNextHookEx      = modUser32Watch.NewProc("CallNextHookEx")
	procUnhookWindowsHookEx = modUser32Watch.NewProc("UnhookWindowsHookEx")
)

const (
	whMouseLL    = 14
	whKeyboardLL = 13
)

// msllHookStruct mirrors Win32's MSLLHOOKSTRUCT.
type msllHookStruct struct {
	Pt          struct{ X, Y int32 }
	MouseData   uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

// kbdllHookStruct mirrors Win32's KBDLLHOOKSTRUCT.
type kbdllHookStruct struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

// lastRealUserInputNano is updated from the hook callbacks every time a mouse
// or keyboard event arrives WITHOUT our syntheticInputTag — i.e. a genuine
// user action, not one of our own SendInput calls.
var lastRealUserInputNano atomic.Int64

// userWatcherOK reports whether the low-level hooks installed successfully.
// The lead-delay skip in ensureControlNotice only applies when this is true —
// if hook installation ever fails (hook quota, locked-down session, ...) we
// fall back to the old always-pause-on-session-start behavior rather than
// silently disabling the safety warning.
var userWatcherOK atomic.Bool

var userWatchOnce sync.Once

// installUserInputWatcher lazily installs the low-level hooks on a dedicated,
// OS-thread-locked goroutine with its own message pump (required for
// WH_*_LL hooks to receive callbacks). Safe to call repeatedly — only the
// first call does anything. A package var so tests can stub it out and avoid
// installing a real global hook.
var installUserInputWatcher = func() {
	userWatchOnce.Do(func() {
		go runUserInputWatcher()
	})
}

func runUserInputWatcher() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	mouseHook, _, _ := procSetWindowsHookExW.Call(
		uintptr(whMouseLL),
		syscall.NewCallback(lowLevelMouseProc),
		0, 0,
	)
	kbdHook, _, _ := procSetWindowsHookExW.Call(
		uintptr(whKeyboardLL),
		syscall.NewCallback(lowLevelKeyboardProc),
		0, 0,
	)
	if mouseHook == 0 && kbdHook == 0 {
		return // both failed; nothing to pump, userWatcherOK stays false
	}
	userWatcherOK.Store(true)
	defer func() {
		if mouseHook != 0 {
			procUnhookWindowsHookEx.Call(mouseHook)
		}
		if kbdHook != 0 {
			procUnhookWindowsHookEx.Call(kbdHook)
		}
	}()

	// WH_*_LL hooks are only delivered on the thread that installed them, and
	// only while it pumps messages — reuses the same GetMessage/Translate/
	// Dispatch triplet as the notice overlay's message loop.
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

// lowLevelMouseProc observes every mouse event system-wide. nCode < 0 means
// "not ours to inspect" per MSDN — pass it straight through.
//
// nCode and lParam are declared as uintptr/unsafe.Pointer (word-sized), not
// int32/typed-pointer, because syscall.NewCallback's trampoline marshals every
// argument as a full machine word regardless of the Win32 prototype's real
// type (the same reason noticeWndProc below takes uintptr for msg, a UINT) —
// a smaller Go type here would panic at NewCallback time. Receiving lParam
// already as unsafe.Pointer (rather than uintptr) also means the struct cast
// below is a pointer-to-pointer conversion, not a uintptr->Pointer one.
func lowLevelMouseProc(nCode, wParam uintptr, lParam unsafe.Pointer) uintptr {
	if int32(nCode) >= 0 && lParam != nil {
		h := (*msllHookStruct)(lParam)
		if h.DwExtraInfo != syntheticInputTag {
			lastRealUserInputNano.Store(time.Now().UnixNano())
		}
	}
	r, _, _ := procCallNextHookEx.Call(0, nCode, wParam, uintptr(lParam))
	return r
}

// lowLevelKeyboardProc observes every keyboard event system-wide, mirroring
// lowLevelMouseProc (see its comment for why the parameter types are this way).
func lowLevelKeyboardProc(nCode, wParam uintptr, lParam unsafe.Pointer) uintptr {
	if int32(nCode) >= 0 && lParam != nil {
		h := (*kbdllHookStruct)(lParam)
		if h.DwExtraInfo != syntheticInputTag {
			lastRealUserInputNano.Store(time.Now().UnixNano())
		}
	}
	r, _, _ := procCallNextHookEx.Call(0, nCode, wParam, uintptr(lParam))
	return r
}

// neverObservedSentinelMS is returned by msSinceRealUserInput when no genuine
// user input has ever been observed — effectively "infinitely long ago".
const neverObservedSentinelMS = int64(1) << 40

// msSinceRealUserInput returns milliseconds since the last genuine
// (non-synthetic) mouse/keyboard event, or neverObservedSentinelMS if none
// has ever been observed. A package var (rather than calling it directly) so
// tests can fake user presence/absence without a real Win32 hook.
var msSinceRealUserInput = func() int64 {
	last := lastRealUserInputNano.Load()
	if last == 0 {
		return neverObservedSentinelMS
	}
	return (time.Now().UnixNano() - last) / int64(time.Millisecond)
}
