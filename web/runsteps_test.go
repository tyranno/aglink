package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

// stepsExtension is a fake Chrome extension for run_steps: it records every
// call and answers from a small script.
type stepsExtension struct {
	mu      sync.Mutex
	calls   []string          // "method {params}"
	visible map[string]int    // selector → how many element_exists polls before it shows
	failOn  string            // method that fails
	replies map[string]string // method → reply text, overriding the defaults
}

func (x *stepsExtension) run(conn *websocket.Conn) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req Request
		if json.Unmarshal(data, &req) != nil || req.Method == pingMethod {
			continue
		}
		p, _ := json.Marshal(req.Params)
		x.mu.Lock()
		x.calls = append(x.calls, req.Method+" "+string(p))
		reply := Reply{ID: req.ID, OK: true}
		switch {
		case req.Method == x.failOn:
			reply.OK, reply.Error = false, "no element matched selector: #missing"
		case x.replies[req.Method] != "":
			reply.Text = x.replies[req.Method]
		case req.Method == "navigate":
			reply.Text = "ok: navigated tab 42 — 설정 — https://app.example/settings"
		case req.Method == "element_exists":
			sel, _ := req.Params["selector"].(string)
			n, known := x.visible[sel]
			if known && n <= 0 {
				reply.Text = `{"exists":true,"visible":true,"count":1}`
			} else {
				if known {
					x.visible[sel] = n - 1
				}
				reply.Text = `{"exists":false,"visible":false,"count":0}`
			}
		case req.Method == "get_page_text":
			reply.Text = "저장됨\n[cursor:3]"
		default:
			reply.Text = "ok: " + req.Method
		}
		x.mu.Unlock()
		b, _ := json.Marshal(reply)
		conn.WriteMessage(websocket.TextMessage, b)
	}
}

func (x *stepsExtension) methods() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	out := make([]string, len(x.calls))
	for i, c := range x.calls {
		out[i] = strings.SplitN(c, " ", 2)[0]
	}
	return out
}

func newStepsDaemon(t *testing.T, x *stepsExtension) *Daemon {
	t.Helper()
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	t.Cleanup(srv.Close)
	conn := dialFakeExtension(t, srv, "a@b.com")
	t.Cleanup(func() { conn.Close() })
	go x.run(conn)
	waitForProfile(t, d, "a@b.com")
	return d
}

func TestRunStepsRunsTheBatchAndAppendsTheSnapshot(t *testing.T) {
	x := &stepsExtension{visible: map[string]int{"role=dialog": 2}}
	d := newStepsDaemon(t, x)
	res := d.call("run_steps", map[string]any{
		"steps": `[{"tool":"click","selector":"text=설정"},
		           {"tool":"expect","selector":"role=dialog"},
		           {"tool":"type","selector":"label=이름","text":"홍길동"}]`,
		"snapshot": "text",
	}, "")
	if !res.OK {
		t.Fatalf("got %+v", res)
	}
	for _, want := range []string{"ok: 3/3 steps", "1 click ✓ ok: click", "2 expect ✓ role=dialog is visible", "3 type ✓", "--- snapshot (text", "저장됨"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("missing %q in:\n%s", want, res.Text)
		}
	}
	// expect polled until the dialog showed (2 misses, then a hit).
	if n := strings.Count(strings.Join(x.methods(), " "), "element_exists"); n != 3 {
		t.Fatalf("element_exists polled %d times, want 3", n)
	}
}

func TestRunStepsStopsAtTheFirstFailure(t *testing.T) {
	x := &stepsExtension{failOn: "type"}
	d := newStepsDaemon(t, x)
	res := d.call("run_steps", map[string]any{
		"steps": `[{"tool":"click","selector":"#a"},{"tool":"type","selector":"#missing","text":"x"},{"tool":"click","selector":"#never"}]`,
	}, "")
	if res.OK || !strings.Contains(res.Error, "stopped at step 2/3 (type)") || !strings.Contains(res.Error, "2 type ✗ no element matched selector: #missing") {
		t.Fatalf("got %+v", res)
	}
	if got := strings.Join(x.methods(), ","); got != "click,type" {
		t.Fatalf("steps after the failure must not run: %s", got)
	}
}

func TestRunStepsContinueOnError(t *testing.T) {
	x := &stepsExtension{failOn: "type"}
	d := newStepsDaemon(t, x)
	res := d.call("run_steps", map[string]any{
		"steps":             `[{"tool":"type","selector":"#missing","text":"x"},{"tool":"click","selector":"#b"}]`,
		"continue_on_error": "true",
	}, "")
	if !res.OK || !strings.Contains(res.Text, "1/2 steps ok, 1 failed") {
		t.Fatalf("got %+v", res)
	}
}

