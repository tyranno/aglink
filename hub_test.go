package main

import (
	"errors"
	"sync"
	"testing"
)

// recCh is a ChannelSender test double recording calls; optional forced error.
type recCh struct {
	mu      sync.Mutex
	texts   []string
	photos  int
	typings int
	echoes  []string // "origin:text" for each EchoUser call
	targets []Target // the Target each Send carried
	sendErr error
}

func (r *recCh) Send(tgt Target, _ int64, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	r.targets = append(r.targets, tgt)
	return r.sendErr
}
func (r *recCh) SendPhoto(_ Target, _ int64, _ []byte, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.photos++
	return nil
}
func (r *recCh) Typing(_ Target, _ int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.typings++
}
func (r *recCh) Done(Target, int64) {}
func (r *recCh) EchoUser(_ Target, _ int64, text, origin string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.echoes = append(r.echoes, origin+":"+text)
}

func webTarget(id string) Target { return Target{Kind: TargetWeb, ID: id} }

func TestHubFanOut_GlobalPlusPerChat(t *testing.T) {
	h := NewHub()
	g := &recCh{}     // telegram-like global
	w := &recCh{}     // web, bound to chat 7
	other := &recCh{} // web bound to a different chat
	h.RegisterGlobal(g)
	h.Register(7, w)
	h.Register(99, other)

	_ = h.Send(TelegramTarget(), 7, "hi")
	_ = h.SendPhoto(TelegramTarget(), 7, []byte("png"), "cap")
	h.Typing(TelegramTarget(), 7)

	if len(g.texts) != 1 || g.texts[0] != "hi" {
		t.Errorf("global should get the text, got %v", g.texts)
	}
	if len(w.texts) != 1 || g.photos != 1 || w.photos != 1 || g.typings != 1 || w.typings != 1 {
		t.Errorf("chat-7 channels should receive; g=%+v w=%+v", g, w)
	}
	if len(other.texts) != 0 || other.photos != 0 || other.typings != 0 {
		t.Errorf("chat-99 channel must NOT receive chat-7 traffic, got %+v", other)
	}
}

// A web topic is invisible from Telegram: its output must reach only the web
// channels, never a global (Telegram) channel. This is the isolation that was
// missing — every web-topic reply used to be posted into Telegram as well.
func TestHubFanOut_WebTargetNeverReachesGlobal(t *testing.T) {
	h := NewHub()
	tg := &recCh{} // telegram (global)
	web := &recCh{}
	h.RegisterGlobal(tg)
	h.Register(7, web)

	_ = h.Send(webTarget("conv-1"), 7, "web only")
	_ = h.SendPhoto(webTarget("conv-1"), 7, []byte("png"), "cap")
	h.Typing(webTarget("conv-1"), 7)
	h.Done(webTarget("conv-1"), 7)
	h.EchoUser(webTarget("conv-1"), 7, "typed in web", OriginWeb)

	if len(tg.texts) != 0 || tg.photos != 0 || tg.typings != 0 || len(tg.echoes) != 0 {
		t.Errorf("telegram must not receive web-topic output, got %+v", tg)
	}
	if len(web.texts) != 1 || web.texts[0] != "web only" || web.photos != 1 || web.typings != 1 {
		t.Errorf("web channel should receive its own topic output, got %+v", web)
	}
}

// The telegram stream still reaches both: Telegram itself, and the browser,
// which renders it as one of its conversations.
func TestHubFanOut_TelegramTargetReachesBoth(t *testing.T) {
	h := NewHub()
	tg := &recCh{}
	web := &recCh{}
	h.RegisterGlobal(tg)
	h.Register(7, web)

	_ = h.Send(TelegramTarget(), 7, "stream")

	if len(tg.texts) != 1 || len(web.texts) != 1 {
		t.Fatalf("telegram stream should reach both, tg=%v web=%v", tg.texts, web.texts)
	}
	if web.targets[0].IsWeb() {
		t.Errorf("frame handed to the web channel must be tagged telegram, got %+v", web.targets[0])
	}
}

