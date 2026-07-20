package main

import "testing"

// webRename must report success/failure synchronously and have the new title
// durable in the store by the time it returns — no goroutine, no timing window.
// This is what lets the control API acknowledge the rename so a fast page reload
// can't race ahead of the write. On the old code (go-wrapped, no return value) a
// caller had no way to observe either.
func TestWebRename_SucceedsAndPersistsSynchronously(t *testing.T) {
	st := newTestStore(t)
	c, err := st.NewWebConv("old title")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	b := &Bot{store: st, out: NewHub()}

	if rerr := b.webRename(b.ReplyTo(WebTarget(c.ID)), 1, c.ID, "new title"); rerr != nil {
		t.Fatalf("rename should succeed, got %v", rerr)
	}
	// Durable immediately on return — the whole point of the synchronous path.
	got, ok := st.GetWebConv(c.ID)
	if !ok {
		t.Fatal("conversation vanished after rename")
	}
	if got.Title != "new title" {
		t.Errorf("title = %q, want %q — not persisted by the time webRename returned", got.Title, "new title")
	}
}

func TestWebRename_MissingConversationReturnsError(t *testing.T) {
	st := newTestStore(t)
	b := &Bot{store: st, out: NewHub()}
	if err := b.webRename(b.ReplyTo(WebTarget("nope")), 1, "nope", "x"); err == nil {
		t.Error("renaming a non-existent conversation must return an error")
	}
}

func TestWebRename_EmptyTitleReturnsError(t *testing.T) {
	st := newTestStore(t)
	c, err := st.NewWebConv("keep")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	b := &Bot{store: st, out: NewHub()}
	if err := b.webRename(b.ReplyTo(WebTarget(c.ID)), 1, c.ID, ""); err == nil {
		t.Error("empty title must return an error")
	}
	// And the original title must be untouched.
	if got, _ := st.GetWebConv(c.ID); got.Title != "keep" {
		t.Errorf("title changed to %q on a rejected empty rename", got.Title)
	}
}
