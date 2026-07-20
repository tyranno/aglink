package main

import (
	"context"
	"path/filepath"
	"testing"
)

// The project sink persists into Projects[project]; the telegram sink persists
// into the global TelegramConv. runWorker must route saves through the sink.
func TestConvSink_Telegram_PersistsGlobally(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "done", SessionID: "sess-x"}}
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	dir := t.TempDir()
	_ = st.AddProject("myapp", dir)
	m := NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true}))

	tc := st.TelegramConversation()
	sink := m.telegramSink("myapp")
	f := &fakeSender{}
	m.runWorker(context.Background(), 1, "hi", sink, dir, tc, f, fc, "claude")

	got := st.TelegramConversation()
	if !got.Started || len(got.History) != 1 || got.History[0].Prompt != "hi" {
		t.Errorf("telegram conversation not persisted globally: %+v", got)
	}
	// Must NOT have leaked into any project's conversation map.
	p, _ := st.GetProject("myapp")
	if len(p.Conversations) != 0 {
		t.Errorf("telegram turn must not create a project topic, got %d", len(p.Conversations))
	}
}
