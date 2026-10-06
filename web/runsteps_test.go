package main

import (
	"bytes"
	"encoding/json"
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
	calls   []string       // "method {params}"
	visible map[string]int // selector → how many element_exists polls before it shows
	failOn  string         // method that fails
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

// The MCP registration's settings reach the daemon through the bridge's call.
func TestCallCarriesTheBridgeSettings(t *testing.T) {
	defer setClientEnv(nil)
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()

	t.Setenv("AGLINK_WEB_CDP_PORTS", "") // the daemon's own environment says nothing
	body, _ := json.Marshal(callRequest{Method: "list_tabs", Env: map[string]string{"AGLINK_WEB_CDP_PORTS": "9555"}})
	resp, err := http.Post(srv.URL+"/call", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if p := cdpPorts(); len(p) != 1 || p[0] != 9555 {
		t.Fatalf("cdpPorts = %v, want the forwarded 9555", p)
	}
	// The next call without it drops it again.
	body, _ = json.Marshal(callRequest{Method: "list_tabs"})
	resp, _ = http.Post(srv.URL+"/call", "application/json", bytes.NewReader(body))
	resp.Body.Close()
	if p := cdpPorts(); len(p) == 1 && p[0] == 9555 {
		t.Fatal("a setting removed from the registration must stop applying")
	}
}
