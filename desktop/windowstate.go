package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// windowState is the persisted desktop window geometry and visibility, so the
// window comes back where, how large and how (maximised, or hidden in the tray)
// it was left. A position that no longer lands on any screen (a monitor was
// unplugged) is dropped at startup by keepWindowOnScreen.
type windowState struct {
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	X         int  `json:"x"`
	Y         int  `json:"y"`
	HasPos    bool `json:"hasPos"`
	Maximised bool `json:"maximised"`
	Hidden    bool `json:"hidden"` // in the tray
}

var winState windowState

func windowStatePath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "desktop-window.json"), nil
}

// loadWindowState reads the saved geometry into winState (best-effort; a missing
// or bad file just leaves the defaults).
func loadWindowState() {
	p, err := windowStatePath()
	if err != nil {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &winState)
}

func saveWindowState() {
	p, err := windowStatePath()
	if err != nil {
		return
	}
	if b, err := json.Marshal(winState); err == nil {
		_ = os.WriteFile(p, b, 0o644)
	}
}

// applyWindowState folds the restored size, position, maximised flag and tray
// state onto the base options.
func applyWindowState(opts application.WebviewWindowOptions) application.WebviewWindowOptions {
	if winState.Width >= opts.MinWidth && winState.Height >= opts.MinHeight {
		opts.Width = winState.Width
		opts.Height = winState.Height
	}
	if winState.HasPos {
		opts.InitialPosition = application.WindowXY
		opts.X = winState.X
		opts.Y = winState.Y
	}
	if winState.Maximised {
		opts.StartState = application.WindowStateMaximised
	}
	opts.Hidden = winState.Hidden
	return opts
}

// onAnyScreen reports whether at least a 100x40 strip of the window's title
// area lies in some screen's work area, i.e. it can still be grabbed and moved.
func onAnyScreen(x, y, w int, screens []*application.Screen) bool {
	for _, s := range screens {
		a := s.WorkArea
		left, right := max(x, a.X), min(x+w, a.X+a.Width)
		top, bottom := max(y, a.Y), min(y+40, a.Y+a.Height)
		if right-left >= 100 && bottom-top >= 40 {
			return true
		}
	}
	return false
}

// keepWindowOnScreen re-centres the window when the restored position is off
// every screen. Screens are only known once the app runs, hence after start.
func keepWindowOnScreen(app *application.App, win *application.WebviewWindow) {
	if !winState.HasPos {
		return
	}
	screens := app.Screen.GetAll()
	if len(screens) == 0 || onAnyScreen(winState.X, winState.Y, winState.Width, screens) {
		return
	}
	win.Center()
	winState.HasPos = false
	saveWindowState()
}

// setWindowHidden records whether the window is in the tray.
func setWindowHidden(hidden bool) {
	if winState.Hidden != hidden {
		winState.Hidden = hidden
		saveWindowState()
	}
}

// captureWindowGeometry updates winState from the live window and persists it.
// When maximised it only flips the flag, keeping the last normal size so
// un-maximising (and the restore size on next launch) stays sensible.
func captureWindowGeometry(win *application.WebviewWindow) {
	if win.IsMinimised() {
		return // Windows parks a minimised window at -32000 with a tiny size
	}
	if win.IsMaximised() {
		winState.Maximised = true
	} else {
		w, h := win.Size()
		if w > 0 && h > 0 {
			winState.Width = w
			winState.Height = h
		}
		winState.X, winState.Y = win.Position()
		winState.HasPos = true
		winState.Maximised = false
	}
	saveWindowState()
}
