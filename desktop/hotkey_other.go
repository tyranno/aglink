//go:build !windows

package main

import "errors"

// registerGlobalHotkey: system-wide hotkeys are only wired up on Windows.
func registerGlobalHotkey(func()) error {
	return errors.New("global hotkey is only supported on Windows")
}
