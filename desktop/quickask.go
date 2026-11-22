package main

import (
	"log"
	"strings"
	"unicode/utf8"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 빠른 질문: a system-wide hotkey (Ctrl+Alt+Space, Windows only — see
// hotkey_windows.go) brings the main window forward and asks the frontend to
// open its "빠른 질문" box, with the clipboard's text along so it can be
// offered as the question's material.

// QuickAskHotkeyLabel is how the hotkey is shown to the user.
const QuickAskHotkeyLabel = "Ctrl+Alt+Space"

// maxQuickAskClipRunes caps the clipboard text sent along with the event.
const maxQuickAskClipRunes = 8000

// quickAskPayload is the "hotkey:quickask" event data: the clipboard text,
// trimmed and capped (a huge clipboard is not material for a quick question).
func quickAskPayload(clip string, ok bool) map[string]any {
	text := ""
	if ok {
		text = strings.TrimSpace(clip)
		if utf8.RuneCountInString(text) > maxQuickAskClipRunes {
			text = string([]rune(text)[:maxQuickAskClipRunes])
		}
	}
	return map[string]any{"clipboard": text, "hotkey": QuickAskHotkeyLabel}
}

// showMainWindow brings the main window back from the tray / minimised state
// and gives it focus (the tray's "열기" does the same).
func showMainWindow(app *application.App, win *application.WebviewWindow) {
	voiceIndicator.setMainHidden(app, false)
	setWindowHidden(false)
	win.Show()
	if win.IsMinimised() {
		win.Restore()
	}
	win.Focus()
}

func setupQuickAsk(app *application.App, win *application.WebviewWindow) {
	err := registerGlobalHotkey(func() {
		// The hotkey arrives on its own OS thread; window calls are dispatched
		// to the main thread by Wails.
		application.InvokeAsync(func() {
			showMainWindow(app, win)
			clip, ok := app.Clipboard.Text()
			app.Event.Emit("hotkey:quickask", quickAskPayload(clip, ok))
		})
	})
	if err != nil {
		// Another program may own the combination; the in-app shortcut still works.
		log.Printf("[quickask] global hotkey %s unavailable: %v", QuickAskHotkeyLabel, err)
	}
}
