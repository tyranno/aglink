package main

import (
	"path/filepath"
	"testing"
)

func TestWebConv_CRUD(t *testing.T) {
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	c, err := st.NewWebConv("첫 웹 대화")
	if err != nil || c.ID == "" || c.SessionID == "" {
		t.Fatalf("NewWebConv failed: %v %+v", err, c)
	}
	if c.Origin != OriginWeb {
		t.Errorf("web conv origin should be web, got %q", c.Origin)
	}
	c.WorkDir = "C:/tmp/x"
	if err := st.UpdateWebConv(c); err != nil {
		t.Fatal(err)
	}
	got, ok := st.GetWebConv(c.ID)
	if !ok || got.WorkDir != "C:/tmp/x" {
		t.Errorf("web conv workdir not persisted: %+v", got)
	}
	if len(st.ListWebConvs()) != 1 {
		t.Errorf("expected 1 web conv, got %d", len(st.ListWebConvs()))
	}
	if err := st.DeleteWebConv(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.GetWebConv(c.ID); ok {
		t.Error("web conv should be deleted")
	}
}

func TestHistorySnapshot_WebConv(t *testing.T) {
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	c, _ := st.NewWebConv("t")
	c.History = []ConversationTurn{{Prompt: "q", Response: "a"}}
	_ = st.UpdateWebConv(c)
	turns := st.HistorySnapshot(Target{Kind: "web", ID: c.ID})
	if len(turns) != 1 || turns[0].Prompt != "q" {
		t.Errorf("web history snapshot wrong: %+v", turns)
	}
}

func TestNewWebConv_DefaultBackendIsInherited(t *testing.T) {
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	if err := st.SetStoredBackend("codex"); err != nil {
		t.Fatal(err)
	}
	c, err := st.NewWebConv("default backend")
	if err != nil {
		t.Fatal(err)
	}
	if c.Backend != "" {
		t.Fatalf("new web conversations should inherit the default backend with an empty override, got %q", c.Backend)
	}
}