func TestRunStepsFollowsTheTabANavigateOpened(t *testing.T) {
	x := &stepsExtension{}
	d := newStepsDaemon(t, x)
	res := d.call("run_steps", map[string]any{
		"steps": `[{"tool":"navigate","url":"https://app.example/settings"},{"tool":"click","selector":"#save"}]`,
	}, "")
	if !res.OK {
		t.Fatalf("got %+v", res)
	}
	x.mu.Lock()
	last := x.calls[len(x.calls)-1]
	x.mu.Unlock()
	if !strings.Contains(last, `"tabId":42`) {
		t.Fatalf("the click must go to the tab navigate opened: %s", last)
	}
}

// A click that opened a tab moves the batch there only when the step asks.
func TestRunStepsFollowsAPopupWhenAsked(t *testing.T) {
	x := &stepsExtension{replies: map[string]string{"click": "ok: left-clicked <a> \"열기\"\nopened new tab id=77 url=https://pay.example/ — pass tabId=77 to work in it"}}
	d := newStepsDaemon(t, x)
	res := d.call("run_steps", map[string]any{
		"steps": `[{"tool":"click","selector":"#open","follow_popup":true},{"tool":"get_value","selector":"#amount"}]`,
		"tabId": 5,
	}, "")
	if !res.OK || !strings.Contains(res.Text, "→ following tab 77") {
		t.Fatalf("got %+v", res)
	}
	x.mu.Lock()
	last := x.calls[len(x.calls)-1]
	x.mu.Unlock()
	if !strings.Contains(last, `"tabId":77`) {
		t.Fatalf("the next step must run in the new tab: %s", last)
	}
	if strings.Contains(last, "follow_popup") {
		t.Fatalf("follow_popup steers run_steps and is not passed to the tool: %s", last)
	}

	// Without follow_popup the batch stays, and says how to follow.
	res = d.call("run_steps", map[string]any{
		"steps": `[{"tool":"click","selector":"#open"},{"tool":"get_value","selector":"#amount"}]`,
		"tabId": 5,
	}, "")
	if !res.OK || !strings.Contains(res.Text, `add "follow_popup": true`) {
		t.Fatalf("got %+v", res)
	}
	x.mu.Lock()
	last = x.calls[len(x.calls)-1]
	x.mu.Unlock()
	if !strings.Contains(last, `"tabId":5`) {
		t.Fatalf("without follow_popup the batch keeps its tab: %s", last)
	}
}

// get_network_requests' own "full" reaches the tool; elsewhere "full" keeps
// the step's whole output.
func TestParseStepsPassesFullToNetworkRequests(t *testing.T) {
	steps, err := parseSteps(`[{"tool":"get_network_requests","full":"true"},{"tool":"get_page_text","full":true}]`)
	if err != nil {
		t.Fatal(err)
	}
	if steps[0].Args["full"] != "true" || steps[0].Full {
		t.Fatalf("get_network_requests full must go to the tool: %+v", steps[0])
	}
	if _, passed := steps[1].Args["full"]; passed || !steps[1].Full {
		t.Fatalf("other tools: full steers the report: %+v", steps[1])
	}
}

func TestRunStepsExpectGoneTimesOut(t *testing.T) {
	x := &stepsExtension{visible: map[string]int{".spinner": 0}}
	d := newStepsDaemon(t, x)
	res := d.call("run_steps", map[string]any{
		"steps": `[{"tool":"expect","selector":".spinner","gone":true,"timeout_ms":300}]`,
	}, "")
	if res.OK || !strings.Contains(res.Error, "expect: .spinner still visible after 300ms") {
		t.Fatalf("got %+v", res)
	}
}

func TestParseStepsRejectsBadBatches(t *testing.T) {
	for in, want := range map[string]string{
		``:                       "requires 'steps'",
		`[]`:                     "steps is empty",
		`[{"selector":"#a"}]`:    `step 1 has no "tool"`,
		`[{"tool":"run_steps"}]`: "cannot be nested",
		`{"tool":"click"}`:       "JSON array",
	} {
		if _, err := parseSteps(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("parseSteps(%q) = %v, want %q", in, err, want)
		}
	}
	// A /call body may carry the steps already decoded.
	steps, err := parseSteps([]any{map[string]any{"tool": "click", "selector": "#a", "wait_ms": float64(200)}})
	if err != nil || len(steps) != 1 || steps[0].WaitMs != 200 || steps[0].Args["selector"] != "#a" {
		t.Fatalf("decoded steps: %+v %v", steps, err)
	}
	if _, has := steps[0].Args["wait_ms"]; has {
		t.Fatal("wait_ms steers run_steps and must not reach the tool")
	}
}

