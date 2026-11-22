//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// seqStep is one action in a run_sequence batch. Only the fields relevant to
// Action are read; the rest are ignored, mirroring each action's own tool
// parameters so callers can reuse the same mental model.
type seqStep struct {
	Action    string `json:"action"`
	X         int    `json:"x,omitempty"`
	Y         int    `json:"y,omitempty"`
	X2        int    `json:"x2,omitempty"`
	Y2        int    `json:"y2,omitempty"`
	Dx        int    `json:"dx,omitempty"`
	Dy        int    `json:"dy,omitempty"`
	Button    string `json:"button,omitempty"`
	Modifiers string `json:"modifiers,omitempty"`
	Window    string `json:"window,omitempty"`
	Name      string `json:"name,omitempty"`
	Text      string `json:"text,omitempty"`
	Combo     string `json:"combo,omitempty"`
	Nth       int    `json:"nth,omitempty"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
	HoldMs    int    `json:"hold_ms,omitempty"`
	Ms        int    `json:"ms,omitempty"`      // wait: how long
	WaitMs    int    `json:"wait_ms,omitempty"` // any step: pause after it (screen transitions, animations)
	// Background is click_control's opt-out (nil = default true).
	Background *bool `json:"background,omitempty"`
	Steps      int   `json:"steps,omitempty"` // drag: interpolated moves
	Gone       bool  `json:"gone,omitempty"`  // wait_for_window: wait for it to disappear

	// UIA selector extras (invoke / set_value / wait_for_control / set_state).
	// Nth above is 1-based for these, 0-based for click_control (its own tool's
	// convention).
	ControlType string `json:"control_type,omitempty"`
	Match       string `json:"match,omitempty"`

	// wait_for_control conditions; Value is a string there and a number for
	// set_state, so it is decoded per action.
	Until    string          `json:"until,omitempty"`
	Contains string          `json:"contains,omitempty"`
	Value    json.RawMessage `json:"value,omitempty"`

	// set_state.
	Checked        *bool  `json:"checked,omitempty"`
	Expanded       *bool  `json:"expanded,omitempty"`
	Selected       *bool  `json:"selected,omitempty"`
	ScrollIntoView bool   `json:"scroll_into_view,omitempty"`
	SelectItem     string `json:"select_item,omitempty"`
}

// selector is the step's UIA selector.
func (st seqStep) selector() (uiaSelector, error) {
	return newSelector(st.Name, st.ControlType, st.Nth, st.Match)
}

// valueString decodes "value" as text (a JSON number is taken verbatim);
// set=false when absent.
func (st seqStep) valueString() (v string, set bool, err error) {
	if len(st.Value) == 0 || string(st.Value) == "null" {
		return "", false, nil
	}
	if err := json.Unmarshal(st.Value, &v); err == nil {
		return v, true, nil
	}
	var n json.Number
	if err := json.Unmarshal(st.Value, &n); err == nil {
		return n.String(), true, nil
	}
	return "", false, fmt.Errorf("\"value\" must be a string or number")
}

// valueNumber decodes "value" as a number (a numeric string is accepted); nil
// when absent.
func (st seqStep) valueNumber() (*float64, error) {
	s, set, err := st.valueString()
	if err != nil || !set {
		return nil, err
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return nil, fmt.Errorf("\"value\" must be a number for set_state, got %q", s)
	}
	return &f, nil
}

// maxSeqWait caps any single pause so a typo (60000 for 600) cannot stall a
// turn for minutes.
const maxSeqWait = 60 * time.Second

// seqDelayFromEnv is the default pause between steps when the call gives
// none: AGLINK_SCREEN_STEP_DELAY_MS, which the installer can set on the MCP
// registration for a machine whose UI needs a beat between inputs.
func seqDelayFromEnv() time.Duration {
	n, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("AGLINK_SCREEN_STEP_DELAY_MS")))
	if n <= 0 {
		return 0
	}
	return capSeqWait(time.Duration(n) * time.Millisecond)
}

func capSeqWait(d time.Duration) time.Duration {
	if d > maxSeqWait {
		return maxSeqWait
	}
	return d
}

// seqStepResult is one line of runSequence's step-by-step report.
type seqStepResult struct {
	Index  int    `json:"index"`
	Action string `json:"action"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// runSequence executes steps in order, stopping at the first failure so the
// caller sees exactly how far it got instead of a batch that silently
// partially applied. It reuses the same functions each individual tool
// (click/type/key/...) calls, so behavior is identical to calling them one
// at a time — this only removes the round-trips between them. Screen input
// has no natural concurrency to manage (one desktop, one input stream), so
// steps just run sequentially on the calling goroutine.
//
// delay is a pause between consecutive steps (0 for none); a step's own
// wait_ms adds a pause after that one step — for a screen that slides in or a
// window that takes a moment to appear after a click.
func runSequence(stepsJSON string, delay time.Duration) ([]seqStepResult, error) {
	var steps []seqStep
	if err := json.Unmarshal([]byte(stepsJSON), &steps); err != nil {
		return nil, fmt.Errorf("invalid steps JSON: %w", err)
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("steps is empty")
	}
	delay = capSeqWait(delay)
	results := make([]seqStepResult, 0, len(steps))
	for i, st := range steps {
		if i > 0 && delay > 0 {
			time.Sleep(delay)
		}
		detail, err := runOneStep(st)
		if err != nil {
			results = append(results, seqStepResult{Index: i, Action: st.Action, OK: false, Detail: err.Error()})
			return results, fmt.Errorf("step %d (%s) failed: %w", i, st.Action, err)
		}
		results = append(results, seqStepResult{Index: i, Action: st.Action, OK: true, Detail: detail})
		if st.WaitMs > 0 {
			time.Sleep(capSeqWait(time.Duration(st.WaitMs) * time.Millisecond))
		}
	}
	return results, nil
}

// runOneStep dispatches a single step to the same function its standalone
// MCP tool calls.
func runOneStep(st seqStep) (string, error) {
	switch st.Action {
	case "wait":
		if st.Ms <= 0 {
			return "", fmt.Errorf("wait requires a positive \"ms\"")
		}
		w := capSeqWait(time.Duration(st.Ms) * time.Millisecond)
		time.Sleep(w)
		return fmt.Sprintf("waited %dms", w.Milliseconds()), nil
	case "click":
		button := st.Button
		if button == "" {
			button = "left"
		}
		if st.Modifiers != "" {
			if err := mouseClickMods(st.X, st.Y, button, strings.Split(st.Modifiers, "+")); err != nil {
				return "", err
			}
			return fmt.Sprintf("%s+%s-clicked at (%d,%d)", st.Modifiers, button, st.X, st.Y), nil
		}
		if err := mouseClick(st.X, st.Y, button); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s-clicked at (%d,%d)", button, st.X, st.Y), nil
	case "double_click":
		if err := mouseDouble(st.X, st.Y); err != nil {
			return "", err
		}
		return fmt.Sprintf("double-clicked at (%d,%d)", st.X, st.Y), nil
	case "triple_click":
		if err := mouseTriple(st.X, st.Y); err != nil {
			return "", err
		}
		return fmt.Sprintf("triple-clicked at (%d,%d)", st.X, st.Y), nil
	case "type":
		if err := typeText(st.Text); err != nil {
			return "", err
		}
		return fmt.Sprintf("typed %d character(s)", len([]rune(st.Text))), nil
	case "key":
		if err := keyComboHold(st.Combo, st.HoldMs); err != nil {
			return "", err
		}
		return fmt.Sprintf("pressed %q", st.Combo), nil
	case "invoke":
		sel, err := st.selector()
		if err != nil {
			return "", err
		}
		hwnd, err := resolveTargetWindow(st.Window)
		if err != nil {
			return "", err
		}
		note, err := uiaInvokeIn(hwnd, sel)
		if err != nil {
			return "", err
		}
		return withNote(fmt.Sprintf("invoked %q%s", st.Name, backgroundSuffix(hwnd)), note), nil
	case "set_value":
		sel, err := st.selector()
		if err != nil {
			return "", err
		}
		hwnd, err := resolveTargetWindow(st.Window)
		if err != nil {
			return "", err
		}
		note, err := uiaSetValueIn(hwnd, sel, st.Text)
		if err != nil {
			return "", err
		}
		return withNote(fmt.Sprintf("set %q = %q%s", st.Name, st.Text, backgroundSuffix(hwnd)), note), nil
	case "set_state":
		sel, err := st.selector()
		if err != nil {
			return "", err
		}
		val, err := st.valueNumber()
		if err != nil {
			return "", err
		}
		hwnd, err := resolveTargetWindow(st.Window)
		if err != nil {
			return "", err
		}
		out, err := uiaSetStateIn(hwnd, sel, stateRequest{
			Checked: st.Checked, Expanded: st.Expanded, Selected: st.Selected, Value: val,
			ScrollIntoView: st.ScrollIntoView, SelectItem: st.SelectItem,
		})
		if err != nil {
			return "", err
		}
		return out + backgroundSuffix(hwnd), nil
	case "click_control":
		return clickControl(st.Window, st.Text, st.Nth, st.Background == nil || *st.Background)
	case "wait_for_control":
		sel, err := st.selector()
		if err != nil {
			return "", err
		}
		v, set, err := st.valueString()
		if err != nil {
			return "", err
		}
		cond, err := newWaitCond(st.Until, v, set, st.Contains)
		if err != nil {
			return "", err
		}
		return uiaWaitForControlIn(st.Window, sel, cond, st.TimeoutMs)
	case "clipboard_set":
		if err := clipboardSetText(st.Text); err != nil {
			return "", err
		}
		return fmt.Sprintf("clipboard set (%d character(s))", len([]rune(st.Text))), nil
	case "clipboard_get":
		text, ok, err := clipboardGetText()
		if err != nil {
			return "", err
		}
		if !ok {
			return "clipboard holds no text", nil
		}
		return fmt.Sprintf("clipboard = %q", onelinePreview(text, snapshotPreviewChars*2)), nil
	case "note":
		// A placeholder left by record_workflow (e.g. a skipped password); no-op.
		return "note: " + st.Text, nil
	case "wait_for_window":
		if st.Gone {
			return waitForWindowGone(st.Window, st.TimeoutMs)
		}
		return waitForWindow(st.Window, st.TimeoutMs)
	case "scroll":
		if st.Dx == 0 && st.Dy == 0 {
			return "", fmt.Errorf("scroll requires a non-zero dx or dy")
		}
		if err := scroll(st.Dx, st.Dy); err != nil {
			return "", err
		}
		return fmt.Sprintf("scrolled dx=%d dy=%d", st.Dx, st.Dy), nil
	case "drag":
		button := st.Button
		if button == "" {
			button = "left"
		}
		if err := mouseDrag(st.X, st.Y, st.X2, st.Y2, button, st.HoldMs, st.Steps); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s-dragged (%d,%d) -> (%d,%d)", button, st.X, st.Y, st.X2, st.Y2), nil
	default:
		return "", fmt.Errorf("unknown action %q (supported: click, double_click, triple_click, type, key, invoke, set_value, set_state, click_control, wait_for_control, wait_for_window, clipboard_get, clipboard_set, scroll, drag, wait, note)", st.Action)
	}
}
