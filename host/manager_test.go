package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// With interactive_claude enabled, a claude web conversation is interactive by
// default (no per-conversation toggle needed); "!interactive off" explicitly
// overrides that default and "!interactive on" restores it.
func TestIsInteractive_DefaultOnForClaude(t *testing.T) {
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	m := NewManager(&fakeClaude{}, nil, st, NewConfigHolder(&Config{DefaultBackend: "claude"}))
	m.SetInteractiveClient(&fakeClaude{}) // interactive_claude enabled → runner present

	c, err := st.NewWebConv("t")
	if err != nil {
		t.Fatal(err)
	}
	c.Backend = "claude"
	if err := st.UpdateWebConv(c); err != nil {
		t.Fatal(err)
	}
	tgt := Target{Kind: TargetWeb, ID: c.ID}

	if !m.IsInteractive(tgt) {
		t.Fatal("claude web conv should default to interactive when interactive_claude is enabled")
	}
	if err := m.SetInteractive(tgt, false); err != nil {
		t.Fatal(err)
	}
	if m.IsInteractive(tgt) {
		t.Error("explicit !interactive off must override the default")
	}
	if err := m.SetInteractive(tgt, true); err != nil {
		t.Fatal(err)
	}
	if !m.IsInteractive(tgt) {
		t.Error("explicit !interactive on must be interactive")
	}
}

// Interactive is claude-only and gated on the runner existing: a codex
// conversation, or any conversation when interactive_claude is off, is never
// interactive regardless of the default.
func TestIsInteractive_OffForNonClaudeOrDisabled(t *testing.T) {
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	// Runner present but the conversation runs on codex → not interactive.
	m := NewManager(&fakeClaude{}, &fakeClaude{}, st, NewConfigHolder(&Config{DefaultBackend: "claude"}))
	m.SetInteractiveClient(&fakeClaude{})
	c, _ := st.NewWebConv("codex-conv")
	c.Backend = "codex"
	_ = st.UpdateWebConv(c)
	if m.IsInteractive(Target{Kind: TargetWeb, ID: c.ID}) {
		t.Error("codex conversation must never be interactive")
	}

	// No interactive runner constructed (interactive_claude off) → always false.
	m2 := NewManager(&fakeClaude{}, nil, st, NewConfigHolder(&Config{DefaultBackend: "claude"}))
	c2, _ := st.NewWebConv("claude-conv")
	c2.Backend = "claude"
	_ = st.UpdateWebConv(c2)
	if m2.IsInteractive(Target{Kind: TargetWeb, ID: c2.ID}) {
		t.Error("interactive must be off when no interactive runner exists")
	}
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

// !compact should ask the Worker to persist decisions to memory.md, then reset
// the local history mirror and force the next turn onto a fresh CLI session
// (Started=false) instead of resuming the now-externalized one.
func TestCompactTelegramConversation_SavesAndResetsSession(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "메모리에 3가지 결정사항을 저장했습니다."}}
	m, st, _ := tgManager(t, fc)

	c := st.TelegramConversation()
	c.Started = true
	c.History = append(c.History, ConversationTurn{Prompt: "질문", Response: "답변"})
	oldSession := c.SessionID
	_ = st.UpdateTelegramConversation(c)

	f := &fakeSender{}
	m.CompactTelegramConversation(context.Background(), 1, f)

	if fc.runCalls != 1 {
		t.Fatalf("expected one compaction worker run, got %d", fc.runCalls)
	}
	if !contains(fc.lastRun.Prompt, "memory/"+c.ID+".md") {
		t.Errorf("compaction prompt should ask to save to this conversation's own memory file, got: %q", fc.lastRun.Prompt)
	}
	if !fc.lastRun.Resume || fc.lastRun.SessionID != oldSession {
		t.Errorf("compaction turn should resume the existing session %q to see full context, got resume=%v session=%q",
			oldSession, fc.lastRun.Resume, fc.lastRun.SessionID)
	}

	tc := st.TelegramConversation()
	if len(tc.History) != 0 {
		t.Errorf("history mirror should be cleared after compaction, got %d turns", len(tc.History))
	}
	if tc.Started {
		t.Error("Started should be reset so the next turn starts a fresh session instead of resuming")
	}
	if tc.SessionID == oldSession {
		t.Error("SessionID should be cleared, not left pointing at the now-compacted session")
	}
	if !contains(strings.Join(f.sent, ""), "저장했습니다") {
		t.Errorf("user should see the compaction summary, got: %v", f.sent)
	}
}

