package main

import (
	"path/filepath"
	"testing"
	"time"
)

func histStore(t *testing.T) *fileStore {
	t.Helper()
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	_ = st.AddProject("myapp", t.TempDir())
	return st
}

func TestBuildHistoryResponse_Telegram(t *testing.T) {
	st := histStore(t)
	tc := st.TelegramConversation()
	tc.History = []ConversationTurn{{Timestamp: time.Now(), Prompt: "안녕", Response: "네"}}
	_ = st.UpdateTelegramConversation(tc)

	resp := buildHistoryResponse(st, Target{Kind: "telegram"})
	if len(resp.Turns) != 2 || resp.Turns[0].Role != "user" || resp.Turns[0].Text != "안녕" || resp.Turns[1].Role != "assistant" || resp.Turns[1].Text != "네" {
		t.Errorf("telegram history should expand to user/assistant turns, got %+v", resp.Turns)
	}
}

func TestBuildHistoryResponse_WebTopic(t *testing.T) {
	st := histStore(t)
	c, _ := st.NewConversation("myapp", "t", OriginWeb)
	c.History = []ConversationTurn{{Prompt: "q", Response: "a"}}
	_ = st.UpdateConversation("myapp", c)

	resp := buildHistoryResponse(st, Target{Kind: "web", Project: "myapp", ID: c.ID})
	if len(resp.Turns) != 2 || resp.Turns[1].Text != "a" {
		t.Errorf("web topic history wrong: %+v", resp.Turns)
	}
}

func TestBuildConversationsResponse_IncludesTelegram(t *testing.T) {
	st := histStore(t)
	_ = st.TelegramConversation()
	resp := buildConversationsResponse(st)
	if resp.Telegram == nil || resp.Telegram.ID != "telegram" {
		t.Errorf("conversations response must include the telegram global entry, got %+v", resp.Telegram)
	}
}
