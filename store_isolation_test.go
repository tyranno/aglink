package main

import (
	"os"
	"path/filepath"
	"testing"
)

func isoStore(t *testing.T) (*fileStore, string) {
	t.Helper()
	dir := t.TempDir()
	st := NewFileStore(filepath.Join(dir, "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(dir, "alpha")
	if err := os.Mkdir(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := st.AddProject("alpha", proj); err != nil {
		t.Fatal(err)
	}
	return st, proj
}

// Read accessors hand out the store's own objects. Callers then read and mutate
// them outside the store's lock, while writers mutate the same objects under it
// — so the lock protects nothing. Worse, buildConversationsResponse ranges over
// a project's Conversations map while NewConversation writes to it, which the Go
// runtime kills the process for.
//
// A reader must get a copy it cannot use to reach back into the store.
func TestStore_ListProjectsReturnsIsolatedCopies(t *testing.T) {
	st, _ := isoStore(t)
	if _, err := st.NewConversation("alpha", "첫 대화", ""); err != nil {
		t.Fatal(err)
	}

	got := st.ListProjects()["alpha"]
	got.Path = "hijacked"
	got.Conversations["injected"] = &Conversation{ID: "injected"}

	live, _ := st.GetProject("alpha")
	if live.Path == "hijacked" {
		t.Error("mutating a listed project reached into the store")
	}
	if _, bad := live.Conversations["injected"]; bad {
		t.Error("mutating a listed project's conversation map reached into the store")
	}
}

func TestStore_GetProjectReturnsIsolatedCopy(t *testing.T) {
	st, _ := isoStore(t)
	p, ok := st.GetProject("alpha")
	if !ok {
		t.Fatal("project missing")
	}
	p.Path = "hijacked"
	if again, _ := st.GetProject("alpha"); again.Path == "hijacked" {
		t.Error("mutating a fetched project reached into the store")
	}
}

// runWorker appends to conv.History with no store lock held, while
// HistorySnapshot copies it under one. Handing out the live conversation is what
// makes that a race; a copy makes the writer's mutations local until it saves.
func TestStore_TelegramConversationReturnsIsolatedCopy(t *testing.T) {
	st, _ := isoStore(t)

	tc := st.TelegramConversation()
	tc.Title = "hijacked"
	tc.History = append(tc.History, ConversationTurn{Prompt: "p", Response: "r"})

	again := st.TelegramConversation()
	if again.Title == "hijacked" {
		t.Error("mutating the returned telegram conversation reached into the store")
	}
	if len(again.History) != 0 {
		t.Errorf("history leaked into the store without an Update: %d turn(s)", len(again.History))
	}

	// The intended flow still works: mutate the copy, then persist it.
	tc2 := st.TelegramConversation()
	tc2.Title = "saved"
	if err := st.UpdateTelegramConversation(tc2); err != nil {
		t.Fatal(err)
	}
	if st.TelegramConversation().Title != "saved" {
		t.Error("UpdateTelegramConversation must persist the caller's copy")
	}
}

func TestStore_GetWebConvReturnsIsolatedCopy(t *testing.T) {
	st, _ := isoStore(t)
	c, err := st.NewWebConv("주제")
	if err != nil {
		t.Fatal(err)
	}

	got, ok := st.GetWebConv(c.ID)
	if !ok {
		t.Fatal("conv missing")
	}
	got.Title = "hijacked"
	got.History = append(got.History, ConversationTurn{Prompt: "p"})

	again, _ := st.GetWebConv(c.ID)
	if again.Title == "hijacked" || len(again.History) != 0 {
		t.Error("mutating a fetched web conversation reached into the store")
	}

	// Persisting the copy still works.
	again.Title = "saved"
	if err := st.UpdateWebConv(again); err != nil {
		t.Fatal(err)
	}
	if final, _ := st.GetWebConv(c.ID); final.Title != "saved" {
		t.Error("UpdateWebConv must persist the caller's copy")
	}
}

// buildConversationsResponse ranges over each project's Conversations map while
// a worker turn may be creating a conversation in it. Handing out the live map
// made that "concurrent map iteration and map write" — a runtime fatal that
// takes the whole process down, not a recoverable error. Copies make it safe.
//
// If ListProjects ever stops copying, this test does not fail politely: the
// runtime kills the test binary. That is the point.
func TestStore_ConcurrentListAndCreateIsSafe(t *testing.T) {
	st, _ := isoStore(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 300; i++ {
			if _, err := st.NewConversation("alpha", "", ""); err != nil {
				return
			}
		}
	}()

	for i := 0; i < 300; i++ {
		for _, p := range st.ListProjects() {
			for id := range p.Conversations {
				_ = id
			}
		}
	}
	<-done
}