// A failed compaction turn must leave the conversation untouched — otherwise a
// transient error would silently discard history that was never actually
// saved anywhere durable.
func TestCompactTelegramConversation_FailurePreservesState(t *testing.T) {
	fc := &fakeClaude{runErr: fmt.Errorf("boom")}
	m, st, _ := tgManager(t, fc)

	c := st.TelegramConversation()
	c.Started = true
	c.History = append(c.History, ConversationTurn{Prompt: "질문", Response: "답변"})
	_ = st.UpdateTelegramConversation(c)

	f := &fakeSender{}
	m.CompactTelegramConversation(context.Background(), 1, f)

	tc := st.TelegramConversation()
	if len(tc.History) != 1 {
		t.Errorf("history must survive a failed compaction, got %d turns", len(tc.History))
	}
	if !tc.Started {
		t.Error("Started must survive a failed compaction")
	}
	if !contains(strings.Join(f.sent, ""), "압축 실패") {
		t.Errorf("user should be told compaction failed, got: %v", f.sent)
	}
}

// A never-used telegram conversation (no history, never started) has nothing
// to compact — must not spend a Worker turn on it.
func TestCompactTelegramConversation_NothingToCompact(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "should not be called"}}
	m, _, _ := tgManager(t, fc)

	f := &fakeSender{}
	m.CompactTelegramConversation(context.Background(), 1, f)

	if fc.runCalls != 0 {
		t.Errorf("nothing to compact should not spend a worker turn, runCalls=%d", fc.runCalls)
	}
	if !contains(strings.Join(f.sent, ""), "압축할 대화 내용이 없습니다") {
		t.Errorf("user should be told there's nothing to compact, got: %v", f.sent)
	}
}

// estimateTurnDuration must withhold an estimate until there's enough data to
// back it — showing an "average" from 1-2 samples would read as a made-up
// number rather than a genuine trend.
func TestEstimateTurnDuration_NeedsMinimumSamples(t *testing.T) {
	m := &Manager{}
	for i, want := range []bool{false, false, true} {
		m.recordTurnDuration("claude", time.Duration(i+1)*time.Minute)
		_, samples, ok := m.estimateTurnDuration("claude")
		if ok != want {
			t.Errorf("after %d sample(s): ok=%v, want %v", i+1, ok, want)
		}
		if samples != i+1 {
			t.Errorf("after %d sample(s): reported samples=%d", i+1, samples)
		}
	}
	avg, _, ok := m.estimateTurnDuration("claude")
	if !ok || avg != 2*time.Minute {
		t.Errorf("average of 1,2,3 minutes = %v, want 2m (ok=%v)", avg, ok)
	}
	// A backend with no recorded turns yet must not borrow another backend's data.
	if _, _, ok := m.estimateTurnDuration("codex"); ok {
		t.Error("codex has no samples yet — must not report an estimate")
	}
}

// The rolling window caps at turnDurationSamples so the estimate reflects
// recent turns, not a stale all-time average.
func TestEstimateTurnDuration_RollingWindowCap(t *testing.T) {
	m := &Manager{}
	for range turnDurationSamples {
		m.recordTurnDuration("claude", time.Minute) // baseline: all 1-minute turns
	}
	m.recordTurnDuration("claude", 60*time.Minute) // one big outlier pushes out the oldest sample
	m.durationsMu.Lock()
	got := len(m.turnDurations["claude"])
	m.durationsMu.Unlock()
	if got != turnDurationSamples {
		t.Fatalf("window should stay capped at %d, got %d", turnDurationSamples, got)
	}
	avg, _, _ := m.estimateTurnDuration("claude")
	wantAvg := (time.Duration(turnDurationSamples-1)*time.Minute + 60*time.Minute) / turnDurationSamples
	if avg != wantAvg {
		t.Errorf("average = %v, want %v (oldest 1m sample should have been evicted)", avg, wantAvg)
	}
}

