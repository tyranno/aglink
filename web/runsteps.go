package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// run_steps runs a sequence of the other tools in one call.
//
// Most of the time in driving a UI step by step is not the steps — a click is
// milliseconds — but the model's round trip between them. When the next few
// steps are already known (open settings, fill the form, press save, wait for
// "saved"), doing them here removes every one of those round trips. It runs
// through d.call, the same path each tool takes on its own, so a step behaves
// exactly as that tool does: in a Chrome tab or an Electron/Wails window
// alike, with the same dialog and certificate-warning handling.
//
// It deliberately does not branch. It stops at the first failed step, says how
// far it got, and shows the page as it was then, so the caller decides what to
// do next instead of the batch guessing.

const (
	maxRunSteps        = 100
	maxStepWait        = 60 * time.Second
	defaultExpectWait  = 5 * time.Second
	expectPoll         = 150 * time.Millisecond
	stepDetailMax      = 300 // chars of each step's own output kept in the report
	stepDetailFullMax  = 20000
	snapshotTextChars  = 4000
	snapshotElementMax = 80
)

type runStep struct {
	Tool   string
	Args   map[string]any
	WaitMs int  // pause after this step
	Full   bool // keep this step's whole output instead of a one-line summary
}

// Step keys that steer run_steps itself rather than being passed to the tool.
var stepControlKeys = map[string]bool{"tool": true, "wait_ms": true, "full": true}

// parseSteps accepts the steps as a JSON string (how MCP clients pass them) or
// an already-decoded array (a /call body).
func parseSteps(v any) ([]runStep, error) {
	var raw []map[string]any
	switch s := v.(type) {
	case string:
		if strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("run_steps requires 'steps'")
		}
		if err := json.Unmarshal([]byte(s), &raw); err != nil {
			return nil, fmt.Errorf("steps must be a JSON array of objects: %v", err)
		}
	case []any:
		for i, e := range s {
			m, ok := e.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("step %d is not an object", i+1)
			}
			raw = append(raw, m)
		}
	case nil:
		return nil, fmt.Errorf("run_steps requires 'steps'")
	default:
		return nil, fmt.Errorf("steps must be a JSON array of objects")
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("steps is empty")
	}
	if len(raw) > maxRunSteps {
		return nil, fmt.Errorf("too many steps (%d, max %d) — split the batch", len(raw), maxRunSteps)
	}
	steps := make([]runStep, 0, len(raw))
	for i, m := range raw {
		tool, _ := m["tool"].(string)
		tool = strings.TrimSpace(tool)
		if tool == "" {
			return nil, fmt.Errorf("step %d has no \"tool\"", i+1)
		}
		if tool == "run_steps" {
			return nil, fmt.Errorf("step %d: run_steps cannot be nested", i+1)
		}
		st := runStep{Tool: tool, Args: map[string]any{}, WaitMs: intp(m, "wait_ms", 0)}
		st.Full, _ = m["full"].(bool)
		for k, v := range m {
			if !stepControlKeys[k] {
				st.Args[k] = v
			}
		}
		steps = append(steps, st)
	}
	return steps, nil
}

var (
	navigatedTabRE = regexp.MustCompile(`^ok: navigated tab (\d+)`)
	activatedTabRE = regexp.MustCompile(`^ok: activated tab (\d+)`)
)

// runSteps executes the batch. params: steps (required), tabId, snapshot
// ("text" | "elements" | "none"), step_delay_ms, continue_on_error.
func (d *Daemon) runSteps(params map[string]any, profile string) CallResult {
	steps, err := parseSteps(params["steps"])
	if err != nil {
		return CallResult{Error: err.Error()}
	}
	snapshot := strings.ToLower(strings.TrimSpace(str(params, "snapshot")))
	if snapshot != "" && snapshot != "none" && snapshot != "text" && snapshot != "elements" {
		return CallResult{Error: fmt.Sprintf("snapshot must be text, elements or none, not %q", snapshot)}
	}
	keepGoing := isTrue(str(params, "continue_on_error"))
	delay := time.Duration(intp(params, "step_delay_ms", defaultStepDelayMs())) * time.Millisecond
	tab := intp(params, "tabId", 0)

	var lines []string
	failed := 0
	stoppedAt := -1
	for i, st := range steps {
		if i > 0 && delay > 0 {
			time.Sleep(capWait(delay))
		}
		// Steps follow the tab the batch is on: a navigate that opened a new
		// tab, or an activate_tab, moves the rest of the batch with it.
		if tab != 0 {
			if _, set := st.Args["tabId"]; !set {
				st.Args["tabId"] = tab
			}
		}
		text, serr := d.runOneStep(st, profile)
		if serr != nil {
			failed++
			lines = append(lines, fmt.Sprintf("%d %s ✗ %s", i+1, st.Tool, oneLine(serr.Error(), stepDetailMax)))
			if !keepGoing {
				stoppedAt = i
				break
			}
		} else {
			if m := navigatedTabRE.FindStringSubmatch(text); m != nil {
				tab, _ = strconv.Atoi(m[1])
			} else if m := activatedTabRE.FindStringSubmatch(text); m != nil {
				tab, _ = strconv.Atoi(m[1])
			}
			detail := oneLine(text, stepDetailMax)
			if st.Full {
				detail = clip(text, stepDetailFullMax)
			}
			lines = append(lines, fmt.Sprintf("%d %s ✓ %s", i+1, st.Tool, detail))
		}
		if st.WaitMs > 0 {
			time.Sleep(capWait(time.Duration(st.WaitMs) * time.Millisecond))
		}
	}

	var head string
	switch {
	case stoppedAt >= 0:
		head = fmt.Sprintf("run_steps stopped at step %d/%d (%s) — the steps after it did not run", stoppedAt+1, len(steps), steps[stoppedAt].Tool)
	case failed > 0:
		head = fmt.Sprintf("run_steps: %d/%d steps ok, %d failed", len(steps)-failed, len(steps), failed)
	default:
		head = fmt.Sprintf("ok: %d/%d steps", len(steps), len(steps))
	}
	out := head + "\n" + strings.Join(lines, "\n")
	if snap := d.stepsSnapshot(snapshot, tab, profile); snap != "" {
		out += "\n" + snap
	}
	if stoppedAt >= 0 {
		return CallResult{Error: out}
	}
	return CallResult{OK: true, Text: out}
}