// An empty/unknown Kind is the telegram stream, so an outdated client can never
// silently address a web topic.
func TestHubFanOut_EmptyKindIsTelegramStream(t *testing.T) {
	h := NewHub()
	tg := &recCh{}
	h.RegisterGlobal(tg)

	_ = h.Send(Target{}, 7, "no kind")

	if len(tg.texts) != 1 {
		t.Errorf("empty Kind must fan out to the telegram stream, got %v", tg.texts)
	}
}

func TestHubUnregister(t *testing.T) {
	h := NewHub()
	w := &recCh{}
	h.Register(7, w)
	h.Unregister(7, w)
	_ = h.Send(TelegramTarget(), 7, "hi")
	if len(w.texts) != 0 {
		t.Errorf("unregistered channel must not receive, got %v", w.texts)
	}
}

func TestHubErrorIsolation(t *testing.T) {
	h := NewHub()
	bad := &recCh{sendErr: errors.New("boom")}
	good := &recCh{}
	h.RegisterGlobal(bad)
	h.Register(7, good)
	if err := h.Send(TelegramTarget(), 7, "hi"); err != nil {
		t.Errorf("Hub.Send should swallow per-channel errors, got %v", err)
	}
	if len(good.texts) != 1 {
		t.Errorf("a failing channel must not block others, got %v", good.texts)
	}
}

func TestHubFanOut_MultipleWebChannels(t *testing.T) {
	h := NewHub()
	g := &recCh{}     // telegram-like global
	w1 := &recCh{}    // web tab #1 on chat 7
	w2 := &recCh{}    // web tab #2 on chat 7
	other := &recCh{} // web tab on a different chat (8)
	h.RegisterGlobal(g)
	h.Register(7, w1)
	h.Register(7, w2)
	h.Register(8, other)

	_ = h.Send(TelegramTarget(), 7, "hi")

	if len(g.texts) != 1 || g.texts[0] != "hi" {
		t.Errorf("global should get the text, got %v", g.texts)
	}
	if len(w1.texts) != 1 || w1.texts[0] != "hi" {
		t.Errorf("chat-7 tab #1 should receive, got %v", w1.texts)
	}
	if len(w2.texts) != 1 || w2.texts[0] != "hi" {
		t.Errorf("chat-7 tab #2 should receive, got %v", w2.texts)
	}
	if len(other.texts) != 0 {
		t.Errorf("chat-8 channel must NOT receive chat-7 traffic, got %v", other.texts)
	}
}

// Bot.For binds a Target to the sender, so the manager's unchanged
// s.Send(chatID, …) call sites address the right conversation.
func TestBotFor_BindsTargetToSender(t *testing.T) {
	b := &Bot{}
	b.out = NewHub()
	tg := &recCh{}
	web := &recCh{}
	b.out.RegisterGlobal(tg)
	b.out.Register(7, web)

	_ = b.For(webTarget("c1")).Send(7, "to web topic")
	if len(tg.texts) != 0 {
		t.Errorf("web-bound sender must not reach telegram, got %v", tg.texts)
	}
	if len(web.texts) != 1 || web.targets[0].ID != "c1" {
		t.Errorf("web channel should get the tagged frame, got %+v", web)
	}

	// Bot itself keeps addressing the telegram stream.
	_ = b.Send(7, "to telegram")
	if len(tg.texts) != 1 || tg.texts[0] != "to telegram" {
		t.Errorf("Bot.Send must address the telegram stream, got %v", tg.texts)
	}
}

// bindTarget leaves a sender that can't rebind untouched (test fakes, relays).
func TestBindTarget_PassthroughForPlainSender(t *testing.T) {
	plain := &recMsgSender{}
	if got := bindTarget(plain, webTarget("x")); got != MessageSender(plain) {
		t.Error("a sender without For() must be returned unchanged")
	}
}

// recMsgSender is a MessageSender with no target binding.
type recMsgSender struct{ texts []string }

func (r *recMsgSender) Send(_ int64, text string) error { r.texts = append(r.texts, text); return nil }
func (r *recMsgSender) Typing(int64)                    {}
func (r *recMsgSender) Done(int64)                      {}

