package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// fakeClaude is a programmable ClaudeClient for manager tests.
type fakeClaude struct {
	decision RouteDecision
	routeErr error
	runRes   RunResult
	runErr   error
	lastRun  RunRequest
	runCalls int
}

func (f *fakeClaude) Route(_ context.Context, _ RouteRequest) (RouteDecision, error) {
	return f.decision, f.routeErr
}
func (f *fakeClaude) Run(_ context.Context, req RunRequest) (RunResult, error) {
	f.lastRun = req
	f.runCalls++
	return f.runRes, f.runErr
}

func mgrFixture(t *testing.T, fc *fakeClaude) (*Manager, *fileStore, string) {
	t.Helper()
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := st.AddProject("myapp", dir); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{ManagerAlways: true}
	return NewManager(fc, nil, st, NewConfigHolder(cfg)), st, dir
}

// Telegram no longer blocks when no projects are registered — the web-first
// redesign runs the turn in the service home instead of prompting !project add.
func TestManager_NoProjects_RunsInHome(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	home := t.TempDir()
	m := NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true, HomeDir: home}))
	f := &fakeSender{}
	m.Handle(context.Background(), 1, "hi", "", f)
	if fc.runCalls != 1 {
		t.Fatalf("no-project telegram should run in home, runCalls=%d", fc.runCalls)
	}
	if fc.lastRun.WorkDir != home {
		t.Errorf("no-project telegram turn should run in home %q, got %q", home, fc.lastRun.WorkDir)
	}
	if contains(strings.Join(f.sent, ""), "!project add") {
		t.Errorf("telegram must no longer block on missing projects, got: %v", f.sent)
	}
}

