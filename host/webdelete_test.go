package main

import "testing"

func TestWebDelete_RemovesConversation(t *testing.T) {
	st := newTestStore(t)
	c, err := st.NewWebConv("temp")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	b := &Bot{store: st}
	b.out = NewHub() // Send → hub with no channels → no-op

	b.webDelete(b.ReplyTo(WebTarget(c.ID)), 1, c.ID)

	if _, ok := st.GetWebConv(c.ID); ok {
		t.Errorf("conversation %s still present after webDelete", c.ID)
	}
}

// TestWebDelete_CancelsActiveTurn is a regression test for: deleting a web
// conversation while a turn was still running did not stop the worker, so a
// trailing frame arrived after the store entry was gone, the client reloaded
// the list and marked the conversation unread — resurrecting the just-deleted
// row with a NEW badge, and only a second delete removed it. webDelete must
// cancel the conversation's own lane (and no other) before removing it.
func TestWebDelete_CancelsActiveTurn(t *testing.T) {
	st := newTestStore(t)
	c, err := st.NewWebConv("temp")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	b := &Bot{store: st}
	b.out = NewHub()

	cancelled := 0
	b.cancels = map[int]*cancelEntry{
		1: {key: "web:" + c.ID, cancel: func() { cancelled++ }},
		2: {key: "web:other-topic", cancel: func() { t.Error("another conversation's lane must not be cancelled") }},
	}

	b.webDelete(b.ReplyTo(WebTarget(c.ID)), 1, c.ID)

	if cancelled != 1 {
		t.Errorf("active turn for the deleted conversation was not cancelled (cancelled=%d, want 1)", cancelled)
	}
	if _, ok := st.GetWebConv(c.ID); ok {
		t.Errorf("conversation %s still present after webDelete", c.ID)
	}
}
