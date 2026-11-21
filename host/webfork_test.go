package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func forkTestHistory(base time.Time) []ConversationTurn {
	return []ConversationTurn{
		{Timestamp: base, Prompt: "첫 질문", Response: "첫 답"},
		{Timestamp: base.Add(time.Minute), Prompt: voiceInputPrefix + "둘째 질문", Response: "같은 답"},
		{Timestamp: base.Add(2 * time.Minute), Prompt: "셋째 질문", Response: "같은 답", Images: []string{"x/y.png"}},
		{Timestamp: base.Add(3 * time.Minute), Prompt: "넷째 질문", Response: "아주 긴 답의 앞부분\n\n그리고 뒷부분"},
	}
}

func TestFindForkTurn(t *testing.T) {
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	h := forkTestHistory(base)
	cases := []struct {
		name string
		p    forkPoint
		want int
		ok   bool
	}{
		{"timestamp match wins over a later identical text", forkPoint{Role: "assistant", Text: "같은 답", At: base.Add(time.Minute).UnixMilli()}, 1, true},
		{"occurrence 0 is the last identical bubble", forkPoint{Role: "assistant", Text: "같은 답"}, 2, true},
		{"occurrence 1 is the one before", forkPoint{Role: "assistant", Text: "같은 답", Occurrence: 1}, 1, true},
		{"voice prefix is ignored for user prompts", forkPoint{Role: "user", Text: "둘째 질문"}, 1, true},
		{"a chunk of a long reply matches by containment", forkPoint{Role: "assistant", Text: "그리고 뒷부분"}, 3, true},
		{"user text does not match assistant turns", forkPoint{Role: "user", Text: "첫 답"}, -1, false},
		{"unknown text", forkPoint{Role: "assistant", Text: "없는 말"}, -1, false},
		{"empty text", forkPoint{Role: "assistant", Text: "  "}, -1, false},
		{"bad role", forkPoint{Role: "system", Text: "첫 답"}, -1, false},
	}
	for _, tc := range cases {
		got, ok := findForkTurn(h, tc.p)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got (%d,%v), want (%d,%v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestForkTitle(t *testing.T) {
	for in, want := range map[string]string{
		"리팩터링":        "리팩터링 (갈래)",
		"리팩터링 (갈래)":   "리팩터링 (갈래 2)",
		"리팩터링 (갈래 2)": "리팩터링 (갈래 3)",
		"":            "대화 (갈래)",
		"  공백 제목  ":   "공백 제목 (갈래)",
	} {
		if got := forkTitle(in); got != want {
			t.Errorf("forkTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// Forking at an assistant reply copies every turn up to and including it; the
// new conversation inherits folder/backend/model, starts a fresh session, and
// the source is left untouched.
func TestWebFork_AtAssistantReply(t *testing.T) {
	st := newTestStore(t)
	src, err := st.NewWebConv("설계 논의")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	src, _ = st.GetWebConv(src.ID)
	src.History = forkTestHistory(base)
	src.WorkDir = t.TempDir()
	src.Backend = "codex"
	src.PinnedModel = "sonnet"
	src.Started = true
	if err := st.UpdateWebConv(src); err != nil {
		t.Fatal(err)
	}
	b := &Bot{store: st, out: NewHub()}

	fork, err := b.webFork(WebTarget(src.ID), forkPoint{Role: "assistant", Text: "같은 답", Occurrence: 1})
	if err != nil {
		t.Fatalf("webFork: %v", err)
	}
	got, ok := st.GetWebConv(fork.ID)
	if !ok {
		t.Fatal("fork not persisted")
	}
	if got.ID == src.ID {
		t.Fatal("fork must be a new conversation")
	}
	if got.Title != "설계 논의 (갈래)" {
		t.Errorf("title = %q", got.Title)
	}
	if len(got.History) != 2 || got.History[1].Response != "같은 답" || !strings.HasSuffix(got.History[1].Prompt, "둘째 질문") {
		t.Errorf("history = %+v, want the first two turns intact", got.History)
	}
	if got.WorkDir != src.WorkDir || got.Backend != "codex" || got.PinnedModel != "sonnet" {
		t.Errorf("inherited settings = dir %q backend %q model %q", got.WorkDir, got.Backend, got.PinnedModel)
	}
	if got.Started || got.SessionID == "" || got.SessionID == src.SessionID {
		t.Errorf("fork must start a fresh session: started=%v session=%q (source %q)", got.Started, got.SessionID, src.SessionID)
	}
	if st.GetActive().ConversationID != got.ID {
		t.Error("fork should become the active web conversation")
	}
	// Its first turn will not resume, so the copied history is what gets
	// inlined into the prompt — the existing fresh-session path.
	if inl := historyForContext(got.History, got.Started); len(inl) != 2 {
		t.Errorf("first-turn context = %d turns, want 2", len(inl))
	}
	prompt := buildContextPrompt("새 질문", "", "", "", "mem.md", historyForContext(got.History, got.Started))
	if !strings.Contains(prompt, "첫 질문") || !strings.Contains(prompt, "같은 답") || strings.Contains(prompt, "셋째 질문") {
		t.Errorf("first-turn prompt must carry the forked history only:\n%s", prompt)
	}
	// Source untouched.
	after, _ := st.GetWebConv(src.ID)
	if len(after.History) != 4 || after.Title != "설계 논의" {
		t.Errorf("source changed: %d turns, title %q", len(after.History), after.Title)
	}
}

// Forking at a user message keeps that request but drops the answer (and its
// screenshots/changes) so the fork can be answered differently.
func TestWebFork_AtUserMessage(t *testing.T) {
	st := newTestStore(t)
	src, _ := st.NewWebConv("t")
	src, _ = st.GetWebConv(src.ID)
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	src.History = forkTestHistory(base)
	src.History[2].Changes = &TurnChanges{}
	_ = st.UpdateWebConv(src)
	b := &Bot{store: st, out: NewHub()}

	fork, err := b.webFork(WebTarget(src.ID), forkPoint{Role: "user", Text: "셋째 질문", At: base.Add(2 * time.Minute).UnixMilli()})
	if err != nil {
		t.Fatalf("webFork: %v", err)
	}
	got, _ := st.GetWebConv(fork.ID)
	if len(got.History) != 3 {
		t.Fatalf("history = %d turns, want 3", len(got.History))
	}
	last := got.History[2]
	if last.Prompt != "셋째 질문" || last.Response != "" || last.Images != nil || last.Changes != nil {
		t.Errorf("last turn = %+v, want the prompt only", last)
	}
}

// The telegram stream can be forked too; the fork is a local (web) conversation.
func TestWebFork_FromTelegram(t *testing.T) {
	st := newTestStore(t)
	tc := st.TelegramConversation()
	if tc == nil {
		tc = &Conversation{ID: "tg", Title: "텔레그램"}
	}
	tc.History = []ConversationTurn{{Timestamp: time.Now(), Prompt: "안녕", Response: "반가워요"}}
	if err := st.UpdateTelegramConversation(tc); err != nil {
		t.Fatal(err)
	}
	b := &Bot{store: st, out: NewHub()}
	fork, err := b.webFork(TelegramTarget(), forkPoint{Role: "assistant", Text: "반가워요"})
	if err != nil {
		t.Fatalf("webFork: %v", err)
	}
	if got, ok := st.GetWebConv(fork.ID); !ok || len(got.History) != 1 {
		t.Errorf("telegram fork = %+v", got)
	}
}

func TestWebFork_Errors(t *testing.T) {
	st := newTestStore(t)
	b := &Bot{store: st, out: NewHub()}
	if _, err := b.webFork(WebTarget("nope"), forkPoint{Role: "user", Text: "x"}); err == nil {
		t.Error("unknown source must fail")
	}
	src, _ := st.NewWebConv("t")
	if _, err := b.webFork(WebTarget(src.ID), forkPoint{Role: "user", Text: "없는 메시지"}); err == nil {
		t.Error("a message not in the history must fail")
	}
	if n := len(st.ListWebConvs()); n != 1 {
		t.Errorf("a failed fork must not leave a conversation behind: %d convs", n)
	}
}

func TestWebForkResult(t *testing.T) {
	var ok struct {
		OK    bool   `json:"ok"`
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	_ = json.Unmarshal(webForkResult(&Conversation{ID: "7", Title: "a (갈래)"}, nil), &ok)
	if !ok.OK || ok.ID != "7" || ok.Title != "a (갈래)" {
		t.Errorf("ok reply = %+v", ok)
	}
	var bad struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(webForkResult(nil, errString("x")), &bad)
	if bad.OK || bad.Error != "x" {
		t.Errorf("error reply = %+v", bad)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
