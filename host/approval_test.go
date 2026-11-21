package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeUI is a connected desktop/web control client: it receives the approval
// events the host broadcasts.
func fakeUI(t *testing.T) chan controlOut {
	t.Helper()
	ch := &remoteChatChannel{send: make(chan controlOut, 16), cancel: func() {}}
	addControlClient(ch)
	t.Cleanup(func() { removeControlClient(ch) })
	return ch.send
}

// nextApproval waits for the next {kind:"approval"} event and returns its id.
func nextApproval(t *testing.T, ui chan controlOut) map[string]any {
	t.Helper()
	for {
		select {
		case o := <-ui:
			if o.Kind != "approval" {
				continue
			}
			var m map[string]any
			_ = json.Unmarshal(o.Data, &m)
			return m
		case <-time.After(3 * time.Second):
			t.Fatal("no approval prompt reached the UI")
			return nil
		}
	}
}

func postApprove(t *testing.T, url string) chan map[string]any {
	t.Helper()
	out := make(chan map[string]any, 1)
	go func() {
		body, _ := json.Marshal(map[string]string{"source": "aglink-web", "tool": "eval", "summary": "eval\n{\"expression\":\"rm -rf\"}"})
		resp, err := http.Post(url+"/approve", "application/json", bytes.NewReader(body))
		if err != nil {
			out <- map[string]any{"err": err.Error()}
			return
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		out <- m
	}()
	return out
}

func TestApprovalApprovedAndDenied(t *testing.T) {
	ui := fakeUI(t)
	b := &Bot{cfgh: NewConfigHolder(&Config{AllowedUserIDs: []int64{1}, ApprovalTimeoutSec: 30, ApprovalTools: []string{"eval"}})}
	srv := httptest.NewServer(http.HandlerFunc(b.handleApproveHTTP))
	defer srv.Close()

	// Approved by id, as the desktop's [승인] button does.
	ans := postApprove(t, srv.URL)
	ev := nextApproval(t, ui)
	if ev["tool"] != "eval" || ev["source"] != "aglink-web" || !strings.Contains(ev["summary"].(string), "rm -rf") {
		t.Fatalf("prompt: %v", ev)
	}
	if req, msg := resolveApproval(ev["id"].(string), true); req == nil {
		t.Fatal(msg)
	}
	if a := <-ans; a["approved"] != true {
		t.Fatalf("approved: %v", a)
	}

	// Denied with a bare "!no" — the only one pending.
	ans = postApprove(t, srv.URL)
	nextApproval(t, ui)
	b.handleApprovalCommand(&recordingReply{}, 1, []string{"!no"}, false)
	if a := <-ans; a["approved"] != false || !strings.Contains(a["reason"].(string), "거부") {
		t.Fatalf("denied: %v", a)
	}

	// Every prompt is followed by approval_done, so the UI closes its dialog
	// even when the answer came from elsewhere.
	done := false
	for len(ui) > 0 {
		if o := <-ui; o.Kind == "approval_done" {
			done = true
		}
	}
	if !done {
		t.Fatal("no approval_done reached the UI")
	}
}

func TestApprovalTimesOut(t *testing.T) {
	fakeUI(t)
	b := &Bot{cfgh: NewConfigHolder(&Config{AllowedUserIDs: []int64{1}, ApprovalTools: []string{"eval"}})}
	ok, reason := b.askApproval("aglink-screen", "launch_app", "launch_app\n{}", 150*time.Millisecond)
	if ok || !strings.Contains(reason, "승인되지 않아") {
		t.Fatalf("timeout: %v %q", ok, reason)
	}
}

func TestApprovalWithNoChannelIsRefused(t *testing.T) {
	b := &Bot{cfgh: NewConfigHolder(&Config{ApprovalTools: []string{"eval"}})} // no Telegram, no UI connected
	if ok, reason := b.askApproval("aglink-web", "eval", "", time.Second); ok || !strings.Contains(reason, "채널이 없어") {
		t.Fatalf("got %v %q", ok, reason)
	}
}

func TestResolveApprovalNeedsAnIDWhenSeveralWait(t *testing.T) {
	approvals.Lock()
	approvals.pending["aaa"] = &approvalRequest{ID: "aaa", Tool: "eval", done: make(chan bool, 1)}
	approvals.pending["bbb"] = &approvalRequest{ID: "bbb", Tool: "launch_app", done: make(chan bool, 1)}
	approvals.Unlock()
	defer func() {
		approvals.Lock()
		delete(approvals.pending, "aaa")
		delete(approvals.pending, "bbb")
		approvals.Unlock()
	}()
	if req, msg := resolveApproval("", true); req != nil || !strings.Contains(msg, "#aaa eval") {
		t.Fatalf("ambiguous: %v %q", req, msg)
	}
	if req, _ := resolveApproval("#BBB", false); req == nil || req.ID != "bbb" {
		t.Fatalf("by id: %v", req)
	}
}

func TestApprovalEnvReachesTheWorkerMCPConfig(t *testing.T) {
	cfg := &Config{ApprovalTools: []string{"eval", "launch_app"}}
	env := approvalEnv(cfg)
	if env["AGLINK_APPROVAL_TOOLS"] != "eval,launch_app" || !strings.HasSuffix(env["AGLINK_APPROVAL_URL"], "/approve") {
		t.Fatalf("env: %v", env)
	}
	if approvalEnv(&Config{}) != nil {
		t.Fatal("no tools listed: no approval env")
	}
	if got := splitToolList(" eval, vscode_terminal_run;eval\nlaunch_app "); strings.Join(got, ",") != "eval,vscode_terminal_run,launch_app" {
		t.Fatalf("splitToolList: %v", got)
	}
}

// "항상 허용" approves this call, drops the tool from approvals.tools in
// config.yaml, and a later call for it (from a worker still holding the old
// tool list) runs without asking.
func TestApprovalAllowAlways(t *testing.T) {
	ui := fakeUI(t)
	dir := t.TempDir()
	path := dir + "/config.yaml"
	old := approvalConfigPath
	approvalConfigPath = path
	t.Cleanup(func() { approvalConfigPath = old })
	cfg := &Config{TelegramBotToken: "1:test", AllowedUserIDs: []int64{1}, ApprovalTimeoutSec: 30, ApprovalTools: []string{"eval", "launch_app"}}
	b := &Bot{cfgh: NewConfigHolder(cfg)}
	srv := httptest.NewServer(http.HandlerFunc(b.handleApproveHTTP))
	defer srv.Close()

	ans := postApprove(t, srv.URL)
	ev := nextApproval(t, ui)
	if req, msg := resolveApprovalAlways(ev["id"].(string), true, true); req == nil {
		t.Fatal(msg)
	}
	if a := <-ans; a["approved"] != true {
		t.Fatalf("approved: %v", a)
	}
	saved, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.ApprovalTools) != 1 || saved.ApprovalTools[0] != "launch_app" {
		t.Fatalf("approvals.tools after always = %v, want [launch_app]", saved.ApprovalTools)
	}

	// The live config now has eval allowed: no prompt, approved at once.
	b.cfgh = NewConfigHolder(saved)
	select {
	case a := <-postApprove(t, srv.URL):
		if a["approved"] != true {
			t.Fatalf("second call: %v", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("allowed tool still waited for approval")
	}
}

func TestStillGated(t *testing.T) {
	cfg := &Config{ApprovalTools: []string{"eval"}}
	if !stillGated(cfg, "eval") || !stillGated(cfg, "click,eval") || stillGated(cfg, "launch_app") || stillGated(&Config{}, "eval") {
		t.Fatal("stillGated")
	}
}
