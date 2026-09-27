//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// Typing into the Chrome window from outside the browser: the last way past a
// certificate warning, since Chrome keeps extensions off its warning page.

const osKeysSupported = true

var (
	user32                         = syscall.NewLazyDLL("user32.dll")
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	procGetWindowTextW             = user32.NewProc("GetWindowTextW")
	procGetWindowThreadProcessId   = user32.NewProc("GetWindowThreadProcessId")
	procSendInput                  = user32.NewProc("SendInput")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
)

const (
	inputKeyboard                  = 1
	keyeventfKeyUp                 = 0x0002
	keyeventfUnicode               = 0x0004
	processQueryLimitedInformation = 0x1000
)

// keybdInput and winInput mirror KEYBDINPUT and INPUT. The trailing pad makes
// winInput as large as the union's biggest member (MOUSEINPUT), which
// SendInput checks against cbSize.
type keybdInput struct {
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

type winInput struct {
	typ uint32
	ki  keybdInput
	_   [8]byte
}

// foregroundWindow returns the title and executable of the window that has
// the keyboard.
func foregroundWindow() (title, exe string) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return "", ""
	}
	buf := make([]uint16, 512)
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	title = syscall.UTF16ToString(buf[:n])

	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	h, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return title, ""
	}
	defer procCloseHandle.Call(h)
	path := make([]uint16, 1024)
	size := uint32(len(path))
	if ok, _, _ := procQueryFullProcessImageNameW.Call(h, 0, uintptr(unsafe.Pointer(&path[0])), uintptr(unsafe.Pointer(&size))); ok == 0 {
		return title, ""
	}
	return title, syscall.UTF16ToString(path[:size])
}

// typeIntoChrome types text into the foreground window — but only when that
// window is Chrome and is showing the tab titled wantTitle. Anything else in
// front (another app, or another Chrome window, such as a remote-desktop
// session) gets nothing: keystrokes that land in the wrong place are worse
// than none.
//
// Characters go in as Unicode (KEYEVENTF_UNICODE), not virtual keys, so the
// keyboard layout and a Korean IME left in Hangul mode cannot turn "t" into
// "ㅅ".
func typeIntoChrome(wantTitle, text string) error {
	title, exe := foregroundWindow()
	if name := strings.ToLower(filepath.Base(exe)); name != "chrome.exe" {
		if name == "" || name == "." {
			name = "unknown"
		}
		return fmt.Errorf("the window in front is %s (%q), not Chrome — nothing was typed", name, title)
	}
	if wantTitle != "" && !strings.HasPrefix(title, wantTitle) {
		return fmt.Errorf("the Chrome window in front shows %q, not the warning tab %q — nothing was typed", title, wantTitle)
	}
	var ins []winInput
	for _, u := range utf16.Encode([]rune(text)) {
		ins = append(ins,
			winInput{typ: inputKeyboard, ki: keybdInput{wScan: u, dwFlags: keyeventfUnicode}},
			winInput{typ: inputKeyboard, ki: keybdInput{wScan: u, dwFlags: keyeventfUnicode | keyeventfKeyUp}},
		)
	}
	sent, _, err := procSendInput.Call(uintptr(len(ins)), uintptr(unsafe.Pointer(&ins[0])), unsafe.Sizeof(ins[0]))
	if int(sent) != len(ins) {
		return fmt.Errorf("SendInput delivered %d of %d key events (%v) — a locked screen or a higher-privileged window blocks it", sent, len(ins), err)
	}
	return nil
}
