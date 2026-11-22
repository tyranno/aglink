//go:build windows

package main

import (
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// clipboard_get / clipboard_set — the Windows clipboard as text (CF_UNICODETEXT).
// Pasting a long or non-ASCII string with clipboard_set + key("ctrl+v") is far
// faster and more faithful than type's per-character events, and clipboard_get
// reads what a "Copy" button produced without a screenshot.

var (
	modKernel32Clip = windows.NewLazySystemDLL("kernel32.dll")

	procOpenClipboard              = modUser32.NewProc("OpenClipboard")
	procCloseClipboard             = modUser32.NewProc("CloseClipboard")
	procEmptyClipboard             = modUser32.NewProc("EmptyClipboard")
	procGetClipboardData           = modUser32.NewProc("GetClipboardData")
	procSetClipboardData           = modUser32.NewProc("SetClipboardData")
	procIsClipboardFormatAvailable = modUser32.NewProc("IsClipboardFormatAvailable")

	procGlobalAlloc  = modKernel32Clip.NewProc("GlobalAlloc")
	procGlobalFree   = modKernel32Clip.NewProc("GlobalFree")
	procGlobalLock   = modKernel32Clip.NewProc("GlobalLock")
	procGlobalUnlock = modKernel32Clip.NewProc("GlobalUnlock")
	procGlobalSize   = modKernel32Clip.NewProc("GlobalSize")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

// clipboardOpenAttempts × clipboardOpenRetry is how long we wait for another
// process (a clipboard manager, the app that just copied) to close the
// clipboard — OpenClipboard fails while anyone else has it open.
const (
	clipboardOpenAttempts = 20
	clipboardOpenRetry    = 25 * time.Millisecond
)

// withClipboard opens the clipboard (retrying while it is busy), runs fn and
// closes it. The OS thread is locked: the clipboard is opened per thread.
func withClipboard(fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var lastErr error
	for i := 0; i < clipboardOpenAttempts; i++ {
		r, _, e := procOpenClipboard.Call(0)
		if r != 0 {
			defer procCloseClipboard.Call()
			return fn()
		}
		lastErr = e
		time.Sleep(clipboardOpenRetry)
	}
	return fmt.Errorf("clipboard is busy (another app keeps it open): %v", lastErr)
}

// clipboardGetText returns the clipboard's text. ok=false when it holds no
// text (empty, or only an image/files).
func clipboardGetText() (text string, ok bool, err error) {
	err = withClipboard(func() error {
		if r, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText); r == 0 {
			return nil
		}
		h, _, e := procGetClipboardData.Call(cfUnicodeText)
		if h == 0 {
			return fmt.Errorf("GetClipboardData: %v", e)
		}
		p, _, e := procGlobalLock.Call(h)
		if p == 0 {
			return fmt.Errorf("GlobalLock: %v", e)
		}
		defer procGlobalUnlock.Call(h)
		size, _, _ := procGlobalSize.Call(h)
		text, ok = utf16Bounded(p, int(size/2)), true
		return nil
	})
	return text, ok, err
}

// utf16Bounded decodes a NUL-terminated UTF-16 string of at most maxUnits
// units at p — bounded so a clipboard block without its NUL can't run past
// its allocation.
func utf16Bounded(p uintptr, maxUnits int) string {
	if p == 0 || maxUnits <= 0 {
		return ""
	}
	units := unsafe.Slice((*uint16)(globalPtr(p)), maxUnits)
	for i, u := range units {
		if u == 0 {
			return windows.UTF16ToString(units[:i])
		}
	}
	return windows.UTF16ToString(units)
}

// globalPtr turns GlobalLock's returned address into a pointer. The block is
// locked (not Go memory, never moved by the GC), so this is safe; going through
// memory instead of unsafe.Pointer(uintptr) keeps vet's unsafeptr check quiet.
func globalPtr(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}

// clipboardSetText replaces the clipboard content with text.
func clipboardSetText(text string) error {
	units, err := windows.UTF16FromString(text)
	if err != nil {
		return fmt.Errorf("text contains a NUL character")
	}
	return withClipboard(func() error {
		if r, _, e := procEmptyClipboard.Call(); r == 0 {
			return fmt.Errorf("EmptyClipboard: %v", e)
		}
		size := uintptr(len(units) * 2)
		h, _, e := procGlobalAlloc.Call(gmemMoveable, size)
		if h == 0 {
			return fmt.Errorf("GlobalAlloc: %v", e)
		}
		p, _, e := procGlobalLock.Call(h)
		if p == 0 {
			procGlobalFree.Call(h)
			return fmt.Errorf("GlobalLock: %v", e)
		}
		copy(unsafe.Slice((*uint16)(globalPtr(p)), len(units)), units)
		procGlobalUnlock.Call(h)
		if r, _, e := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
			procGlobalFree.Call(h) // ownership passes to the system only on success
			return fmt.Errorf("SetClipboardData: %v", e)
		}
		return nil
	})
}
