package main

import (
	"log"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// The voice indicator is a small always-on-top pill in the bottom-right
// corner, shown while the main window is in the tray and voice conversation
// is on, so a voice-only session still shows what the app is doing. Only a
// reply's opening sentences are read aloud, so the whole reply pops up in a
// second window just above the pill ("/?reply", ReplyToast.svelte) and fades
// out by itself after a while; the pill's 원문 button brings it back. The
// pages render the main window's "voice:state" / "voice:reply" events
// themselves; this side only decides what is visible and where.

const (
	indicatorW, indicatorH = 380, 52
	replyW, replyH         = 420, 280
	indicatorMargin        = 16
	replyGap               = 8
)

type indicator struct {
	mu         sync.Mutex
	win        *application.WebviewWindow
	replyWin   *application.WebviewWindow
	active     bool // voice conversation on
	hidden     bool // main window in the tray
	shown      bool
	replyShown bool
	lastState  any
	lastReply  any
}

var voiceIndicator indicator

func setupIndicator(app *application.App) {
	voiceIndicator.win = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "indicator",
		Title:            "aglink 음성 대화",
		Width:            indicatorW,
		Height:           indicatorH,
		Frameless:        true,
		AlwaysOnTop:      true,
		DisableResize:    true,
		Hidden:           true,
		BackgroundColour: application.NewRGB(30, 34, 48),
		URL:              "/?indicator",
		Windows:          application.WindowsWindow{HiddenOnTaskbar: true},
	})
	voiceIndicator.replyWin = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "indicator-reply",
		Title:            "aglink 답변",
		Width:            replyW,
		Height:           replyH,
		Frameless:        true,
		AlwaysOnTop:      true,
		DisableResize:    true,
		Hidden:           true,
		BackgroundColour: application.NewRGB(30, 34, 48),
		URL:              "/?reply",
		Windows:          application.WindowsWindow{HiddenOnTaskbar: true},
	})
	app.Event.On("voice:state", func(e *application.CustomEvent) {
		active := false
		if m, ok := e.Data.(map[string]any); ok {
			active, _ = m["active"].(bool)
		}
		voiceIndicator.mu.Lock()
		voiceIndicator.active = active
		voiceIndicator.lastState = e.Data
		voiceIndicator.mu.Unlock()
		voiceIndicator.update(app)
	})
	app.Event.On("voice:reply", func(e *application.CustomEvent) {
		voiceIndicator.mu.Lock()
		defer voiceIndicator.mu.Unlock()
		voiceIndicator.lastReply = e.Data
		if voiceIndicator.shown {
			voiceIndicator.showReply(app)
		}
	})
	// What the page decided about what it heard (sent, dropped as our own
	// voice, cut short), so a misheard conversation can be traced afterwards.
	app.Event.On("voice:log", func(e *application.CustomEvent) {
		log.Printf("[voice] %v", e.Data)
	})
	app.Event.On("reply:hide", func(*application.CustomEvent) {
		voiceIndicator.mu.Lock()
		defer voiceIndicator.mu.Unlock()
		voiceIndicator.hideReply()
	})
	app.Event.On("reply:toggle", func(*application.CustomEvent) {
		voiceIndicator.mu.Lock()
		defer voiceIndicator.mu.Unlock()
		if voiceIndicator.replyShown {
			voiceIndicator.hideReply()
		} else if voiceIndicator.shown && voiceIndicator.lastReply != nil {
			voiceIndicator.showReply(app)
		}
	})
}

func (ind *indicator) setMainHidden(app *application.App, hidden bool) {
	ind.mu.Lock()
	ind.hidden = hidden
	ind.mu.Unlock()
	ind.update(app)
}

// corner is the bottom-right corner of the primary screen's work area.
func corner(app *application.App) (x, y int, ok bool) {
	s := app.Screen.GetPrimary()
	if s == nil {
		return 0, 0, false
	}
	wa := s.WorkArea
	return wa.X + wa.Width - indicatorMargin, wa.Y + wa.Height - indicatorMargin, true
}

// showReply pops the reply window up above the pill; its page starts the
// fade-out timer on "reply:show". Callers hold ind.mu.
func (ind *indicator) showReply(app *application.App) {
	if x, y, ok := corner(app); ok {
		ind.replyWin.SetPosition(x-replyW, y-indicatorH-replyGap-replyH)
	}
	ind.replyWin.Show()
	ind.replyShown = true
	// Not "voice:reply" again: this side listens to that, and re-emitting it
	// looped show → emit → show, flickering the window and starving the page.
	app.Event.Emit("reply:show", ind.lastReply)
}

// hideReply: callers hold ind.mu.
func (ind *indicator) hideReply() {
	if ind.replyShown {
		ind.replyWin.Hide()
		ind.replyShown = false
	}
}

func (ind *indicator) update(app *application.App) {
	ind.mu.Lock()
	defer ind.mu.Unlock()
	if ind.win == nil {
		return
	}
	want := ind.active && ind.hidden
	if want == ind.shown {
		return
	}
	ind.shown = want
	if !want {
		ind.win.Hide()
		ind.hideReply()
		return
	}
	if x, y, ok := corner(app); ok {
		ind.win.SetPosition(x-indicatorW, y-indicatorH)
	}
	ind.win.Show()
	if ind.lastState != nil {
		app.Event.Emit("voice:state", ind.lastState) // the page may have missed it
	}
}
