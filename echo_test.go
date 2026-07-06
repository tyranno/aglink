package main

import "testing"

func drainWSFrames(ch chan wsFrame) []wsFrame {
	var out []wsFrame
	for {
		select {
		case f := <-ch:
			out = append(out, f)
		default:
			return out
		}
	}
}

// webChannel shows Telegram-origin input as a "user" bubble; web-origin is a
// no-op (the sending tab already rendered it locally → no double echo).
func TestWebChannelEchoUser_Gating(t *testing.T) {
	w := &webChannel{send: make(chan wsFrame, 8), cancel: func() {}}
	w.EchoUser(7, "from telegram", OriginTelegram)
	w.EchoUser(7, "from web", OriginWeb) // must be a no-op

	frames := drainWSFrames(w.send)
	if len(frames) != 1 {
		t.Fatalf("expected exactly 1 frame (telegram-origin only), got %d: %+v", len(frames), frames)
	}
	if frames[0].Type != "user" || frames[0].Text != "from telegram" {
		t.Errorf("frame = %+v, want {user, 'from telegram'}", frames[0])
	}
}

// telegramChannel must NOT touch the (nil) API for telegram-origin input — the
// user's own message is already in their client. A no-op means no nil-deref panic.
func TestTelegramChannelEchoUser_TelegramOriginNoOp(t *testing.T) {
	tc := &telegramChannel{api: nil}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("telegram-origin EchoUser must be a no-op, but it called Send (panic: %v)", r)
		}
	}()
	tc.EchoUser(7, "user's own telegram message", OriginTelegram)
}

// Hub.EchoUser fans to every channel (global + per-chat); each channel decides
// whether to act based on origin.
func TestHubEchoUser_FansToAll(t *testing.T) {
	h := NewHub()
	g := &recCh{}
	w := &recCh{}
	other := &recCh{}
	h.RegisterGlobal(g)
	h.Register(7, w)
	h.Register(99, other)

	h.EchoUser(7, "hi", OriginWeb)

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
