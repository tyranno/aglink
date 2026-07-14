package main

import (
	"context"
	"path/filepath"
	"testing"
)

func tgManager(t *testing.T, fc *fakeClaude) (*Manager, *fileStore, string) {
	t.Helper()
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	dir := t.TempDir()
	_ = st.AddProject("myapp", dir)
	_ = st.SetTelegramActiveProject("myapp")
	return NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true})), st, dir
}

// A plain telegram message always continues the single global telegram conversation.
func TestHandle_Telegram_AlwaysGlobalConversation(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	m, st, _ := tgManager(t, fc)
	f := &fakeSender{}

	m.Handle(context.Background(), 1, "로그인 고쳐줘", OriginTelegram, f)

	if fc.runCalls != 1 {
		t.Fatalf("expected one worker run, got %d", fc.runCalls)
	}
	tc := st.TelegramConversation()
	if len(tc.History) != 1 {
		t.Fatalf("telegram conversation should have the turn, got %d", len(tc.History))
	}
	// No project topic created.
	p, _ := st.GetProject("myapp")
	if len(p.Conversations) != 0 {
		t.Errorf("telegram must not create a project topic, got %d", len(p.Conversations))
	}
}

// "이제 <project> 하자" switches TelegramActiveProject but keeps the same conversation.
func TestHandle_Telegram_ProjectSwitch_KeepsConversation(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	m, st, _ := tgManager(t, fc)
	_ = st.AddProject("voice", t.TempDir())
	f := &fakeSender{}

	m.Handle(context.Background(), 1, "이제 voice 하자", OriginTelegram, f)

	if st.TelegramActiveProject() != "voice" {
		t.Errorf("active project should switch to voice, got %q", st.TelegramActiveProject())
	}
	if st.TelegramConversation().ID != "telegram" {
		t.Errorf("switch must not fork the telegram conversation")
	}
}

// The telegram turn always runs on the manager's live active backend, not a
// value stamped on the conversation at creation time — otherwise a runtime
// !backend switch would report success but keep routing through the old
// backend forever (tc.Backend, once stamped, is never refreshed).
func TestHandle_Telegram_RunsOnActiveBackend(t *testing.T) {
	claudeFC := &fakeClaude{runRes: RunResult{Text: "claude ran"}}
	codexFC := &fakeClaude{runRes: RunResult{Text: "codex ran"}}
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	_ = st.AddProject("myapp", t.TempDir())
	_ = st.SetTelegramActiveProject("myapp")
	m := NewManager(claudeFC, codexFC, st, NewConfigHolder(&Config{ManagerAlways: true, WorkerModel: "sonnet", CodexModel: "gpt-5.5"}))

	// The telegram conversation gets stamped "claude" (the default) at creation;
	// the manager's active backend is then switched to codex afterward.
	tc := st.TelegramConversation()
	if tc.Backend != "" && tc.Backend != "claude" {
		t.Fatalf("expected conversation stamped claude/empty at creation, got %q", tc.Backend)
	}
	if err := m.SetBackend("codex"); err != nil {
		t.Fatalf("SetBackend(codex): %v", err)
	}

	f := &fakeSender{}
	m.Handle(context.Background(), 1, "뭔가 해줘", OriginTelegram, f)

	if codexFC.runCalls != 1 {
		t.Fatalf("telegram turn must run on the active backend (codex), codex runCalls=%d", codexFC.runCalls)
	}
	if claudeFC.runCalls != 0 {
		t.Fatalf("telegram turn must not run on claude, claude runCalls=%d", claudeFC.runCalls)
	}
	if codexFC.lastRun.Model != "gpt-5.5" {
		t.Errorf("model=%q, want codex model gpt-5.5", codexFC.lastRun.Model)
	}
	if m.Backend() != "codex" {
		t.Errorf("active backend should now be codex, got %s", m.Backend())
	}
}

func TestHandle_Telegram_ChannelBackendOverrideWinsOverDefault(t *testing.T) {
	claudeFC := &fakeClaude{runRes: RunResult{Text: "claude ran"}}
	codexFC := &fakeClaude{runRes: RunResult{Text: "codex ran"}}
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	_ = st.AddProject("myapp", t.TempDir())
	_ = st.SetTelegramActiveProject("myapp")
	m := NewManager(claudeFC, codexFC, st, NewConfigHolder(&Config{ManagerAlways: true, WorkerModel: "sonnet", CodexModel: "gpt-5.5"}))

	tc := st.TelegramConversation()
	tc.Backend = "codex"
	_ = st.UpdateTelegramConversation(tc)

	f := &fakeSender{}
	m.Handle(context.Background(), 1, "use channel override", OriginTelegram, f)

	if codexFC.runCalls != 1 {
		t.Fatalf("telegram override should run codex, codex runCalls=%d", codexFC.runCalls)
	}
	if claudeFC.runCalls != 0 {
		t.Fatalf("telegram override should not run claude, claude runCalls=%d", claudeFC.runCalls)
	}
	if codexFC.lastRun.Model != "gpt-5.5" {
		t.Errorf("model=%q, want codex model gpt-5.5", codexFC.lastRun.Model)
	}
}
