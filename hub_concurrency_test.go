package main

import (
	"sync"
	"testing"
)

// Unregister rewrites the per-chat slice in place (append(list[:i], list[i+1:]...)).
// A fan-out that already read its recipient list must not see that mutation:
// targets() copies the elements out, so the copy stays intact. If targets ever
// returns the live slice, this catches it.
func TestHubTargets_CopyIsNotAliasedByUnregister(t *testing.T) {
	h := NewHub()
	a, b, c := &recCh{}, &recCh{}, &recCh{}
	h.Register(7, a)
	h.Register(7, b)
	h.Register(7, c)

	snapshot := h.targets(TelegramTarget(), 7) // what a live Send is holding
	if len(snapshot) != 3 {
		t.Fatalf("snapshot len = %d, want 3", len(snapshot))
	}

	h.Unregister(7, b) // shifts c into b's slot in the underlying array

	want := []ChannelSender{a, b, c}
	for i := range want {
		if snapshot[i] != want[i] {
			t.Errorf("snapshot[%d] changed after Unregister: a concurrent Send would "+
				"deliver to the wrong channel (or twice)", i)
		}
	}
	// And the hub itself is correct afterwards.
	if got := h.targets(TelegramTarget(), 7); len(got) != 2 || got[0] != a || got[1] != c {
		t.Errorf("after Unregister, targets = %v, want [a c]", got)
	}
}

// The hub is written from the control server's accept/disconnect paths while
// worker turns fan out through it. Hammer both at once: a data race on perChat
// surfaces as a runtime "concurrent map" fatal error even without -race, which
// this machine cannot run (no cgo toolchain).
func TestHubConcurrentRegisterUnregisterAndSend(t *testing.T) {
	h := NewHub()
	h.RegisterGlobal(&recCh{})

	const chats = 8
	var wg sync.WaitGroup

	for i := 0; i < chats; i++ {
		chatID := int64(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 200; n++ {
				ch := &recCh{}
				h.Register(chatID, ch)
				h.Unregister(chatID, ch)
			}
		}()
	}
	for i := 0; i < chats; i++ {
		chatID := int64(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 200; n++ {
				_ = h.Send(TelegramTarget(), chatID, "x")
				h.Typing(webTarget("w"), chatID)
				h.Done(TelegramTarget(), chatID)
				h.EchoUser(TelegramTarget(), chatID, "x", OriginWeb)
			}
		}()
	}
	wg.Wait()

	// Every channel registered above was unregistered; nothing may be left behind.
	h.mu.RLock()
	left := len(h.perChat)
	h.mu.RUnlock()
	if left != 0 {
		t.Errorf("perChat leaked %d chat(s) after all channels unregistered", left)
	}
}
