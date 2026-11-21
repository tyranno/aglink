package main

import (
	"errors"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procEnumWindows         = user32.NewProc("EnumWindows")
	procGetWindowTextW      = user32.NewProc("GetWindowTextW")
	procIsWindowVisible     = user32.NewProc("IsWindowVisible")
	procIsIconic            = user32.NewProc("IsIconic")
	procShowWindow          = user32.NewProc("ShowWindow")
	procGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetWindowThreadPID  = user32.NewProc("GetWindowThreadProcessId")
	procGetAncestor         = user32.NewProc("GetAncestor")
	procKeybdEvent          = user32.NewProc("keybd_event")
	procClipboardSeq        = user32.NewProc("GetClipboardSequenceNumber")
)

const (
	vkControl = 0x11
	vkMenu    = 0x12
	vkReturn  = 0x0D
	vkV       = 0x56
	keyUp     = 0x0002
	swRestore = 9
	gaRoot    = 2
)

func windowText(h uintptr) string {
	buf := make([]uint16, 512)
	n, _, _ := procGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

func windowPID(h uintptr) int {
	var pid uint32
	procGetWindowThreadPID.Call(h, uintptr(unsafe.Pointer(&pid)))
	return int(pid)
}

// findVSCodeWindow is the VS Code window of process pid, or failing that one
// whose title starts with the workspace name.
func findVSCodeWindow(pid int, title string) uintptr {
	var byPID, byTitle uintptr
	cb := syscall.NewCallback(func(h, _ uintptr) uintptr {
		if v, _, _ := procIsWindowVisible.Call(h); v == 0 {
			return 1
		}
		t := windowText(h)
		if !strings.Contains(t, "Visual Studio Code") {
			return 1
		}
		if pid > 0 && windowPID(h) == pid && byPID == 0 {
			byPID = h
		}
		if title != "" && byTitle == 0 && strings.Contains(t, title) {
			byTitle = h
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	if byPID != 0 {
		return byPID
	}
	return byTitle
}

func foregroundRoot() uintptr {
	h, _, _ := procGetForegroundWindow.Call()
	r, _, _ := procGetAncestor.Call(h, gaRoot)
	return r
}

// clipboardSeq changes whenever anything is put on the clipboard.
func clipboardSeq() uintptr {
	n, _, _ := procClipboardSeq.Call()
	return n
}

func key(vk byte, up bool) {
	var f uintptr
	if up {
		f = keyUp
	}
	procKeybdEvent.Call(uintptr(vk), 0, f, 0)
}

// pasteAndSend brings the VS Code window forward and presses Ctrl+V, Enter in
// it — the extension has already put the words on the clipboard and the focus
// in Claude Code's input box. Keys go to whatever window is in front, so they
// are only pressed once it is sure to be VS Code.
//
// The clipboard's sequence number is taken first and checked before each key:
// if the user copies something while the window is being brought forward,
// Ctrl+V would paste their copy instead of the words, and Enter would send
// it. Same steps as aglink-voice's engine/Paste.cs.
func pasteAndSend(pid int, title string) error {
	seq := clipboardSeq()
	h := findVSCodeWindow(pid, title)
	if h == 0 {
		return errNoVSCodeWindow
	}
	if foregroundRoot() != h {
		if ic, _, _ := procIsIconic.Call(h); ic != 0 {
			procShowWindow.Call(h, swRestore)
		}
		// Windows only lets the app the user last used change the foreground;
		// a lone Alt press counts as using this one.
		key(vkMenu, false)
		key(vkMenu, true)
		procSetForegroundWindow.Call(h)
		for i := 0; i < 20 && foregroundRoot() != h; i++ {
			time.Sleep(25 * time.Millisecond)
		}
		if foregroundRoot() != h {
			return errors.New("VS Code 창을 앞으로 가져오지 못했어요. 할 말은 클립보드에 넣어 두었어요")
		}
		time.Sleep(150 * time.Millisecond) // the window takes the keyboard focus back
	}
	// Once more right before the keys: in the wait above another window may
	// have come to the front, and Ctrl+V there would paste the words into it.
	if foregroundRoot() != h {
		return errors.New("다른 창이 앞에 있어 붙여넣지 않았어요. 할 말은 클립보드에 넣어 두었어요")
	}
	if clipboardSeq() != seq {
		return errors.New("클립보드가 바뀌어 붙여넣지 않았어요. 다시 말씀해 주세요")
	}
	key(vkControl, false)
	key(vkV, false)
	key(vkV, true)
	key(vkControl, true)
	time.Sleep(120 * time.Millisecond)
	if foregroundRoot() != h {
		return errors.New("VS Code 창이 바뀌어 보내지 못했어요. 입력칸을 확인해 주세요")
	}
	if clipboardSeq() != seq {
		return errors.New("클립보드가 바뀌어 보내지 않았어요. 입력칸을 확인해 주세요")
	}
	key(vkReturn, false)
	key(vkReturn, true)
	return nil
}
