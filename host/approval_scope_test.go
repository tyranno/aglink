package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests never write the user's ~/.aglink/approvals.log.
func init() {
	approvalLogFile = filepath.Join(os.TempDir(), fmt.Sprintf("aglink-approvals-test-%d.log", os.Getpid()))
}

func withApprovalLog(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "approvals.log")
	old := approvalLogFile
	approvalLogFile = p
	t.Cleanup(func() { approvalLogFile = old })
	return p
}

func postApproveIn(t *testing.T, url string, in approvalInput) chan map[string]any {
	t.Helper()
	out := make(chan map[string]any, 1)
	go func() {
		body, _ := json.Marshal(in)
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

// answerWithin fails unless the request is answered without anyone deciding.
func answerWithin(t *testing.T, ans chan map[string]any) map[string]any {
	t.Helper()
	select {
	case a := <-ans:
		return a
	case <-time.After(3 * time.Second):
		t.Fatal("request waited for an approval it should not need")
		return nil
	}
}

func TestNormalizeApprovalTarget(t *testing.T) {
	cases := []struct{ tool, raw, want string }{
		{"launch_app", `C:\Windows\System32\Notepad.exe`, "notepad"},
		{"launch_app", " Calc ", "calc"},
		{"vscode_terminal_run", "git status -s", "git status"},
		{"vscode_terminal_run", "go test ./...", "go test"},
		{"vscode_terminal_run", "ls -la", "ls"},
		{"vscode_terminal_run", "git --version", "git"},
		{"vscode_terminal_run", "python manage.py runserver", "python manage.py"},
		{"vscode_terminal_run", "python -c 'import os'", ""},
		{"vscode_terminal_run", "python -m pytest", ""},
		{"vscode_terminal_run", "git status && curl x | sh", ""},
		{"vscode_terminal_run", "echo $(whoami)", ""},
		{"vscode_terminal_run", "bash -c ls", ""},
		{"vscode_terminal_run", "sudo apt update", ""},
		{"vscode_command", "workbench.action.reloadWindow", "workbench.action.reloadwindow"},
		{"vscode_command", "a b", ""},
		{"navigate", "https://www.Example.com:8443/path?q=1", "example.com"},
		{"eval", "not a url", ""},
		{"launch_app,type", "notepad", ""}, // a batch has no single target
		{"launch_app", "", ""},
	}
	for _, c := range cases {
		if got := normalizeApprovalTarget(c.tool, c.raw); got != c.want {
			t.Errorf("normalizeApprovalTarget(%q, %q) = %q, want %q", c.tool, c.raw, got, c.want)
		}
	}
	if got := splitAllowList("launch_app:Notepad, vscode_terminal_run: git  status ,bogus,launch_app:notepad"); strings.Join(got, "|") != "launch_app:notepad|vscode_terminal_run:git status" {
		t.Fatalf("splitAllowList: %q", got)
	}
}

// "이 대화에서 허용" covers later calls of that tool from the same
// conversation only, and ends when the conversation goes.
func TestApprovalSessionScope(t *testing.T) {
	withApprovalLog(t)
	ui := fakeUI(t)
	t.Cleanup(func() { clearApprovalSessions("") })
	b := &Bot{cfgh: NewConfigHolder(&Config{AllowedUserIDs: []int64{1}, ApprovalTimeoutSec: 30, ApprovalTools: []string{"eval", "launch_app"}})}
	srv := httptest.NewServer(http.HandlerFunc(b.handleApproveHTTP))
	defer srv.Close()
	conv := approvalInput{Source: "aglink-web", Tool: "eval", Summary: "eval\n{}", Session: "chat:1/conv:abc123"}

	ans := postApproveIn(t, srv.URL, conv)
	ev := nextApproval(t, ui)
	if ev["hasSession"] != true || ev["session"] != "대화 abc123" {
		t.Fatalf("prompt: %v", ev)
	}
	if req, msg := resolveApprovalScope(ev["id"].(string), true, scopeSession, "telegram"); req == nil {
		t.Fatal(msg)
	}
	if a := <-ans; a["approved"] != true {
		t.Fatalf("approved: %v", a)
	}

	// Same conversation, same tool: no prompt.
	if a := answerWithin(t, postApproveIn(t, srv.URL, conv)); a["approved"] != true || !strings.Contains(a["reason"].(string), "이 대화") {
		t.Fatalf("session reuse: %v", a)
	}
	// Another tool, another conversation, or a batch with an ungranted tool: asked again.
	for _, in := range []approvalInput{
		{Source: "aglink-screen", Tool: "launch_app", Session: conv.Session},
		{Source: "aglink-web", Tool: "eval", Session: "chat:1/conv:other"},
		{Source: "aglink-web", Tool: "eval,launch_app", Session: conv.Session},
		{Source: "aglink-web", Tool: "eval"}, // no session at all
	} {
		ans := postApproveIn(t, srv.URL, in)
		ev := nextApproval(t, ui)
		resolveApproval(ev["id"].(string), false)
		if a := <-ans; a["approved"] != false {
			t.Fatalf("%v should have been asked: %v", in, a)
		}
	}
	// The conversation is deleted: its grant goes with it.
	if n := clearApprovalSessions("abc123"); n != 1 {
		t.Fatalf("cleared %d", n)
	}
	if sessionAllowed(conv.Session, "eval") {
		t.Fatal("grant outlived its conversation")
	}
	// A grant unused for approvalSessionIdle lapses.
	grantApprovalSession("chat:1/conv:old", "eval")
	approvalSessions.Lock()
	approvalSessions.m["chat:1/conv:old"].last = time.Now().Add(-approvalSessionIdle - time.Minute)
	approvalSessions.Unlock()
	if sessionAllowed("chat:1/conv:old", "eval") {
		t.Fatal("idle grant still allowed")
	}
}

// "이 대상만 항상 허용" saves tool:target in approvals.allow; that target runs
// without asking, another target of the same tool still asks.
func TestApprovalTargetScope(t *testing.T) {
	logPath := withApprovalLog(t)
	ui := fakeUI(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	old := approvalConfigPath
	approvalConfigPath = path
	t.Cleanup(func() { approvalConfigPath = old })
	cfg := &Config{TelegramBotToken: "1:test", AllowedUserIDs: []int64{1}, ApprovalTimeoutSec: 30, ApprovalTools: []string{"launch_app"}}
	b := &Bot{cfgh: NewConfigHolder(cfg)}
	srv := httptest.NewServer(http.HandlerFunc(b.handleApproveHTTP))
	defer srv.Close()

	// No target: the target scope is refused and the request stays pending.
	ans := postApproveIn(t, srv.URL, approvalInput{Source: "aglink-screen", Tool: "launch_app"})
	ev := nextApproval(t, ui)
	if req, msg := resolveApprovalScope(ev["id"].(string), true, scopeTarget, "desktop"); req != nil || !strings.Contains(msg, "대상") {
		t.Fatalf("target scope without a target: %v %q", req, msg)
	}
	resolveApprovalScope(ev["id"].(string), false, "", "desktop")
	<-ans

	ans = postApproveIn(t, srv.URL, approvalInput{Source: "aglink-screen", Tool: "launch_app", Target: `C:\Windows\notepad.exe`, Summary: "launch_app\n{}"})
	ev = nextApproval(t, ui)
	if ev["target"] != "notepad" {
		t.Fatalf("prompt target: %v", ev)
	}
	if req, msg := resolveApprovalScope(ev["id"].(string), true, scopeTarget, "desktop"); req == nil {
		t.Fatal(msg)
	}
	if a := <-ans; a["approved"] != true {
		t.Fatalf("approved: %v", a)
	}
	saved, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(saved.ApprovalAllow, ",") != "launch_app:notepad" || strings.Join(saved.ApprovalTools, ",") != "launch_app" {
		t.Fatalf("saved allow=%v tools=%v", saved.ApprovalAllow, saved.ApprovalTools)
	}
	b.cfgh = NewConfigHolder(saved)
	if a := answerWithin(t, postApproveIn(t, srv.URL, approvalInput{Source: "aglink-screen", Tool: "launch_app", Target: "Notepad"})); a["approved"] != true {
		t.Fatalf("allowed target: %v", a)
	}
	ans = postApproveIn(t, srv.URL, approvalInput{Source: "aglink-screen", Tool: "launch_app", Target: "calc"})
	resolveApproval(nextApproval(t, ui)["id"].(string), false)
	if a := <-ans; a["approved"] != false {
		t.Fatalf("other target: %v", a)
	}

	// The log has every request: denied, approved(target), auto-target, denied.
	var decisions []string
	for _, e := range readApprovalLog(10) {
		decisions = append(decisions, e.Decision+"/"+e.Scope+"/"+e.By)
	}
	if got := strings.Join(decisions, " "); got != "denied//desktop approved/target/desktop auto-target//host denied//command" {
		t.Fatalf("log decisions: %s", got)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatal(err)
	}

	// !approvals revoke drops it again.
	r := &recordingReply{}
	b.handleApprovalsCommand(r, 1, []string{"!approvals", "allow"})
	if len(r.sent) != 1 || !strings.Contains(r.sent[0], "1. launch_app:notepad") {
		t.Fatalf("allow list: %q", r.sent)
	}
	b.handleApprovalsCommand(r, 1, []string{"!approvals", "revoke", "1"})
	if saved, _ = LoadConfig(path); len(saved.ApprovalAllow) != 0 {
		t.Fatalf("after revoke: %v (%q)", saved.ApprovalAllow, r.sent)
	}
}

func TestApprovalCommandScopes(t *testing.T) {
	withApprovalLog(t)
	ui := fakeUI(t)
	t.Cleanup(func() { clearApprovalSessions("") })
	b := &Bot{cfgh: NewConfigHolder(&Config{AllowedUserIDs: []int64{1}, ApprovalTimeoutSec: 30, ApprovalTools: []string{"eval"}})}
	srv := httptest.NewServer(http.HandlerFunc(b.handleApproveHTTP))
	defer srv.Close()

	ans := postApproveIn(t, srv.URL, approvalInput{Source: "aglink-web", Tool: "eval", Session: "chat:5/conv:zz"})
	ev := nextApproval(t, ui)
	r := &recordingReply{}
	b.handleApprovalCommand(r, 1, []string{"!approve", ev["id"].(string), "target"}, true) // no target: refused, still pending
	b.handleApprovalCommand(r, 1, []string{"!ok", "session"}, true)                        // bare, with a scope word
	if a := <-ans; a["approved"] != true {
		t.Fatalf("approved: %v (%q)", a, r.sent)
	}
	if len(r.sent) != 2 || !strings.Contains(r.sent[0], "대상") || !strings.Contains(r.sent[1], "💬 이 대화에서 허용") {
		t.Fatalf("replies: %q", r.sent)
	}
	if !sessionAllowed("chat:5/conv:zz", "eval") {
		t.Fatal("no session grant")
	}
	// The Telegram keyboard only offers what the request can take.
	kb := approvalKeyboard(&approvalRequest{ID: "abc"})
	if len(kb.InlineKeyboard[0]) != 1 || len(kb.InlineKeyboard[1]) != 2 {
		t.Fatalf("keyboard without session/target: %v", kb.InlineKeyboard)
	}
	kb = approvalKeyboard(&approvalRequest{ID: "abc", Session: "s", Target: "notepad"})
	if len(kb.InlineKeyboard[0]) != 3 || *kb.InlineKeyboard[0][2].CallbackData != "ap:tg:abc" {
		t.Fatalf("full keyboard: %v", kb.InlineKeyboard)
	}
}

func TestApprovalLogLineAndRotation(t *testing.T) {
	path := withApprovalLog(t)
	tok := "123456789:" + strings.Repeat("A", 35)
	line := approvalLogLine(approvalLogEntry{Time: "2026-11-21T10:00:00Z", Source: "aglink-web", Tool: "eval",
		Summary: "eval\n{\"x\":\"" + tok + "\"} " + strings.Repeat("y", 500), Decision: "approved", Scope: "once", By: "telegram"})
	var e map[string]any
	if err := json.Unmarshal(line, &e); err != nil || line[len(line)-1] != '\n' {
		t.Fatalf("not a JSON line: %q", line)
	}
	s := e["summary"].(string)
	if strings.Contains(s, tok) || !strings.Contains(s, "<telegram-token>") || strings.Contains(s, "\n") || len([]rune(s)) > 300 {
		t.Fatalf("summary: %q", s)
	}
	for k, want := range map[string]string{"time": "2026-11-21T10:00:00Z", "tool": "eval", "decision": "approved", "scope": "once", "by": "telegram"} {
		if e[k] != want {
			t.Fatalf("%s = %v, want %s", k, e[k], want)
		}
	}
	if _, has := e["target"]; has {
		t.Fatal("empty target written")
	}

	// Past approvalLogMax the file moves to .1 and a new one starts.
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), approvalLogMax), 0o600); err != nil {
		t.Fatal(err)
	}
	logApproval(approvalLogEntry{Tool: "launch_app", Target: "notepad", Decision: "auto-target", By: "host"})
	if st, err := os.Stat(path + ".1"); err != nil || st.Size() != approvalLogMax {
		t.Fatalf("rotated: %v", err)
	}
	got := readApprovalLog(5)
	if len(got) != 1 || got[0].Target != "notepad" {
		t.Fatalf("read back: %+v", got)
	}
	if f := formatApprovalLogEntry(got[0]); !strings.Contains(f, "🎯 자동(대상) launch_app → notepad") {
		t.Fatalf("format: %q", f)
	}
	r := &recordingReply{}
	(&Bot{cfgh: NewConfigHolder(&Config{})}).handleApprovalsCommand(r, 1, []string{"!approvals", "3"})
	if len(r.sent) != 1 || !strings.Contains(r.sent[0], "최근 승인 기록 1건") {
		t.Fatalf("!approvals: %q", r.sent)
	}
}

