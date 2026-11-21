package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGatedHitsLooksInsideBatches(t *testing.T) {
	env := map[string]string{"AGLINK_APPROVAL_URL": "http://x/approve", "AGLINK_APPROVAL_TOOLS": "eval, upload_file"}
	if h := gatedHits("eval", nil, env); strings.Join(h, ",") != "eval" {
		t.Fatalf("direct: %v", h)
	}
	if h := gatedHits("click", nil, env); h != nil {
		t.Fatalf("not gated: %v", h)
	}
	steps := `[{"tool":"click","selector":"#a"},{"tool":"upload_file","selector":"#f","paths":"a"},{"tool":"eval","expression":"1"}]`
	if h := gatedHits("run_steps", map[string]any{"steps": steps}, env); strings.Join(h, ",") != "eval,upload_file" {
		t.Fatalf("batch: %v", h)
	}
	if h := gatedHits("eval", nil, map[string]string{"AGLINK_APPROVAL_TOOLS": "eval"}); h != nil {
		t.Fatalf("no approval URL: no gate: %v", h)
	}
}

func TestCallWaitsForApproval(t *testing.T) {
	var asked atomic.Int32
	var answer atomic.Bool
	approver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"approved": answer.Load(), "reason": "사용자가 거부했습니다"})
	}))
	defer approver.Close()

	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()
	call := func(env map[string]string) CallResult {
		body, _ := json.Marshal(callRequest{Method: "list_flows", Env: env})
		resp, err := http.Post(srv.URL+"/call", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out CallResult
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	gate := map[string]string{"AGLINK_APPROVAL_URL": approver.URL, "AGLINK_APPROVAL_TOOLS": "list_flows"}

	answer.Store(false)
	if r := call(gate); r.OK || !strings.Contains(r.Error, "not approved — 사용자가 거부했습니다") {
		t.Fatalf("denied: %+v", r)
	}
	answer.Store(true)
	t.Setenv("AGLINK_HOME", t.TempDir())
	if r := call(gate); !r.OK {
		t.Fatalf("approved: %+v", r)
	}
	// A call that carries no gate (a user's own !flow) is not asked — and
	// does not switch the gate off for the next gated call.
	if r := call(nil); !r.OK {
		t.Fatalf("ungated: %+v", r)
	}
	answer.Store(false)
	if r := call(gate); r.OK {
		t.Fatalf("the gate must still hold after an ungated call: %+v", r)
	}
	if n := asked.Load(); n != 3 {
		t.Fatalf("asked %d times, want 3", n)
	}
}

// The host gets the call's conversation and target with the request, for its
// "이 대화에서 허용" / "이 대상만 항상 허용" scopes.
func TestApprovalRequestCarriesSessionAndTarget(t *testing.T) {
	got := make(chan map[string]string, 1)
	approver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]string
		_ = json.NewDecoder(r.Body).Decode(&m)
		got <- m
		_ = json.NewEncoder(w).Encode(map[string]any{"approved": false, "reason": "no"})
	}))
	defer approver.Close()
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()
	body, _ := json.Marshal(callRequest{Method: "vscode_terminal_run", Params: map[string]any{"command": "git status"}, Session: "owner:chat:1/conv:ab",
		Env: map[string]string{"AGLINK_APPROVAL_URL": approver.URL, "AGLINK_APPROVAL_TOOLS": "vscode_terminal_run"}})
	resp, err := http.Post(srv.URL+"/call", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	m := <-got
	if m["session"] != "chat:1/conv:ab" || m["target"] != "git status" || m["tool"] != "vscode_terminal_run" {
		t.Fatalf("approval request: %v", m)
	}
	if tg := approvalTarget([]string{"eval", "upload_file"}, "run_steps", map[string]any{"url": "https://x"}); tg != "" {
		t.Fatalf("batch target: %q", tg)
	}
	if tg := approvalTarget([]string{"navigate"}, "navigate", map[string]any{"url": "https://example.com/a"}); tg != "https://example.com/a" {
		t.Fatalf("url target: %q", tg)
	}
}
