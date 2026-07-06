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

// The telegram turn runs on the client of TelegramConversation().Backend — the
// backend-selection principle preserved after removing the old resume routing.
func TestHandle_Telegram_RunsOnConversationBackend(t *testing.T) {
	claudeFC := &fakeClaude{runRes: RunResult{Text: "claude ran"}}
	codexFC := &fakeClaude{runRes: RunResult{Text: "codex ran"}}
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	_ = st.AddProject("myapp", t.TempDir())
	_ = st.SetTelegramActiveProject("myapp")
	m := NewManager(claudeFC, codexFC, st, NewConfigHolder(&Config{ManagerAlways: true, WorkerModel: "sonnet", CodexModel: "gpt-5.5"}))

	// Active backend is claude, but the telegram conversation was created on codex.
	tc := st.TelegramConversation()
	tc.Backend = "codex"
	_ = st.UpdateTelegramConversation(tc)

	f := &fakeSender{}
	m.Handle(context.Background(), 1, "뭔가 해줘", OriginTelegram, f)

	if codexFC.runCalls != 1 {
		t.Fatalf("telegram turn must run on the conversation's backend (codex), codex runCalls=%d", codexFC.runCalls)
	}
	if claudeFC.runCalls != 0 {
		t.Fatalf("telegram turn must not run on claude, claude runCalls=%d", claudeFC.runCalls)
	}
	if codexFC.lastRun.Model != "gpt-5.5" {
		t.Errorf("model=%q, want codex model gpt-5.5", codexFC.lastRun.Model)
	}
	if m.Backend() != "claude" {
		t.Errorf("telegram turn must not change global backend, now %s", m.Backend())
	}
}