// The MCP registration's settings reach the daemon through the bridge's call,
// and stay that call's own: a call without them does not inherit them, and
// two sessions with different values calling at once each see their own.
func TestCallCarriesTheBridgeSettings(t *testing.T) {
	d := newDaemon("")
	var mu sync.Mutex
	seen := map[string]bool{}
	d.discover = func(ctx context.Context) []appInfo {
		mu.Lock()
		seen[fmt.Sprint(cdpPorts(envFrom(ctx)))] = true
		mu.Unlock()
		return nil
	}
	srv := httptest.NewServer(d.handler())
	defer srv.Close()
	t.Setenv("AGLINK_WEB_CDP_PORTS", "") // the daemon's own environment says nothing

	post := func(env map[string]string) {
		body, _ := json.Marshal(callRequest{Method: listProfilesMethod, Env: env})
		resp, err := http.Post(srv.URL+"/call", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Error(err)
			return
		}
		resp.Body.Close()
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); post(map[string]string{"AGLINK_WEB_CDP_PORTS": "9555"}) }()
		go func() { defer wg.Done(); post(map[string]string{"AGLINK_WEB_CDP_PORTS": "9666"}) }()
	}
	wg.Wait()
	post(nil)
	want := map[string]bool{"[9555]": true, "[9666]": true, fmt.Sprint(cdpPorts(nil)): true}
	if len(seen) != len(want) {
		t.Fatalf("ports searched = %v, want exactly %v", seen, want)
	}
	for k := range want {
		if !seen[k] {
			t.Fatalf("ports searched = %v, missing %s", seen, k)
		}
	}
}

func TestCallEnvFallsBackToTheDaemonsOwn(t *testing.T) {
	t.Setenv("AGLINK_WEB_STEP_DELAY_MS", "70")
	if n := defaultStepDelayMs(nil); n != 70 {
		t.Fatalf("no call setting: %d, want the daemon's 70", n)
	}
	if n := defaultStepDelayMs(callEnv{"AGLINK_WEB_STEP_DELAY_MS": "5"}); n != 5 {
		t.Fatalf("call setting: %d, want 5", n)
	}
}

func TestFlowSaveAndReplay(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir())
	x := &stepsExtension{visible: map[string]int{"#done": 0}}
	d := newStepsDaemon(t, x)

	res := d.call("run_steps", map[string]any{
		"steps":   `[{"tool":"click","selector":"#open"},{"tool":"expect","selector":"#done"}]`,
		"save_as": "open-settings",
	}, "a@b.com")
	if !res.OK || !strings.Contains(res.Text, "saved as flow open-settings") {
		t.Fatalf("save: %+v", res)
	}
	if l := d.call("list_flows", nil, ""); !l.OK || !strings.Contains(l.Text, "open-settings | 2 steps | a@b.com") {
		t.Fatalf("list: %+v", l)
	}

	before := len(x.methods())
	res = d.call("run_flow", map[string]any{"name": "open-settings"}, "")
	if !res.OK || !strings.HasPrefix(res.Text, "flow open-settings: ok: 2/2 steps") {
		t.Fatalf("replay: %+v", res)
	}
	if got := strings.Join(x.methods()[before:], ","); got != "click,element_exists" {
		t.Fatalf("replayed calls = %s", got)
	}
}

func TestFlowNotSavedWhenAStepFails(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir())
	x := &stepsExtension{failOn: "type"}
	d := newStepsDaemon(t, x)
	res := d.call("run_steps", map[string]any{
		"steps":             `[{"tool":"type","selector":"#x","text":"a"}]`,
		"save_as":           "broken",
		"continue_on_error": "true",
	}, "")
	if !strings.Contains(res.Text, "not saved as broken") {
		t.Fatalf("got %+v", res)
	}
	if r := d.call("run_flow", map[string]any{"name": "broken"}, ""); r.OK || !strings.Contains(r.Error, "no flow named") {
		t.Fatalf("a failed batch must not be saved: %+v", r)
	}
}

func TestFlowNamesAreChecked(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir())
	for _, bad := range []string{"../evil", "a/b", "", strings.Repeat("x", 65)} {
		if _, err := flowPath(bad); err == nil {
			t.Errorf("accepted flow name %q", bad)
		}
	}
	if _, err := flowPath("로그인-staging_1.v2"); err != nil {
		t.Errorf("rejected a normal name: %v", err)
	}
}
