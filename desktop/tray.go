package main

import (
	_ "embed"
	"os/exec"
	"strings"
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
		setWindowHidden(true)
		voiceIndicator.setMainHidden(app, true)
		app.Event.Emit("window:tray", map[string]bool{"hidden": true})
	}
	show := func() {
		voiceIndicator.setMainHidden(app, false)
		setWindowHidden(false)
		win.Show()
		if win.IsMinimised() {
			win.Restore()
		}
		win.Focus()
	}

	// Started hidden because it was left in the tray: the indicator has to know.
	if winState.Hidden {
		voiceIndicator.setMainHidden(app, true)
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
	var (
		bargeMu    sync.Mutex
		bargeOrder stateOrder
	)
	app.Event.On("voice:state", func(e *application.CustomEvent) {
		m, _ := e.Data.(map[string]any)
		bargeMu.Lock()
		defer bargeMu.Unlock()
		if !bargeOrder.fresh(m) {
			return
		}
		if on, ok := m["barge"].(bool); ok && on != barge.Checked() {
			barge.SetChecked(on)
		}
	})
	// Recording runs in the frontend too; it answers with a spoken note.
	menu.Add("녹음 시작 / 끝").OnClick(func(*application.Context) {
		app.Event.Emit("tray:tape")
	})
	menu.Add("녹음 폴더 열기").OnClick(func(*application.Context) {
		if dir, err := tapeDir(); err == nil {
			_ = exec.Command("explorer.exe", dir).Start()
		}
	})
	// Meeting mode, the same as "지금부터 듣고 있어".
	menu.Add("회의 모드 시작 / 끝").OnClick(func(*application.Context) {
		app.Event.Emit("tray:meeting")
	})
	menu.Add("회의 목록").OnClick(func(*application.Context) {
		show()
		app.Event.Emit("tray:meetings")
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
	// The tooltip and a coloured dot on the icon say what voice conversation is
	// doing, like the indicator.
	var (
		tipMu     sync.Mutex
		tipOrder  stateOrder
		lastTip   string
		lastPhase string
	)
	app.Event.On("voice:state", func(e *application.CustomEvent) {
		m, _ := e.Data.(map[string]any)
		tipMu.Lock()
		defer tipMu.Unlock()
		if !tipOrder.fresh(m) {
			return
		}
		tip, phase := "aglink", ""
		if m != nil {
			if active, _ := m["active"].(bool); active {
				if label, _ := m["label"].(string); label != "" {
					tip = "aglink — " + label
				}
				// Which chat the conversation is talking to.
				if title, _ := m["chat"].(string); title != "" {
					tip = "aglink · " + title + "\n" + strings.TrimPrefix(tip, "aglink — ")
				}
				phase, _ = m["phase"].(string)
			} else if wake, _ := m["wake"].(bool); wake {
				name, _ := m["wakeName"].(string) // the user's own name to wake on, if set
				if name == "" {
					name = "아글링크"
				}
				tip = "aglink — “" + name + "”라고 부르면 음성 대화를 시작해요"
			}
		}
		if tip != lastTip {
			lastTip = tip
			tray.SetTooltip(tip)
		}
		// A passing note keeps the dot it had.
		if phase != "note" && phase != lastPhase {
			lastPhase = phase
			tray.SetIcon(trayIconFor(phase))
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
