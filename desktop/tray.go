package main

import (
	_ "embed"
	"sync"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// The app lives in the notification area: closing the window only hides it
// (voice conversation keeps working in the background), a click on the tray
// icon brings it back, and "종료" in the tray menu really quits. Going to the
// tray starts voice conversation (the frontend listens for "window:tray").

//go:embed trayicon.png
var trayIcon []byte

var quitting atomic.Bool

func setupTray(app *application.App, win *application.WebviewWindow) {
	hide := func() {
		win.Hide()
		voiceIndicator.setMainHidden(app, true)
		app.Event.Emit("window:tray", map[string]bool{"hidden": true})
	}
	show := func() {
		voiceIndicator.setMainHidden(app, false)
		win.Show()
		if win.IsMinimised() {
			win.Restore()
		}
		win.Focus()
	}

	app.Event.On("indicator:open", func(*application.CustomEvent) { show() })

	menu := app.NewMenu()
	menu.Add("열기").OnClick(func(*application.Context) { show() })
	// The frontend toggles conversation mode on the focused pane, the same as 🗣.
	menu.Add("음성 대화 시작 / 끝").OnClick(func(*application.Context) {
		app.Event.Emit("tray:conversation")
	})
	// The frontend owns the setting; it reports it back in "voice:state".
	barge := menu.AddCheckbox("읽는 중 끼어들기", true)
	barge.OnClick(func(*application.Context) {
		app.Event.Emit("tray:barge")
	})
	app.Event.On("voice:state", func(e *application.CustomEvent) {
		if m, ok := e.Data.(map[string]any); ok {
			if on, ok := m["barge"].(bool); ok && on != barge.Checked() {
				barge.SetChecked(on)
			}
		}
	})
	menu.Add("음성 설정").OnClick(func(*application.Context) {
		show()
		app.Event.Emit("tray:voiceSettings")
	})
	menu.AddSeparator()
	menu.Add("종료").OnClick(func(*application.Context) {
		quitting.Store(true)
		app.Quit()
	})

	tray := app.SystemTray.New()
	tray.SetIcon(trayIcon)
	tray.SetTooltip("aglink")
	tray.SetMenu(menu)
	// The tooltip says what voice conversation is doing, like the indicator.
	var (
		tipMu   sync.Mutex
		lastTip string
	)
	app.Event.On("voice:state", func(e *application.CustomEvent) {
		tip := "aglink"
		if m, ok := e.Data.(map[string]any); ok {
			if active, _ := m["active"].(bool); active {
				if label, _ := m["label"].(string); label != "" {
					tip = "aglink — " + label
				}
			}
		}
		tipMu.Lock()
		defer tipMu.Unlock()
		if tip != lastTip {
			lastTip = tip
			tray.SetTooltip(tip)
		}
	})
	tray.OnClick(func() {
		if win.IsVisible() && !win.IsMinimised() {
			hide()
		} else {
			show()
		}
	})

	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		if quitting.Load() {
			return
		}
		captureWindowGeometry(win)
		e.Cancel()
		hide()
	})
}
