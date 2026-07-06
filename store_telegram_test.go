package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStore_LegacyFile_BackedUpAndReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	// A legacy file with no schemaVersion and an old telegram conversation.
	legacy := `{"projects":{"myapp":{"path":"` + filepath.ToSlash(dir) + `","conversations":{"1":{"id":"1","title":"old","origin":"telegram"}}}},"active":{"project":"myapp","conversationId":"1"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	st := NewFileStore(path)
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	// Legacy schema (version 0) → backed up and reset to empty new schema.
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Errorf("expected store.json.bak backup, got %v", err)
	}
	if len(st.ListProjects()) != 0 {
		t.Errorf("legacy data must be discarded, got %d projects", len(st.ListProjects()))
	}
}

func TestStore_TelegramConversation_GetOrCreate(t *testing.T) {
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	c1 := st.TelegramConversation()
	if c1 == nil || c1.SessionID == "" {
		t.Fatal("telegram conversation must be created with a session id")
	}
	c2 := st.TelegramConversation()
	if c2.ID != c1.ID || c2.SessionID != c1.SessionID {
		t.Errorf("TelegramConversation must be a singleton, got %q then %q", c1.SessionID, c2.SessionID)
	}
}

func TestStore_TelegramActiveProject_Persists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	st := NewFileStore(path)
	_ = st.Load()
	if err := st.SetTelegramActiveProject("myapp"); err != nil {
		t.Fatal(err)
	}
	// Reload from disk to confirm persistence + schema version written.
	st2 := NewFileStore(path)
	if err := st2.Load(); err != nil {
		t.Fatal(err)
	}
	if st2.TelegramActiveProject() != "myapp" {
		t.Errorf("telegram active project = %q, want myapp", st2.TelegramActiveProject())
	}
	b, _ := os.ReadFile(path)
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	if raw["schemaVersion"] == nil {
		t.Error("persisted store must carry schemaVersion")
	}
}
