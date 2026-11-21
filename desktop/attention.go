package main

import (
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// attentionWindow is the main window, set once at startup, so a finished turn
// can flash its taskbar button while the user is working in another app.
var attentionWindow atomic.Pointer[application.WebviewWindow]

// FlashIfUnfocused flashes the main window's taskbar button until it is
// brought forward, but only when the window is not already focused (a focused
// window needs no nudge). Reports whether it flashed.
func (c *ControlService) FlashIfUnfocused() bool {
	win := attentionWindow.Load()
	if win == nil || win.IsFocused() {
		return false
	}
	win.Flash(true)
	return true
}