// End-to-end: the estimate message appears once there's enough turn history
// for that backend, and never claims an estimate before then.
func TestHandle_Telegram_ShowsEstimateOnceEnoughHistory(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	m, _, _ := tgManager(t, fc)

	for i := 1; i <= 4; i++ {
		f := &fakeSender{}
		m.Handle(context.Background(), 1, "작업 진행", OriginTelegram, f)
		sent := strings.Join(f.sent, "\n")
		hasEstimate := contains(sent, "최근") && contains(sent, "평균")
		if i < 3 && hasEstimate {
			t.Errorf("turn %d: estimate shown too early with only %d prior sample(s): %v", i, i-1, f.sent)
		}
		if i >= 4 && !hasEstimate {
			t.Errorf("turn %d: expected an estimate after %d prior samples, got: %v", i, i-1, f.sent)
		}
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

	// Create conversation in "myapp" (no store.SetActive — the task carries its
	// own project, pinned at creation time, independent of the shared pointer).
	c, _ := st.NewConversation("myapp", "main chat", "")
	c.Started = true
	_ = st.UpdateConversation("myapp", c)

	f := &fakeSender{}
	m.HandleScheduledTask(context.Background(), 1, "daily check", "myapp", f)

	if fc.runCalls != 1 {
		t.Fatalf("Run called %d times, want 1", fc.runCalls)
	}
	if fc.lastRun.WorkDir != dir {
		t.Errorf("WorkDir = %q, want %q (pinned project dir)", fc.lastRun.WorkDir, dir)
	}
}

// TestHandleScheduledTask_IgnoresConcurrentSharedActive guards the fix: a
// scheduled task must run in the project it was created for even if another
// channel (e.g. a concurrent web conversation) has since overwritten the
// shared store.Active pointer to point somewhere else.
func TestHandleScheduledTask_IgnoresConcurrentSharedActive(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "done"}}
	m, st, dir := mgrFixture(t, fc)

	c, _ := st.NewConversation("myapp", "main chat", "")
	c.Started = true
	_ = st.UpdateConversation("myapp", c)

	// Simulate a concurrent, unrelated web conversation stomping the shared
	// Active pointer onto a different (nonexistent) project.
	_ = st.SetActive("other-project", "some-web-conv-id")

	f := &fakeSender{}
	m.HandleScheduledTask(context.Background(), 1, "daily check", "myapp", f)

	if fc.runCalls != 1 {
		t.Fatalf("Run called %d times, want 1", fc.runCalls)
	}
	if fc.lastRun.WorkDir != dir {
		t.Errorf("WorkDir = %q, want %q (task's own pinned project, not the clobbered shared Active)", fc.lastRun.WorkDir, dir)
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
	// No project pinned on the task (project == "").

	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	m := NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true}))
	f := &fakeSender{}
	m.HandleScheduledTask(context.Background(), 1, "morning summary", "", f)

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
	m.HandleScheduledTask(context.Background(), 1, "hello", "", f)

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
		name                    string
		preferred               string
		claude, codex, opencode bool
		want                    string
		wantOK                  bool
	}{
		{"prefer codex, both installed", "codex", true, true, false, "codex", true},
		{"prefer claude, both installed", "claude", true, true, false, "claude", true},
		{"prefer codex, only claude → fallback", "codex", true, false, false, "claude", true},
		{"prefer claude, only codex → fallback", "claude", false, true, false, "codex", true},
		{"empty pref, only codex", "", false, true, false, "codex", true},
		{"empty pref, only claude", "", true, false, false, "claude", true},
		{"empty pref, both → claude", "", true, true, false, "claude", true},
		{"unknown pref, only codex", "weird", false, true, false, "codex", true},
		{"neither installed", "claude", false, false, false, "", false},
		{"prefer opencode, installed", "opencode", true, true, true, "opencode", true},
		{"prefer opencode, not installed → claude", "opencode", true, false, false, "claude", true},
		{"empty pref, only opencode", "", false, false, true, "opencode", true},
		{"only opencode, none installed", "opencode", false, false, false, "", false},
		{"fallback order claude before opencode", "", true, false, true, "claude", true},
	}
	for _, c := range cases {
		got, ok := chooseBackend(c.preferred, c.claude, c.codex, c.opencode)
		if got != c.want || ok != c.wantOK {
			t.Errorf("%s: chooseBackend(%q,%v,%v,%v)=(%q,%v), want (%q,%v)",
				c.name, c.preferred, c.claude, c.codex, c.opencode, got, ok, c.want, c.wantOK)
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
