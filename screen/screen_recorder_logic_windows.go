//go:build windows

package main

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Workflow recording — the pure part: classify keystrokes and coalesce a list
// of resolved user events into run_sequence steps. The Win32 side
// (screen_recorder_windows.go) turns hook events into recEvents.

const (
	recClick  = "click"
	recChar   = "char"   // a printable character typed
	recKey    = "key"    // a special key or a combo
	recWindow = "window" // the foreground window changed
	recIME    = "ime"    // IME composition key; the composed text is not visible to the hook
)

// recElem is what was found under a recorded click.
type recElem struct {
	Name         string
	AutomationID string
	Invokable    bool
	Editable     bool
	Password     bool
	Win32Button  bool   // the click hit a Win32 BUTTON-class child
	Win32Label   string // its window text
	Win32Nth     int    // its index among same-label controls (click_control nth)
}

// recEvent is one resolved user action.
type recEvent struct {
	Kind   string
	At     time.Time
	Window string // title of the top-level window it happened in
	X, Y   int
	Button string // "left"/"right"
	Mods   string // e.g. "ctrl+shift" (clicks)
	Elem   recElem
	Char   rune
	Combo  string
	Secret bool // typed into a password field
}

// recMods is the modifier state at a key/mouse event.
type recMods struct{ Ctrl, Alt, Shift, Win bool }

func (m recMods) prefix(withShift bool) string {
	var p []string
	if m.Ctrl {
		p = append(p, "ctrl")
	}
	if m.Alt {
		p = append(p, "alt")
	}
	if m.Shift && withShift {
		p = append(p, "shift")
	}
	if m.Win {
		p = append(p, "win")
	}
	return strings.Join(p, "+")
}

// recVKNames are the names key steps use for non-character keys (the inverse
// of vkMap, with one canonical name per key).
var recVKNames = map[uint16]string{
	0x08: "backspace", 0x09: "tab", 0x0D: "enter", 0x1B: "esc", 0x20: "space",
	0x21: "pageup", 0x22: "pagedown", 0x23: "end", 0x24: "home",
	0x25: "left", 0x26: "up", 0x27: "right", 0x28: "down",
	0x2C: "printscreen", 0x2D: "insert", 0x2E: "delete",
}

// recVKName names vk for a key step: special keys, F1–F12, letters and digits.
func recVKName(vk uint16) string {
	switch {
	case recVKNames[vk] != "":
		return recVKNames[vk]
	case vk >= 0x70 && vk <= 0x7B:
		return fmt.Sprintf("f%d", vk-0x6F)
	case vk >= 'A' && vk <= 'Z', vk >= '0' && vk <= '9':
		return strings.ToLower(string(rune(vk)))
	}
	return ""
}

// isModifierVK covers shift/ctrl/alt/win (generic and left/right) and the lock keys.
func isModifierVK(vk uint16) bool {
	switch vk {
	case 0x10, 0x11, 0x12, 0xA0, 0xA1, 0xA2, 0xA3, 0xA4, 0xA5, 0x5B, 0x5C, 0x14, 0x90, 0x91:
		return true
	}
	return false
}

// classifyRecordedKey maps one key-down to an event: a printable character
// (ch, already shifted/layout-mapped by the caller; 0 if none), a key step
// (special key or a ctrl/alt/win combo), an IME placeholder, or nothing
// (Kind "") for a bare modifier or an unnameable key.
func classifyRecordedKey(vk uint16, mods recMods, ch rune) recEvent {
	if isModifierVK(vk) {
		return recEvent{}
	}
	if vk == 0xE5 { // VK_PROCESSKEY: the IME is composing; the hook never sees the text
		return recEvent{Kind: recIME}
	}
	altGr := mods.Ctrl && mods.Alt && !mods.Win
	combo := mods.Ctrl || mods.Alt || mods.Win
	if ch != 0 && unicode.IsPrint(ch) && (!combo || altGr) && vk != 0x0D && vk != 0x09 {
		return recEvent{Kind: recChar, Char: ch}
	}
	name := recVKName(vk)
	if name == "" {
		return recEvent{}
	}
	if name == "space" && !combo {
		return recEvent{Kind: recChar, Char: ' '}
	}
	// Shift is part of a combo only for non-character keys (shift+tab) or
	// alongside another modifier (ctrl+shift+s).
	if p := mods.prefix(true); p != "" {
		name = p + "+" + name
	}
	return recEvent{Kind: recKey, Combo: name}
}

// doubleClickWindow / doubleClickSlop decide when two left clicks are a double click.
const (
	doubleClickWindow = 500 * time.Millisecond
	doubleClickSlop   = 4
)

