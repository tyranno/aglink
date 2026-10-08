//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// loadSecurityPrompts returns the specs pass_security_prompts checks against:
// the user's registered app popups first (so a user line can override a built-in
// by listing the same title), then the built-ins. A missing file is normal and
// yields just the built-ins.
func loadSecurityPrompts() []securityPrompt {
	var user []securityPrompt
	if dir, err := dataDir(); err == nil {
		if b, err := os.ReadFile(filepath.Join(dir, userSecurityPromptsFile)); err == nil {
			user = parseUserPrompts(string(b))
		}
	}
	return append(user, builtinSecurityPrompts...)
}

// passSecurityPrompts finds security/confirmation windows currently on screen
// and clicks each one's affirmative button, up to maxN consecutive windows. It
// acts only on a window matched by loadSecurityPrompts; a UAC consent window is
// reported (it cannot be clicked) and anything unrecognized is left alone.
// Returns one line per window handled, plus any UAC note.
func passSecurityPrompts(extraAccept []string, maxN int) ([]string, error) {
	specs := loadSecurityPrompts()
	if maxN <= 0 {
		maxN = 5
	}

	var handled []string
	uacSeen := false
	// A clicked dialog can linger for a moment before it closes, so re-enumerating
	// would find the SAME window and click it again. Remember the handles we have
	// already acted on and skip them; a genuinely new prompt has a new handle.
	done := map[uintptr]bool{}
	for i := 0; i < maxN; i++ {
		acted := false
		for _, w := range enumWindows() {
			if done[w.HWND] {
				continue
			}
			if isUACTitle(w.Title) {
				if !uacSeen {
					handled = append(handled, fmt.Sprintf("UAC %q — the secure desktop blocks synthetic input; a person must approve it", w.Title))
					uacSeen = true
				}
				done[w.HWND] = true
				continue
			}
			spec, ok := matchSecurityPrompt(w.Title, specs)
			if !ok {
				continue
			}
			line, clicked := handleSecurityWindow(w, spec, extraAccept)
			if line != "" {
				handled = append(handled, line)
			}
			if clicked {
				done[w.HWND] = true
				acted = true
				break // re-enumerate: clicking one may raise the next
			}
		}
		if !acted {
			break
		}
		time.Sleep(400 * time.Millisecond) // let the next window (if any) appear
	}
	return handled, nil
}

// handleSecurityWindow clicks the affirmative control of one matched window. For
// a UIA (modern) dialog it goes through passSmartScreen; otherwise it finds a
// Win32 Button labelled like one of the spec's (and any extra) affirmatives and
// clicks it. Returns a report line and whether a click actually happened.
func handleSecurityWindow(w win, spec securityPrompt, extraAccept []string) (string, bool) {
	accept := append(append([]string{}, spec.Buttons...), extraAccept...)

	if spec.UIA {
		if err := passSmartScreen(w.HWND, accept); err != nil {
			return fmt.Sprintf("%s %q -> could not pass: %v", spec.Name, w.Title, err), false
		}
		return fmt.Sprintf("%s %q -> passed via UI Automation", spec.Name, w.Title), true
	}

	btn, found := findAffirmativeButton(w.HWND, accept)
	if !found {
		return fmt.Sprintf("%s %q -> no affirmative button %v found (use snapshot/win_controls to see labels)", spec.Name, w.Title, accept), false
	}
	_ = bringToFront(w.HWND)
	if err := mouseClick(btn.CenterX(), btn.CenterY(), "left"); err != nil {
		return fmt.Sprintf("%s %q -> click %q failed: %v", spec.Name, w.Title, btn.Text, err), false
	}
	return fmt.Sprintf("%s %q -> clicked %q", spec.Name, w.Title, btn.Text), true
}

// passSmartScreen clicks through the SmartScreen dialog, whose controls are UI
// Automation elements rather than Win32 Buttons. SmartScreen hides "Run anyway"
// behind a "More info" expander, so this reveals it first, then invokes the
// affirmative. NOT verified against a live SmartScreen prompt.
func passSmartScreen(hwnd uintptr, accept []string) error {
	if err := bringToFront(hwnd); err != nil {
		return err
	}
	time.Sleep(300 * time.Millisecond)
	// Reveal "Run anyway" (ignore failure: on some builds it is already shown).
	for _, more := range []string{"추가 정보", "More info"} {
		if uiaInvoke(more) == nil {
			time.Sleep(200 * time.Millisecond)
			break
		}
	}
	var lastErr error
	for _, label := range accept {
		if err := uiaInvoke(label); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no affirmative element %v", accept)
	}
	return lastErr
}

// unblockFile clears the "downloaded from the internet" mark (the NTFS
// Zone.Identifier alternate data stream) from path, which is what makes Windows
// raise the Open-File security warning and SmartScreen on an exe. Removing it is
// the equivalent of PowerShell's Unblock-File: a statement that this file is
// trusted. launch_app does this only when explicitly asked (unblock=true).
// A missing stream is not an error.
func unblockFile(path string) error {
	err := os.Remove(path + ":Zone.Identifier")
	if err != nil && !os.IsNotExist(err) {
		// A path that simply has no such stream surfaces as "not found" too;
		// only a real failure (e.g. permission) is worth returning.
		if !strings.Contains(strings.ToLower(err.Error()), "cannot find") {
			return err
		}
	}
	return nil
}
