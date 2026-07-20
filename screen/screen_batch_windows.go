//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"strings"
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
func runSequence(stepsJSON string) ([]seqStepResult, error) {
	var steps []seqStep
	if err := json.Unmarshal([]byte(stepsJSON), &steps); err != nil {
		return nil, fmt.Errorf("invalid steps JSON: %w", err)
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("steps is empty")
	}
	results := make([]seqStepResult, 0, len(steps))
	for i, st := range steps {
		detail, err := runOneStep(st)
		if err != nil {
			results = append(results, seqStepResult{Index: i, Action: st.Action, OK: false, Detail: err.Error()})
			return results, fmt.Errorf("step %d (%s) failed: %w", i, st.Action, err)
		}
		results = append(results, seqStepResult{Index: i, Action: st.Action, OK: true, Detail: detail})
	}
	return results, nil
}

// runOneStep dispatches a single step to the same function its standalone
// MCP tool calls.
func runOneStep(st seqStep) (string, error) {
	switch st.Action {
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
		if err := uiaInvoke(st.Name); err != nil {
			return "", err
		}
		return fmt.Sprintf("invoked %q", st.Name), nil
	case "set_value":
		if err := uiaSetValue(st.Name, st.Text); err != nil {
			return "", err
		}
		return fmt.Sprintf("set %q = %q", st.Name, st.Text), nil
	case "click_control":
		return clickControl(st.Window, st.Text, st.Nth)
	case "wait_for_control":
		return uiaWaitForControl(st.Name, st.TimeoutMs)
	case "wait_for_window":
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
		if err := mouseDrag(st.X, st.Y, st.X2, st.Y2, button); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s-dragged (%d,%d) -> (%d,%d)", button, st.X, st.Y, st.X2, st.Y2), nil
	default:
		return "", fmt.Errorf("unknown action %q (supported: click, double_click, triple_click, type, key, invoke, set_value, click_control, wait_for_control, wait_for_window, scroll, drag)", st.Action)
	}
}