// coalesceRecording turns resolved events into run_sequence steps:
//   - a click in a window other than the current one is preceded by
//     wait_for_window, as is a key/char/window event in a new window;
//   - a click on a Win32 button becomes click_control{window,text,nth}, on a
//     named invokable UIA element invoke{window,name}, else click{x,y}
//     (modified or right clicks always stay coordinate clicks); two quick left
//     clicks on one spot become double_click;
//   - consecutive characters become one type step (backspace edits it);
//   - keystrokes into a password field (or after clicking one, until tab/
//     enter/a click) become a single note step instead of their text;
//   - IME composition keys become a note that the text was not captured.
func coalesceRecording(events []recEvent) []seqStep {
	var steps []seqStep
	var typed []rune
	curWindow := ""
	secretRun, imeRun := 0, 0
	secretFocus := false
	lastClick := -1 // index in steps of a plain left coordinate/name click that may become a double click
	var lastClickEv recEvent

	flushType := func() {
		if len(typed) > 0 {
			steps = append(steps, seqStep{Action: "type", Text: string(typed), Window: curWindow})
			typed = nil
		}
	}
	flushNotes := func() {
		if secretRun > 0 {
			steps = append(steps, seqStep{Action: "note", Text: fmt.Sprintf("password input skipped (%d keystroke(s)) — fill it in by hand or with set_value", secretRun)})
			secretRun = 0
		}
		if imeRun > 0 {
			steps = append(steps, seqStep{Action: "note", Text: fmt.Sprintf("IME input not captured (%d key(s)) — add a type step with the intended text", imeRun)})
			imeRun = 0
		}
	}
	flushAll := func() { flushType(); flushNotes() }
	enterWindow := func(w string) {
		if w == "" || w == curWindow {
			return
		}
		flushAll()
		steps = append(steps, seqStep{Action: "wait_for_window", Window: w})
		curWindow = w
		lastClick = -1
	}

	for _, ev := range events {
		switch ev.Kind {
		case recWindow:
			enterWindow(ev.Window)
		case recClick:
			enterWindow(ev.Window)
			flushAll()
			secretFocus = ev.Elem.Password
			if lastClick == len(steps)-1 && lastClick >= 0 && ev.Button == "left" && ev.Mods == "" &&
				ev.At.Sub(lastClickEv.At) <= doubleClickWindow &&
				abs(ev.X-lastClickEv.X) <= doubleClickSlop && abs(ev.Y-lastClickEv.Y) <= doubleClickSlop {
				steps[lastClick] = seqStep{Action: "double_click", X: lastClickEv.X, Y: lastClickEv.Y}
				lastClick = -1
				continue
			}
			steps = append(steps, clickStep(ev, ev.Window))
			lastClick, lastClickEv = -1, ev
			if ev.Button == "left" && ev.Mods == "" {
				lastClick = len(steps) - 1
			}
		case recChar, recKey, recIME:
			enterWindow(ev.Window)
			lastClick = -1
			secret := ev.Secret || secretFocus
			switch {
			case ev.Kind == recIME:
				flushType()
				if secretRun > 0 {
					flushNotes()
				}
				imeRun++
			case secret && (ev.Kind == recChar || ev.Combo == "backspace" || ev.Combo == "delete"):
				flushType()
				if imeRun > 0 {
					flushNotes()
				}
				secretRun++
			case ev.Kind == recChar:
				flushNotes()
				typed = append(typed, ev.Char)
			case ev.Combo == "backspace" && len(typed) > 0:
				typed = typed[:len(typed)-1]
			default:
				flushAll()
				steps = append(steps, seqStep{Action: "key", Combo: ev.Combo})
				if ev.Combo == "tab" || ev.Combo == "shift+tab" || ev.Combo == "enter" {
					secretFocus = false
				}
			}
		}
	}
	flushAll()
	// type steps carried the window only to keep flushType simple; replay
	// types into the focused control, so drop it.
	for i := range steps {
		if steps[i].Action == "type" {
			steps[i].Window = ""
		}
	}
	return steps
}

// clickStep picks the most robust step for a click.
func clickStep(ev recEvent, window string) seqStep {
	el := ev.Elem
	plain := ev.Button == "left" && ev.Mods == ""
	if plain && window != "" {
		if el.Win32Button && strings.TrimSpace(el.Win32Label) != "" {
			return seqStep{Action: "click_control", Window: window, Text: el.Win32Label, Nth: el.Win32Nth}
		}
		name := el.Name
		if strings.TrimSpace(name) == "" {
			name = el.AutomationID
		}
		if el.Invokable && !el.Editable && strings.TrimSpace(name) != "" {
			return seqStep{Action: "invoke", Window: window, Name: name}
		}
	}
	st := seqStep{Action: "click", X: ev.X, Y: ev.Y, Modifiers: ev.Mods}
	if ev.Button != "left" {
		st.Button = ev.Button
	}
	return st
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// describeStep is one human-readable summary line.
func describeStep(st seqStep) string {
	switch st.Action {
	case "wait_for_window":
		return fmt.Sprintf("wait for window %q", st.Window)
	case "click_control":
		return fmt.Sprintf("click button %q in %q", st.Text, st.Window)
	case "invoke":
		return fmt.Sprintf("invoke %q in %q", st.Name, st.Window)
	case "click", "double_click":
		b := st.Button
		if b == "" {
			b = "left"
		}
		if st.Modifiers != "" {
			b = st.Modifiers + "+" + b
		}
		if st.Action == "double_click" {
			b = "double"
		}
		return fmt.Sprintf("%s-click at (%d,%d)", b, st.X, st.Y)
	case "type":
		return fmt.Sprintf("type %q", st.Text)
	case "key":
		return fmt.Sprintf("press %s", st.Combo)
	case "note":
		return "note: " + st.Text
	}
	return st.Action
}
