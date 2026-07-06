package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func originStore(t *testing.T) *fileStore {
	t.Helper()
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	if err := st.AddProject("p", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestNewConversation_TagsOrigin(t *testing.T) {
	st := originStore(t)
	w, _ := st.NewConversation("p", "web chat", OriginWeb)
	if w.Origin != OriginWeb {
		t.Errorf("origin = %q, want %q", w.Origin, OriginWeb)
	}
	tg, _ := st.NewConversation("p", "tg chat", "")
	if tg.Origin != "" {
		t.Errorf("origin = %q, want empty (shared/telegram)", tg.Origin)
	}
}

func TestConversationChannel(t *testing.T) {
	cases := []struct {
		origin string
		want   string
	}{
		{OriginWeb, OriginWeb},
		{"", OriginTelegram},
		{OriginTelegram, OriginTelegram},
		{"anything", OriginTelegram},
	}
	for _, c := range cases {
		if got := conversationChannel(&Conversation{Origin: c.origin}); got != c.want {
			t.Errorf("conversationChannel(%q) = %q, want %q", c.origin, got, c.want)
		}
	}
	if conversationChannel(nil) != OriginTelegram {
		t.Error("nil conversation should map to telegram")
	}
}

// !chat list (Telegram) must exclude web-created conversations but keep shared ones.
func TestFormatChatList_ExcludesWebOrigin(t *testing.T) {
	st := originStore(t)
	_, _ = st.NewConversation("p", "shared-chat", "")
	webC, _ := st.NewConversation("p", "웹전용대화", OriginWeb)

	b := &Bot{store: st}
	list := b.formatChatList("p")
	if strings.Contains(list, "웹전용대화") || strings.Contains(list, "["+webC.ID+"]") {
		t.Errorf("!chat list must exclude web-origin conversation, got:\n%s", list)
	}
	if !strings.Contains(list, "shared-chat") {
		t.Errorf("!chat list must include shared conversation, got:\n%s", list)
	}
}

// A continuation inherits its parent series' origin.
func TestMakeContinuation_InheritsOrigin(t *testing.T) {
	st := originStore(t)
	m := NewManager(&fakeClaude{}, nil, st, NewConfigHolder(&Config{}))
	parent, _ := st.NewConversation("p", "web parent", OriginWeb)
	child, err := m.makeContinuation("p", parent)
	if err != nil {
		t.Fatal(err)
	}
	if child.Origin != OriginWeb {
		t.Errorf("continuation origin = %q, want %q (inherited)", child.Origin, OriginWeb)
	}
}

// !chat new issued from the web is tagged origin=web.
func TestHandleChat_NewFromWeb_TagsWebOrigin(t *testing.T) {
	st := originStore(t)
	seed, _ := st.NewConversation("p", "seed", "")
	_ = st.SetActive("p", seed.ID)

	b := &Bot{store: st}
	b.out = NewHub()
	b.handleChat(7, "!chat new 웹세션", strings.Fields("!chat new 웹세션"), OriginWeb)

	p, _ := st.GetProject("p")
	found := false
	for _, c := range p.Conversations {
		if c.Title == "웹세션" {
			found = true
			if c.Origin != OriginWeb {
				t.Errorf("!chat new from web origin = %q, want %q", c.Origin, OriginWeb)
			}
		}
	}
	if !found {
		t.Fatal("!chat new did not create a conversation")
	}
}

// /api/conversations tags each topic with its display channel.
func TestWebTopics_ChannelTagging(t *testing.T) {
	st := originStore(t)
	_, _ = st.NewConversation("p", "shared", "")
	_, _ = st.NewConversation("p", "webonly", OriginWeb)

	p, _ := st.GetProject("p")
	topics := webTopicsForProject("p", p.Conversations, ActiveRef{})
	var sharedCh, webCh string
	for _, tp := range topics {
		switch tp.Title {
		case "shared":
			sharedCh = tp.Channel
		case "webonly":
			webCh = tp.Channel
		}
	}
	if sharedCh != OriginTelegram {
		t.Errorf("shared topic channel = %q, want %q", sharedCh, OriginTelegram)
	}
	if webCh != OriginWeb {
		t.Errorf("webonly topic channel = %q, want %q", webCh, OriginWeb)
	}
}
