package main

import "testing"

// telegramChannel must NOT touch the (nil) API for telegram-origin input — the
// user's own message is already in their client. A no-op means no nil-deref panic.
func TestTelegramChannelEchoUser_TelegramOriginNoOp(t *testing.T) {
	tc := &telegramChannel{api: nil}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("telegram-origin EchoUser must be a no-op, but it called Send (panic: %v)", r)
		}
	}()
	tc.EchoUser(TelegramTarget(), 7, "user's own telegram message", OriginTelegram)
}

// Hub.EchoUser fans to every channel for the telegram stream (global + per-chat);
// each channel decides whether to act based on origin.
func TestHubEchoUser_FansToAll(t *testing.T) {
	h := NewHub()
	g := &recCh{}
	w := &recCh{}
	other := &recCh{}
	h.RegisterGlobal(g)
	h.Register(7, w)
	h.Register(99, other)

	h.EchoUser(TelegramTarget(), 7, "hi", OriginWeb)

	if len(g.echoes) != 1 || g.echoes[0] != "web:hi" {
		t.Errorf("global should receive echo, got %v", g.echoes)
	}
	if len(w.echoes) != 1 || w.echoes[0] != "web:hi" {
		t.Errorf("chat-7 channel should receive echo, got %v", w.echoes)
	}
	if len(other.echoes) != 0 {
		t.Errorf("chat-99 channel must not receive chat-7 echo, got %v", other.echoes)
	}
}

// Input typed into a web topic must not be mirrored into Telegram — only input
// addressed to the telegram stream is.
func TestDispatchTargeted_WebTopicInputNotEchoedToTelegram(t *testing.T) {
	b := newParallelTestBot(0)
	b.out = NewHub()
	tg := &recCh{}
	web := &recCh{}
	b.out.RegisterGlobal(tg)
	b.out.Register(7, web)

	wt := Target{Kind: TargetWeb, ID: "c1"}
	b.dispatchTargeted(7, "secret web note", &wt)

	if len(tg.echoes) != 0 {
		t.Errorf("web-topic input must not be echoed to telegram, got %v", tg.echoes)
	}
	if len(web.echoes) != 1 {
		t.Errorf("web channel should still receive the echo, got %v", web.echoes)
	}
}

// dispatchText mirrors the user's input via the Hub before dispatching.
func TestDispatchText_EchoesUserInput(t *testing.T) {
	b := &Bot{}
	b.out = NewHub()
	rec := &recCh{}
	b.out.Register(7, rec)
	b.dispatchHook = func(int64, string) {} // skip real dispatch

	b.dispatchText(7, "hello", OriginTelegram)

	if len(rec.echoes) != 1 || rec.echoes[0] != "telegram:hello" {
		t.Errorf("dispatchText should echo the user input to the Hub, got %v", rec.echoes)
	}
}

// dispatchTargeted must also mirror the user's input via the Hub (Task 5 review
// fix: this mirror was dropped when dispatchTargeted bypassed dispatchText's
// queue path — without it, web-typed input stopped showing up in Telegram).
// MaxWorkers=0 forces dispatch()'s queueing branch, so this stays deterministic
// (see TestDispatchTargeted_RoutesThroughQueue in bot_parallel_test.go) — the
// echo itself happens synchronously before the message is even enqueued.
func TestDispatchTargeted_EchoesUserInput(t *testing.T) {
	b := newParallelTestBot(0)
	b.out = NewHub()
	rec := &recCh{}
	b.out.Register(7, rec)

	b.dispatchTargeted(7, "hello from web", nil)

	if len(rec.echoes) != 1 || rec.echoes[0] != "web:hello from web" {
		t.Errorf("dispatchTargeted should echo the user input to the Hub, got %v", rec.echoes)
	}
}
