package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// twoBackendManager builds a Manager with BOTH claude and codex clients so tests
// can verify resume routing across backends. Active backend defaults to claude.
// Params are ClaudeClient (not *fakeClaude) so a caller can pass an untyped nil
// for a missing backend — a typed *fakeClaude(nil) would become a non-nil
// interface and defeat the installed-check, which production never does
// (main.go leaves `var codexRunner ClaudeClient` as a true nil interface).
func twoBackendManager(t *testing.T, claude, codex ClaudeClient) (*Manager, *fileStore) {
	t.Helper()
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	if err := st.AddProject("myapp", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{ManagerAlways: true, WorkerModel: "sonnet", CodexModel: "gpt-5.5"}
	return NewManager(claude, codex, st, NewConfigHolder(cfg)), st
}

// Regression: resuming a conversation created on a DIFFERENT backend than the
// currently active one must continue that conversation on its own backend — not
// silently fork a fresh "새 대화 (claude)". This was the top-priority bug: web
// users reopening an old codex chat while claude was active always got a new
// conversation instead of the one they picked.
func TestManager_Resume_UsesConversationOwnBackend(t *testing.T) {
	claudeFC := &fakeClaude{runRes: RunResult{Text: "claude ran"}}
	codexFC := &fakeClaude{runRes: RunResult{Text: "codex ran"}}
	m, st := twoBackendManager(t, claudeFC, codexFC)

	if m.Backend() != "claude" {
		t.Fatalf("default backend should be claude, got %s", m.Backend())
	}

	// An existing, started conversation created on codex.
	c, _ := st.NewConversation("myapp", "옛날 codex 대화", "")
	c.Backend = "codex"
	c.Started = true
	c.SessionID = "codex-sess-1"
	_ = st.UpdateConversation("myapp", c)
	_ = st.SetActive("myapp", c.ID)

	// User explicitly resumes it while claude is the active backend.
	claudeFC.decision = RouteDecision{Action: ActionResume, Project: "myapp", ConversationID: c.ID}
	f := &fakeSender{}
	m.Handle(context.Background(), 1, "그거 계속 진행하자", "", f)

	// The codex client runs it; claude must not.
	if codexFC.runCalls != 1 {
		t.Fatalf("codex conversation must run on codex client, codex runCalls=%d", codexFC.runCalls)
	}
	if claudeFC.runCalls != 0 {
		t.Fatalf("codex conversation must NOT run on claude client, claude runCalls=%d", claudeFC.runCalls)
	}
	// It resumes the SAME session, using the codex model.
	if !codexFC.lastRun.Resume {
		t.Error("should resume the existing codex session")
	}
	if codexFC.lastRun.SessionID != "codex-sess-1" {
		t.Errorf("session=%q, want codex-sess-1", codexFC.lastRun.SessionID)
	}
	if codexFC.lastRun.Model != "gpt-5.5" {
		t.Errorf("model=%q, want codex model gpt-5.5", codexFC.lastRun.Model)
	}
	// No fork: still exactly one conversation.
	p, _ := st.GetProject("myapp")
	if len(p.Conversations) != 1 {
		t.Fatalf("resume must not fork a new conversation, got %d", len(p.Conversations))
	}
	// Resume must not mutate the global backend selection.
	if m.Backend() != "claude" {
		t.Errorf("resume must not change global backend, now %s", m.Backend())
	}
	// No "새 대화" noise.
	for _, msg := range f.sent {
		if strings.Contains(msg, "새 대화") {
			t.Errorf("resume should not announce a new conversation: %q", msg)
		}
	}
}

// When a conversation's backend is not installed, resume must explain clearly
// rather than silently substituting a new conversation on the active backend.
func TestManager_Resume_ConversationBackendMissing_Explains(t *testing.T) {
	claudeFC := &fakeClaude{runRes: RunResult{Text: "claude ran"}}
	m, st := twoBackendManager(t, claudeFC, nil) // codex NOT installed

	c, _ := st.NewConversation("myapp", "codex 대화", "")
	c.Backend = "codex"
	c.Started = true
	_ = st.UpdateConversation("myapp", c)
	_ = st.SetActive("myapp", c.ID)

	claudeFC.decision = RouteDecision{Action: ActionResume, Project: "myapp", ConversationID: c.ID}
	f := &fakeSender{}
	m.Handle(context.Background(), 1, "그거 계속 진행하자", "", f)

	if claudeFC.runCalls != 0 {
		t.Errorf("must not silently run a codex conversation on claude, runCalls=%d", claudeFC.runCalls)
	}
	p, _ := st.GetProject("myapp")
	if len(p.Conversations) != 1 {
		t.Errorf("must not fork a new conversation, got %d", len(p.Conversations))
	}
	joined := strings.Join(f.sent, "\n")
	if !strings.Contains(joined, "설치") || !strings.Contains(strings.ToUpper(joined), "CODEX") {
		t.Errorf("should explain that codex is not installed, got %v", f.sent)
	}
}

// Sanity: a claude conversation still resumes on claude when claude is active
// (same-backend path unchanged).
func TestManager_Resume_SameBackend_Unchanged(t *testing.T) {
	claudeFC := &fakeClaude{runRes: RunResult{Text: "claude ran"}}
	codexFC := &fakeClaude{runRes: RunResult{Text: "codex ran"}}
	m, st := twoBackendManager(t, claudeFC, codexFC)

	c, _ := st.NewConversation("myapp", "claude 대화", "")
	c.Backend = "claude"
	c.Started = true
	c.SessionID = "claude-sess-1"
	_ = st.UpdateConversation("myapp", c)
	_ = st.SetActive("myapp", c.ID)

	claudeFC.decision = RouteDecision{Action: ActionResume, Project: "myapp", ConversationID: c.ID}
	f := &fakeSender{}
	m.Handle(context.Background(), 1, "그거 계속 진행하자", "", f)

	if claudeFC.runCalls != 1 || codexFC.runCalls != 0 {
		t.Fatalf("claude conversation should run on claude only, claude=%d codex=%d", claudeFC.runCalls, codexFC.runCalls)
	}
	if claudeFC.lastRun.Model != "sonnet" {
		t.Errorf("model=%q, want claude worker model sonnet", claudeFC.lastRun.Model)
	}
}
