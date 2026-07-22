package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// windowState is the persisted desktop window geometry so a size you leave (large
// or maximised) is restored next launch. Position is intentionally not saved —
// restoring an absolute X/Y can land the window off-screen after a monitor change.
type windowState struct {
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximised bool `json:"maximised"`
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

// applyWindowState folds the restored size / maximised flag onto the base options.
func applyWindowState(opts application.WebviewWindowOptions) application.WebviewWindowOptions {
	if winState.Width >= opts.MinWidth && winState.Height >= opts.MinHeight {
		opts.Width = winState.Width
		opts.Height = winState.Height
	}
	if winState.Maximised {
		opts.StartState = application.WindowStateMaximised
	}
	return opts
}

// captureWindowGeometry updates winState from the live window and persists it.
// When maximised it only flips the flag, keeping the last normal size so
// un-maximising (and the restore size on next launch) stays sensible.
func captureWindowGeometry(win *application.WebviewWindow) {
	if win.IsMaximised() {
		winState.Maximised = true
	} else {
		w, h := win.Size()
		if w > 0 && h > 0 {
			winState.Width = w
			winState.Height = h
		}
		winState.Maximised = false
	}
	saveWindowState()
}
