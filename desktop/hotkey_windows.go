//go:build windows

package main

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32Hotkey       = windows.NewLazySystemDLL("user32.dll")
	procRegisterHotKey = user32Hotkey.NewProc("RegisterHotKey")
	procGetMessageW    = user32Hotkey.NewProc("GetMessageW")
)

const (
	hotkeyModAlt      = 0x0001
	hotkeyModControl  = 0x0002
	hotkeyModNoRepeat = 0x4000
	hotkeyVKSpace     = 0x20
	wmHotkey          = 0x0312
	quickAskHotkeyID  = 0xA61 // any id unique within this thread
)

type winMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
	private uint32
}

// registerGlobalHotkey registers Ctrl+Alt+Space for the whole session on a
// dedicated OS thread (WM_HOTKEY is posted to the registering thread's
// queue) and calls onPress for each press. It returns once registration has
// succeeded or failed; the thread then lives as long as the process.
func registerGlobalHotkey(onPress func()) error {
	result := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // the message queue belongs to this thread
		r, _, callErr := procRegisterHotKey.Call(0, quickAskHotkeyID, hotkeyModControl|hotkeyModAlt|hotkeyModNoRepeat, hotkeyVKSpace)
		if r == 0 {
			result <- fmt.Errorf("RegisterHotKey: %v", callErr)
			runtime.UnlockOSThread()
			return
		}
		result <- nil
		var msg winMsg
		for {
			ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(ret) <= 0 { // WM_QUIT or error
				return
			}
			if msg.message == wmHotkey && msg.wParam == quickAskHotkeyID {
				onPress()
			}
		}
	}()
	return <-result
}
