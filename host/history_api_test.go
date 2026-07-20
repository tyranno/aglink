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

// TestHistorySnapshot_ReturnsCopy guards against the /api/history data race:
// buildHistoryResponse must read a defensive copy of a conversation's History,
// not the live slice a worker may concurrently be appending to. It checks both
// directions — mutating the returned snapshot must not corrupt the live
// conversation, and appending to the live conversation after the snapshot was
// taken must not retroactively change the snapshot.
func TestHistorySnapshot_ReturnsCopy(t *testing.T) {
	st := histStore(t)
	tc := st.TelegramConversation()
	tc.History = []ConversationTurn{{Prompt: "p1", Response: "r1"}}
	_ = st.UpdateTelegramConversation(tc)

	snap := st.HistorySnapshot(Target{Kind: "telegram"})
	if len(snap) != 1 || snap[0].Prompt != "p1" {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}

	// Mutating the snapshot must not leak back into the live conversation.
	snap[0].Prompt = "mutated"
	live := st.TelegramConversation()
	if live.History[0].Prompt != "p1" {
		t.Errorf("mutating snapshot corrupted live history: %+v", live.History)
	}

	// Appending to the live conversation after the snapshot was taken must not
	// retroactively grow the already-taken snapshot (simulates runWorker's
	// concurrent append while a handler still holds an older snapshot).
	live.History = append(live.History, ConversationTurn{Prompt: "p2", Response: "r2"})
	_ = st.UpdateTelegramConversation(live)
	if len(snap) != 1 {
		t.Errorf("snapshot length changed after live append: got %d turns, want 1", len(snap))
	}

	resp := buildHistoryResponse(st, Target{Kind: "telegram"})
	if len(resp.Turns) != 4 {
		t.Errorf("buildHistoryResponse should reflect the latest live history (4 turns), got %d: %+v", len(resp.Turns), resp.Turns)
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
