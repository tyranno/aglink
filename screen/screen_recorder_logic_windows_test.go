//go:build windows

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var recT0 = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

func at(ms int) time.Time { return recT0.Add(time.Duration(ms) * time.Millisecond) }

func chars(win, s string, startMs int, secret bool) []recEvent {
	var out []recEvent
	for i, r := range s {
		out = append(out, recEvent{Kind: recChar, Char: r, Window: win, At: at(startMs + 50*i), Secret: secret})
	}
	return out
}

func stepsJSON(t *testing.T, steps []seqStep) string {
	t.Helper()
	b, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestClassifyRecordedKey(t *testing.T) {
	none := recMods{}
	shift := recMods{Shift: true}
	ctrl := recMods{Ctrl: true}
	cases := []struct {
		name string
		vk   uint16
		mods recMods
		ch   rune
		kind string
		want string // Combo for keys, Char for chars
	}{
		{"letter", 'A', none, 'a', recChar, "a"},
		{"shifted letter", 'A', shift, 'A', recChar, "A"},
		{"hangul char from layout", 'A', none, 'ㅁ', recChar, "ㅁ"},
		{"space", 0x20, none, ' ', recChar, " "},
		{"enter", 0x0D, none, '\r', recKey, "enter"},
		{"tab", 0x09, none, '\t', recKey, "tab"},
		{"shift+tab", 0x09, shift, '\t', recKey, "shift+tab"},
		{"ctrl+s", 'S', ctrl, 0x13, recKey, "ctrl+s"},
		{"ctrl+shift+s", 'S', recMods{Ctrl: true, Shift: true}, 0x13, recKey, "ctrl+shift+s"},
		{"alt+f4", 0x73, recMods{Alt: true}, 0, recKey, "alt+f4"},
		{"win+d", 'D', recMods{Win: true}, 'd', recKey, "win+d"},
		{"altgr char", 'Q', recMods{Ctrl: true, Alt: true}, '@', recChar, "@"},
		{"f5", 0x74, none, 0, recKey, "f5"},
		{"backspace", 0x08, none, '\b', recKey, "backspace"},
		{"arrow", 0x28, none, 0, recKey, "down"},
		{"ime", 0xE5, none, 0, recIME, ""},
		{"bare shift", 0xA0, shift, 0, "", ""},
		{"bare ctrl", 0x11, ctrl, 0, "", ""},
		{"unnamed key", 0xFF, none, 0, "", ""},
	}
	for _, c := range cases {
		ev := classifyRecordedKey(c.vk, c.mods, c.ch)
		if ev.Kind != c.kind {
			t.Errorf("%s: kind = %q, want %q", c.name, ev.Kind, c.kind)
			continue
		}
		got := ev.Combo
		if ev.Kind == recChar {
			got = string(ev.Char)
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCoalesceRecordingFormFill(t *testing.T) {
	var ev []recEvent
	ev = append(ev, recEvent{Kind: recClick, Window: "Login", X: 100, Y: 50, Button: "left", At: at(0),
		Elem: recElem{Name: "User", Editable: true}})
	ev = append(ev, chars("Login", "bob", 100, false)...)
	ev = append(ev, recEvent{Kind: recKey, Combo: "backspace", Window: "Login", At: at(300)})
	ev = append(ev, chars("Login", "x", 350, false)...)
	ev = append(ev, recEvent{Kind: recKey, Combo: "tab", Window: "Login", At: at(400)})
	ev = append(ev, chars("Login", "hunter2", 500, true)...)
	ev = append(ev, recEvent{Kind: recKey, Combo: "backspace", Window: "Login", At: at(900), Secret: true})
	ev = append(ev, recEvent{Kind: recClick, Window: "Login", X: 200, Y: 300, Button: "left", At: at(1500),
		Elem: recElem{Name: "Sign in", Win32Button: true, Win32Label: "Sign in", Win32Nth: 0}})
	ev = append(ev, recEvent{Kind: recClick, Window: "Welcome", X: 10, Y: 10, Button: "left", At: at(3000),
		Elem: recElem{Name: "Settings", Invokable: true}})
	ev = append(ev, recEvent{Kind: recKey, Combo: "ctrl+s", Window: "Welcome", At: at(3500)})

	steps := coalesceRecording(ev)
	got := stepsJSON(t, steps)
	want := stepsJSON(t, []seqStep{
		{Action: "wait_for_window", Window: "Login"},
		{Action: "click", X: 100, Y: 50},
		{Action: "type", Text: "box"},
		{Action: "key", Combo: "tab"},
		{Action: "note", Text: "password input skipped (8 keystroke(s)) — fill it in by hand or with set_value"},
		{Action: "click_control", Window: "Login", Text: "Sign in"},
		{Action: "wait_for_window", Window: "Welcome"},
		{Action: "invoke", Window: "Welcome", Name: "Settings"},
		{Action: "key", Combo: "ctrl+s"},
	})
	if got != want {
		t.Fatalf("steps:\n got %s\nwant %s", got, want)
	}
	if strings.Contains(got, "hunter") {
		t.Fatal("password text leaked into the recording")
	}
}

// Clicking a password field makes the following keystrokes secret even when
// the per-key focus check missed it, until tab/enter moves on.
func TestCoalesceRecordingPasswordClickIsSticky(t *testing.T) {
	var ev []recEvent
	ev = append(ev, recEvent{Kind: recClick, Window: "W", X: 1, Y: 1, Button: "left", At: at(0), Elem: recElem{Name: "Password", Editable: true, Password: true}})
	ev = append(ev, chars("W", "pw", 100, false)...)
	ev = append(ev, recEvent{Kind: recKey, Combo: "enter", Window: "W", At: at(300)})
	ev = append(ev, chars("W", "ok", 400, false)...)
	got := stepsJSON(t, coalesceRecording(ev))
	if strings.Contains(got, `"pw"`) || strings.Contains(got, `"p"`) {
		t.Fatalf("password leaked: %s", got)
	}
	if !strings.Contains(got, `"password input skipped (2 keystroke(s))`) || !strings.Contains(got, `{"action":"type","text":"ok"}`) {
		t.Fatalf("unexpected steps: %s", got)
	}
}

func TestCoalesceRecordingDoubleClickAndModifiers(t *testing.T) {
	ev := []recEvent{
		{Kind: recClick, Window: "Explorer", X: 50, Y: 60, Button: "left", At: at(0), Elem: recElem{Name: "file.txt", Invokable: true}},
		{Kind: recClick, Window: "Explorer", X: 51, Y: 61, Button: "left", At: at(200), Elem: recElem{Name: "file.txt", Invokable: true}},
		{Kind: recClick, Window: "Explorer", X: 50, Y: 90, Button: "left", Mods: "ctrl", At: at(1000), Elem: recElem{Name: "b.txt", Invokable: true}},
		{Kind: recClick, Window: "Explorer", X: 50, Y: 90, Button: "right", At: at(2000), Elem: recElem{Name: "b.txt", Invokable: true}},
		// slow second click: two separate clicks
		{Kind: recClick, Window: "Explorer", X: 5, Y: 5, Button: "left", At: at(3000)},
		{Kind: recClick, Window: "Explorer", X: 5, Y: 5, Button: "left", At: at(4000)},
	}
	got := stepsJSON(t, coalesceRecording(ev))
	want := stepsJSON(t, []seqStep{
		{Action: "wait_for_window", Window: "Explorer"},
		{Action: "double_click", X: 50, Y: 60},
		{Action: "click", X: 50, Y: 90, Modifiers: "ctrl"},
		{Action: "click", X: 50, Y: 90, Button: "right"},
		{Action: "click", X: 5, Y: 5},
		{Action: "click", X: 5, Y: 5},
	})
	if got != want {
		t.Fatalf("steps:\n got %s\nwant %s", got, want)
	}
}

func TestCoalesceRecordingWindowsAndIME(t *testing.T) {
	ev := []recEvent{
		{Kind: recWindow, Window: "A", At: at(0)},
		{Kind: recWindow, Window: "A", At: at(10)},
		{Kind: recWindow, Window: "", At: at(20)}, // untitled (desktop/taskbar): ignored
		{Kind: recIME, Window: "A", At: at(30)},
		{Kind: recIME, Window: "A", At: at(40)},
		{Kind: recChar, Char: '1', Window: "B", At: at(50)},
		// an untitled window keeps clicks coordinate-based
		{Kind: recClick, Window: "", X: 9, Y: 9, Button: "left", At: at(2000), Elem: recElem{Name: "Start", Invokable: true}},
	}
	got := stepsJSON(t, coalesceRecording(ev))
	want := stepsJSON(t, []seqStep{
		{Action: "wait_for_window", Window: "A"},
		{Action: "note", Text: "IME input not captured (2 key(s)) — add a type step with the intended text"},
		{Action: "wait_for_window", Window: "B"},
		{Action: "type", Text: "1"},
		{Action: "click", X: 9, Y: 9},
	})
	if got != want {
		t.Fatalf("steps:\n got %s\nwant %s", got, want)
	}
}

func TestCoalesceRecordingEmpty(t *testing.T) {
	if steps := coalesceRecording(nil); len(steps) != 0 {
		t.Fatalf("no events should give no steps, got %v", steps)
	}
}

func TestRecordedStepsReplayableShape(t *testing.T) {
	steps := coalesceRecording(append([]recEvent{{Kind: recClick, Window: "N", X: 1, Y: 2, Button: "left", At: at(0)}}, chars("N", "hi", 10, false)...))
	// What record_workflow returns must be accepted by flow_save / run_sequence.
	if _, n, err := normalizeFlowSteps(stepsJSON(t, steps)); err != nil || n != len(steps) {
		t.Fatalf("recorded steps not storable: %v", err)
	}
	for _, st := range steps {
		if describeStep(st) == "" {
			t.Errorf("no description for %+v", st)
		}
	}
}
