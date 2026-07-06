package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// chatCmdBot builds a *Bot with a real fileStore and a recCh registered on
// chatID 1, following the pattern used by origin_test.go
// (TestHandleChatUseQualifiedProjectConversation /
// TestHandleChat_NewFromWeb_TagsWebOrigin): handleChat sends via b.Send,
// which fans out through b.out (*Hub), not a "sender" field (Bot has no such
// field — see bot.go's Bot struct).
func chatCmdBot(t *testing.T) (*Bot, *fileStore, *recCh) {
	t.Helper()
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	if err := st.AddProject("myapp", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	b := &Bot{store: st, out: NewHub()}
	w := &recCh{}
	b.out.Register(1, w)
	return b, st, w
}

func TestHandleChat_TelegramRejected(t *testing.T) {
	b, _, w := chatCmdBot(t)
	b.handleChat(1, "!chat new x", []string{"!chat", "new", "x"}, OriginTelegram)
	joined := strings.Join(w.texts, "\n")
	if !strings.Contains(joined, "웹") {
		t.Errorf("telegram !chat should be rejected with web guidance, got %v", w.texts)
	}
}

func TestHandleChat_WebRename(t *testing.T) {
	b, st, _ := chatCmdBot(t)
	c, _ := st.NewConversation("myapp", "old", OriginWeb)
	_ = st.SetActive("myapp", c.ID)
	b.handleChat(1, "!chat rename 새 제목", []string{"!chat", "rename", "새", "제목"}, OriginWeb)
	got, _ := st.GetConversation("myapp", c.ID)
	if got.Title != "새 제목" {
		t.Errorf("rename should update title, got %q", got.Title)
	}
}
