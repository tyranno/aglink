package main

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *fileStore {
	t.Helper()
	return NewFileStore(filepath.Join(t.TempDir(), "store.json"))
}

func TestStore_AddProject_PersistAndReload(t *testing.T) {
	dir := t.TempDir() // existing directory to register
	path := filepath.Join(t.TempDir(), "store.json")

	s := NewFileStore(path)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProject("myapp", dir); err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	// Reload from disk into a fresh store.
	s2 := NewFileStore(path)
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	p, ok := s2.GetProject("myapp")
	if !ok {
		t.Fatal("project not persisted")
	}
	if p.Path == "" {
		t.Error("path empty after reload")
	}
}

func TestStore_AddProject_BadPath(t *testing.T) {
	s := newTestStore(t)
	_ = s.Load()
	if err := s.AddProject("x", filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for nonexistent dir")
	}
}

func TestStore_AddProject_Duplicate(t *testing.T) {
	s := newTestStore(t)
	_ = s.Load()
	dir := t.TempDir()
	if err := s.AddProject("a", dir); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProject("a", dir); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestStore_NewConversation_UniqueIDsAndUUID(t *testing.T) {
	s := newTestStore(t)
	_ = s.Load()
	dir := t.TempDir()
	if err := s.AddProject("a", dir); err != nil {
		t.Fatal(err)
	}
	c1, err := s.NewConversation("a", "first", "")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := s.NewConversation("a", "second", "")
	if err != nil {
		t.Fatal(err)
	}
	if c1.ID == c2.ID {
		t.Errorf("conversation IDs not unique: %s == %s", c1.ID, c2.ID)
	}
	if c1.SessionID == "" || c1.SessionID == c2.SessionID {
		t.Errorf("session UUIDs invalid: %q / %q", c1.SessionID, c2.SessionID)
	}
	if c1.Started {
		t.Error("new conversation should not be Started")
	}
}

func TestStore_UpdateAndGetConversation(t *testing.T) {
	s := newTestStore(t)
	_ = s.Load()
	dir := t.TempDir()
	_ = s.AddProject("a", dir)
	c, _ := s.NewConversation("a", "t", "")
	c.Started = true
	c.Summary = "done"
	if err := s.UpdateConversation("a", c); err != nil {
		t.Fatal(err)
	}
	got, ok := s.GetConversation("a", c.ID)
	if !ok || !got.Started || got.Summary != "done" {
		t.Errorf("update not persisted: %+v", got)
	}
}

func TestStore_RemoveProject_ClearsActive(t *testing.T) {
	s := newTestStore(t)
	_ = s.Load()
	dir := t.TempDir()
	_ = s.AddProject("a", dir)
	_ = s.SetActive("a", "1")
	if err := s.RemoveProject("a"); err != nil {
		t.Fatal(err)
	}
	if s.GetActive().Project != "" {
		t.Error("active should be cleared after removing its project")
	}
}

func TestNextConvID(t *testing.T) {
	convs := map[string]*Conversation{"1": {}, "2": {}, "5": {}}
	if got := nextConvID(convs); got != "6" {
		t.Errorf("nextConvID = %s, want 6", got)
	}
	if got := nextConvID(map[string]*Conversation{}); got != "1" {
		t.Errorf("nextConvID(empty) = %s, want 1", got)
	}
}