func TestBotHubAccessorRegistersTelegram(t *testing.T) {
	// A freshly constructed Bot must expose a Hub that already has the Telegram
	// global channel registered, so registering a web channel + sending reaches both.
	// We only assert the web side here (Telegram needs a live API), proving Bot.Send
	// delegates to the Hub rather than calling tgbotapi directly.
	b := &Bot{}
	b.out = NewHub() // mimic NewBot's hub init without a real tgbotapi client
	w := &recCh{}
	b.out.Register(55, w)
	if err := b.Send(55, "via hub"); err != nil {
		t.Fatal(err)
	}
	if b.Hub() != b.out {
		t.Error("Hub() must return the bot's hub")
	}
	if len(w.texts) != 1 || w.texts[0] != "via hub" {
		t.Errorf("Bot.Send must delegate to Hub, got %v", w.texts)
	}
}

// A command must answer only the conversation it was sent from. A "!" command
// typed into a web topic used to reply through b.Send, which always addresses
// the telegram stream — so the answer showed up in Telegram instead.
func TestHandleCommand_RepliesOnlyToRequestingConversation(t *testing.T) {
	b := &Bot{}
	b.out = NewHub()
	tg := &recCh{}
	web := &recCh{}
	b.out.RegisterGlobal(tg)
	b.out.Register(7, web)

	b.handleCommand(7, "!help", OriginWeb, webTarget("c1"))

	if len(tg.texts) != 0 {
		t.Errorf("a web-topic command must not answer in telegram, got %v", tg.texts)
	}
	if len(web.texts) != 1 {
		t.Fatalf("the web topic should get the answer, got %v", web.texts)
	}
	if !web.targets[0].IsWeb() || web.targets[0].ID != "c1" {
		t.Errorf("answer target = %+v, want web/c1", web.targets[0])
	}
}

// The same command from Telegram still answers on the telegram stream (and the
// browser, which renders that stream as one of its conversations).
func TestHandleCommand_TelegramCommandAnswersTelegramStream(t *testing.T) {
	b := &Bot{}
	b.out = NewHub()
	tg := &recCh{}
	web := &recCh{}
	b.out.RegisterGlobal(tg)
	b.out.Register(7, web)

	b.handleCommand(7, "!help", OriginTelegram, TelegramTarget())

	if len(tg.texts) != 1 {
		t.Errorf("telegram command should answer in telegram, got %v", tg.texts)
	}
	if len(web.texts) != 1 || web.targets[0].IsWeb() {
		t.Errorf("browser should see it tagged as the telegram stream, got %+v", web.targets)
	}
}

// Web-conversation management is browser-only: its confirmations must never
// reach Telegram, whatever target the requester sent.
func TestWebNew_ConfirmationNeverReachesTelegram(t *testing.T) {
	st := newTestStore(t)
	b := &Bot{store: st}
	b.out = NewHub()
	tg := &recCh{}
	web := &recCh{}
	b.out.RegisterGlobal(tg)
	b.out.Register(7, web)

	b.webNew(b.ReplyTo(AsWebTarget(TelegramTarget())), 7, "topic")

	if len(tg.texts) != 0 {
		t.Errorf("web_new confirmation must not reach telegram, got %v", tg.texts)
	}
	if len(web.texts) != 1 {
		t.Errorf("browser should get the confirmation, got %v", web.texts)
	}
}

// AsWebTarget coerces a telegram/empty target to web, so a web-only reply can
// never be addressed to the telegram stream.
func TestAsWebTarget(t *testing.T) {
	if got := AsWebTarget(TelegramTarget()); !got.IsWeb() {
		t.Errorf("AsWebTarget(telegram) = %+v, want web", got)
	}
	if got := AsWebTarget(Target{}); !got.IsWeb() {
		t.Errorf("AsWebTarget(empty) = %+v, want web", got)
	}
	wt := webTarget("keep")
	if got := AsWebTarget(wt); got.ID != "keep" {
		t.Errorf("AsWebTarget must preserve an existing web target, got %+v", got)
	}
}