// runOneStep runs one step: one of the built-in step kinds, or any tool.
func (d *Daemon) runOneStep(st runStep, profile string) (string, error) {
	switch st.Tool {
	case "wait":
		ms := intp(st.Args, "ms", 0)
		if ms <= 0 {
			return "", fmt.Errorf("wait requires a positive \"ms\"")
		}
		w := capWait(time.Duration(ms) * time.Millisecond)
		time.Sleep(w)
		return fmt.Sprintf("waited %dms", w.Milliseconds()), nil
	case "expect":
		return d.expectStep(st.Args, profile)
	}
	res := d.call(st.Tool, st.Args, profile)
	if !res.OK {
		return "", fmt.Errorf("%s", res.Error)
	}
	return res.Text, nil
}

// expectStep waits until a condition holds, failing the batch when it does not
// within timeout_ms. {selector} or {text} must become visible; with
// "gone": true it must disappear instead (a spinner, a closing dialog).
func (d *Daemon) expectStep(args map[string]any, profile string) (string, error) {
	sel := str(args, "selector")
	if sel == "" {
		if t := str(args, "text"); t != "" {
			sel = "text=" + t
		}
	}
	if sel == "" {
		return "", fmt.Errorf("expect requires \"selector\" or \"text\"")
	}
	gone := false
	switch v := args["gone"].(type) {
	case bool:
		gone = v
	case string:
		gone = isTrue(v)
	}
	timeout := defaultExpectWait
	if ms := intp(args, "timeout_ms", 0); ms > 0 {
		timeout = capWait(time.Duration(ms) * time.Millisecond)
	}
	q := map[string]any{"selector": sel}
	if tab, ok := args["tabId"]; ok {
		q["tabId"] = tab
	}

	start := time.Now()
	for {
		res := d.call("element_exists", q, profile)
		if !res.OK {
			return "", fmt.Errorf("expect %s: %s", sel, res.Error)
		}
		var st struct {
			Exists  bool `json:"exists"`
			Visible bool `json:"visible"`
		}
		_ = json.Unmarshal([]byte(res.Text), &st)
		shown := st.Exists && st.Visible
		if shown != gone {
			waited := time.Since(start).Round(time.Millisecond)
			if gone {
				return fmt.Sprintf("%s is gone (%s)", sel, waited), nil
			}
			return fmt.Sprintf("%s is visible (%s)", sel, waited), nil
		}
		if time.Since(start) >= timeout {
			if gone {
				return "", fmt.Errorf("expect: %s still visible after %s", sel, timeout)
			}
			return "", fmt.Errorf("expect: %s not visible after %s", sel, timeout)
		}
		time.Sleep(expectPoll)
	}
}

// stepsSnapshot shows the page as the batch left it, so the caller sees the
// outcome without another call.
func (d *Daemon) stepsSnapshot(kind string, tab int, profile string) string {
	q := map[string]any{}
	if tab != 0 {
		q["tabId"] = tab
	}
	switch kind {
	case "text":
		q["offset"] = -snapshotTextChars
		res := d.call("get_page_text", q, profile)
		if !res.OK {
			return "--- snapshot (text) unavailable: " + oneLine(res.Error, stepDetailMax)
		}
		return "--- snapshot (text, last " + strconv.Itoa(snapshotTextChars) + " chars) ---\n" + res.Text
	case "elements":
		q["max"] = snapshotElementMax
		res := d.call("list_elements", q, profile)
		if !res.OK {
			return "--- snapshot (elements) unavailable: " + oneLine(res.Error, stepDetailMax)
		}
		return "--- snapshot (elements) ---\n" + res.Text
	}
	return ""
}

func defaultStepDelayMs() int {
	n, _ := strconv.Atoi(strings.TrimSpace(webGetenv("AGLINK_WEB_STEP_DELAY_MS")))
	if n < 0 {
		return 0
	}
	return n
}

func capWait(d time.Duration) time.Duration {
	if d > maxStepWait {
		return maxStepWait
	}
	return d
}

func isTrue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "1", "on":
		return true
	}
	return false
}

// oneLine flattens a tool's output to one short line for the step report.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	return clip(s, max)
}

func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
