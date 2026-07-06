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

// A web send targeting a web topic continues that topic only.
func TestHandleWebTarget_WebTopic(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	m, st, _ := webTgtManager(t, fc)
	c, _ := st.NewConversation("myapp", "웹 토픽", OriginWeb)
	f := &fakeSender{}

	m.HandleWebTarget(context.Background(), 1, "이 토픽 이어서", Target{Kind: "web", Project: "myapp", ID: c.ID}, f)

	got, _ := st.GetConversation("myapp", c.ID)
	if len(got.History) != 1 {
		t.Errorf("web topic target should append to that topic, got %d", len(got.History))
	}
	if len(st.TelegramConversation().History) != 0 {
		t.Errorf("web topic target must not touch telegram conversation")
	}
}
