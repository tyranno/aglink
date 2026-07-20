package main

import (
	"context"
	"path/filepath"
	"testing"
)

func webTgtManager(t *testing.T, fc *fakeClaude) (*Manager, *fileStore, string) {
	t.Helper()
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	dir := t.TempDir()
	_ = st.AddProject("myapp", dir)
	_ = st.SetTelegramActiveProject("myapp")
	return NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true})), st, dir
}

// A web send targeting telegram continues the global telegram conversation.
func TestHandleWebTarget_Telegram(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	m, st, _ := webTgtManager(t, fc)
	f := &fakeSender{}

	m.HandleWebTarget(context.Background(), 1, "여기 텔레그램 스트림에 이어서", Target{Kind: "telegram"}, f)

	if len(st.TelegramConversation().History) != 1 {
		t.Errorf("web→telegram target should append to telegram conversation")
	}
}

// A web send targeting a top-level web conversation continues that conversation
// only — never a project topic and never the telegram stream.
func TestHandleWebTarget_WebTopic(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	m, st, _ := webTgtManager(t, fc)
	c, _ := st.NewWebConv("웹 대화")
	f := &fakeSender{}

	m.HandleWebTarget(context.Background(), 1, "이 대화 이어서", Target{Kind: "web", ID: c.ID}, f)

	got, _ := st.GetWebConv(c.ID)
	if len(got.History) != 1 {
		t.Errorf("web conv target should append to that conversation, got %d", len(got.History))
	}
	if len(st.TelegramConversation().History) != 0 {
		t.Errorf("web conv target must not touch telegram conversation")
	}
}
