//go:build windows

package main

import (
	"log"
	"time"
)

// Design Ref: screen_control.keep_awake — prevents the idle-timeout screensaver/
// lock from kicking in while teleclaude is running with screen control enabled,
// since a locked workstation blocks focus_window/click (SendInput/SetForegroundWindow
// can't cross into the Winlogon secure desktop) and capture_window returns a black
// image (BitBlt reads the un-composited Default desktop). This only defeats
// *idle-timeout* locking — it cannot override an explicit Win+L, a GPO-forced
// re-lock, or a lock triggered by closing a laptop lid.

// modKernel32App is declared in screen_apps_windows.go.
var procSetThreadExecutionState = modKernel32App.NewProc("SetThreadExecutionState")

const (
	esContinuous      = 0x80000000
	esSystemRequired  = 0x00000001
	esDisplayRequired = 0x00000002
)

// startKeepAwake asserts ES_SYSTEM_REQUIRED|ES_DISPLAY_REQUIRED and refreshes it
// every minute so the assertion survives across any transient reset. Returns a
// stop func that releases the assertion (Windows also releases it automatically
// if the process dies without calling this).
func startKeepAwake() (stop func()) {
	assert := func() {
		if r, _, _ := procSetThreadExecutionState.Call(uintptr(esContinuous | esSystemRequired | esDisplayRequired)); r == 0 {
			log.Printf("[keepawake] SetThreadExecutionState failed")
		}
	}
	assert()

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				assert()
			case <-done:
				procSetThreadExecutionState.Call(uintptr(esContinuous))
				return
			}
		}
	}()

	var once bool
	return func() {
		if once {
			return
		}
		once = true
		close(done)
	}
}
