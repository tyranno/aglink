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