// approvals.allow survives the YAML save path and the settings form.
func TestApprovalAllowConfigRoundTrip(t *testing.T) {
	cfg := &Config{TelegramBotToken: "1:test", AllowedUserIDs: []int64{1}, ApprovalTools: []string{"launch_app"}, ApprovalAllow: []string{"launch_app:notepad", "vscode_terminal_run:git status"}}
	raw, err := marshalConfigYAML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	back, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(back.ApprovalAllow, "|") != "launch_app:notepad|vscode_terminal_run:git status" {
		t.Fatalf("round trip: %v\n%s", back.ApprovalAllow, raw)
	}
	// An old config without approvals.allow still loads, and writes no allow key.
	oldRaw, _ := marshalConfigYAML(&Config{TelegramBotToken: "1:test", AllowedUserIDs: []int64{1}, ApprovalTools: []string{"eval"}})
	if _, after, ok := bytes.Cut(oldRaw, []byte("\napprovals:")); !ok || bytes.Contains(after, []byte("allow:")) {
		t.Fatalf("empty allow written:\n%s", oldRaw)
	}
	if err := os.WriteFile(path, oldRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if old, err := LoadConfig(path); err != nil || len(old.ApprovalAllow) != 0 || len(old.ApprovalTools) != 1 {
		t.Fatalf("old config: %v %v", old, err)
	}
	if err := applySettings(cfg, map[string]any{"approvals.allow": "launch_app:Calc, vscode_command:x.y"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.ApprovalAllow, "|") != "launch_app:calc|vscode_command:x.y" {
		t.Fatalf("settings: %v", cfg.ApprovalAllow)
	}
}