// Auto-continuation still fires on the telegram stream: a large history triggers
// an in-place series reset via the telegram sink (same "telegram" record, fresh
// CLI session), carrying the prior summary into the next prompt — without ever
// forking a project topic.
func TestManager_AutoContinuation_LargeHistory(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "더 많은 작업 완료"}}
	m, st, _ := tgManager(t, fc)

	// Seed the global telegram conversation with a large history + summary.
	c := st.TelegramConversation()
	c.Started = true
	c.Summary = "이전에 많은 작업을 했습니다"
	oldSession := c.SessionID

	longPrompt := "여기는 매우 긴 프롬프트입니다. "
	longResponse := "여기는 매우 긴 응답입니다. "
	for range 5000 { // ~70k tokens with multiplier
		longPrompt += "긴 텍스트를 반복합니다. "
		longResponse += "긴 응답을 반복합니다. "
	}
	c.History = append(c.History, ConversationTurn{Prompt: longPrompt, Response: longResponse})
	_ = st.UpdateTelegramConversation(c)

	f := &fakeSender{}
	m.Handle(context.Background(), 1, "계속 작업해줘", OriginTelegram, f)

	if fc.runCalls != 1 {
		t.Fatalf("expected one worker run, got %d", fc.runCalls)
	}
	// In-place continuation: still exactly the single telegram record, no project fork.
	tc := st.TelegramConversation()
	if tc.ID != "telegram" {
		t.Errorf("telegram record must stay one conversation, got id %q", tc.ID)
	}
	p, _ := st.GetProject("myapp")
	if len(p.Conversations) != 0 {
		t.Fatalf("auto-continuation must not fork a project topic, got %d", len(p.Conversations))
	}
	// Session was reset for the fresh series.
	if tc.SessionID == oldSession {
		t.Errorf("continuation should reset the CLI session, still %q", oldSession)
	}
	// The carried summary made it into the worker prompt.
	if !contains(fc.lastRun.Prompt, "이전에 많은 작업을 했습니다") {
		t.Errorf("continuation prompt should include parent summary, got: %q", fc.lastRun.Prompt)
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		text  string
		name  string
		check func(int) bool
	}{
		{"", "empty", func(n int) bool { return n == 0 }},
		{"hello", "one-word", func(n int) bool { return n > 0 && n < 10 }},
		{"hello world this is a test", "multi-word", func(n int) bool { return n >= 5 && n < 15 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateTokens(tt.text)
			if !tt.check(got) {
				t.Errorf("estimateTokens(%q) = %d, check failed", tt.text, got)
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestSetBackend_Switch(t *testing.T) {
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	claude := &fakeClaude{}
	codex := &fakeClaude{}
	m := NewManager(claude, codex, st, NewConfigHolder(&Config{ManagerAlways: true}))

	if m.Backend() != "claude" {
		t.Fatal("default backend should be claude")
	}

	if err := m.SetBackend("codex"); err != nil {
		t.Fatal(err)
	}
	if m.Backend() != "codex" {
		t.Error("expected codex after switch")
	}

	if err := m.SetBackend("claude"); err != nil {
		t.Fatal(err)
	}
	if m.Backend() != "claude" {
		t.Error("expected claude after switch back")
	}
}

func TestHandleScheduledTask_UsesActiveProject(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "done"}}
	m, st, dir := mgrFixture(t, fc)

	// Create conversation and mark active in "myapp".
	c, _ := st.NewConversation("myapp", "main chat", "")
	c.Started = true
	_ = st.UpdateConversation("myapp", c)
	_ = st.SetActive("myapp", c.ID)

	f := &fakeSender{}
	m.HandleScheduledTask(context.Background(), 1, "daily check", f)

	if fc.runCalls != 1 {
		t.Fatalf("Run called %d times, want 1", fc.runCalls)
	}
	if fc.lastRun.WorkDir != dir {
		t.Errorf("WorkDir = %q, want %q (active project dir)", fc.lastRun.WorkDir, dir)
	}
}

func TestHandleScheduledTask_AlphabeticalFallback(t *testing.T) {
	// When no active project is set, HandleScheduledTask must fall back to
	// alphabetically first project — not the map-iteration random one.
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	// Add three projects with names that differ only in first char.
	zoDir := t.TempDir()
	alDir := t.TempDir()
	beDir := t.TempDir()
	for _, pair := range [][2]string{{"zoo", zoDir}, {"alpha", alDir}, {"beta", beDir}} {
		if err := st.AddProject(pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	// No active project set — Active.Project is "".

	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	m := NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true}))
	f := &fakeSender{}
	m.HandleScheduledTask(context.Background(), 1, "morning summary", f)

	if fc.runCalls != 1 {
		t.Fatalf("Run called %d times, want 1", fc.runCalls)
	}
	// "alpha" is alphabetically first.
	if fc.lastRun.WorkDir != alDir {
		t.Errorf("WorkDir = %q, want %q (alpha — alphabetically first)", fc.lastRun.WorkDir, alDir)
	}
}

func TestHandleScheduledTask_NoProjects(t *testing.T) {
	fc := &fakeClaude{}
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	m := NewManager(fc, nil, st, NewConfigHolder(&Config{}))
	f := &fakeSender{}
	m.HandleScheduledTask(context.Background(), 1, "hello", f)

	if fc.runCalls != 0 {
		t.Errorf("Run should not be called when no projects registered")
	}
	if !contains(strings.Join(f.sent, ""), "!project add") {
		t.Errorf("should prompt user to register project, got: %v", f.sent)
	}
}

func TestNewManager_CodexOnlyDefaultsToCodex(t *testing.T) {
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	codex := &fakeClaude{}
	// claude not installed → nil claude runner.
	m := NewManager(nil, codex, st, NewConfigHolder(&Config{}))
	if m.Backend() != "codex" {
		t.Errorf("codex-only install should default to codex, got %q", m.Backend())
	}
}

func TestSetBackend_ClaudeUnavailable(t *testing.T) {
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	codex := &fakeClaude{}
	m := NewManager(nil, codex, st, NewConfigHolder(&Config{}))
	if err := m.SetBackend("claude"); err == nil {
		t.Error("expected error when claude not available")
	}
	if m.Backend() != "codex" {
		t.Errorf("backend should remain codex after failed switch, got %q", m.Backend())
	}
}

func TestChooseBackend(t *testing.T) {
	cases := []struct {
		name          string
		preferred     string
		claude, codex bool
		want          string
		wantOK        bool
	}{
		{"prefer codex, both installed", "codex", true, true, "codex", true},
		{"prefer claude, both installed", "claude", true, true, "claude", true},
		{"prefer codex, only claude → fallback", "codex", true, false, "claude", true},
		{"prefer claude, only codex → fallback", "claude", false, true, "codex", true},
		{"empty pref, only codex", "", false, true, "codex", true},
		{"empty pref, only claude", "", true, false, "claude", true},
		{"empty pref, both → claude", "", true, true, "claude", true},
		{"unknown pref, only codex", "weird", false, true, "codex", true},
		{"neither installed", "claude", false, false, "", false},
	}
	for _, c := range cases {
		got, ok := chooseBackend(c.preferred, c.claude, c.codex)
		if got != c.want || ok != c.wantOK {
			t.Errorf("%s: chooseBackend(%q,%v,%v)=(%q,%v), want (%q,%v)",
				c.name, c.preferred, c.claude, c.codex, got, ok, c.want, c.wantOK)
		}
	}
}

func TestSetBackend_CodexUnavailable(t *testing.T) {
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	claude := &fakeClaude{}
	m := NewManager(claude, nil, st, NewConfigHolder(&Config{ManagerAlways: true}))

	if err := m.SetBackend("codex"); err == nil {
		t.Error("expected error when codex not available")
	}
	if m.Backend() != "claude" {
		t.Error("backend should remain claude after failed switch")
	}
}
