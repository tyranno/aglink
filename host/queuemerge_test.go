package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMergeQueuedHead(t *testing.T) {
	web := Target{Kind: "web", ID: "w1"}
	tg := TelegramTarget()
	m := func(text string) queuedMsg {
		return queuedMsg{chatID: 1, text: text, origin: OriginWeb, target: &web}
	}
	texts := func(q []queuedMsg) []string {
		var out []string
		for _, x := range q {
			out = append(out, x.text)
		}
		return out
	}

	got := mergeQueuedHead([]queuedMsg{m("a"), m("b"), m("c")})
	if len(got) != 1 || got[0].text != "a\n\nb\n\nc" || got[0].merged != 3 {
		t.Fatalf("plain merge = %q (merged=%d)", texts(got), got[0].merged)
	}

	// Boundaries stop the merge without reordering anything behind them.
	bang := m("!status")
	noMerge := m("p2")
	noMerge.noMerge = true
	task := m("task")
	task.isTask = true
	tgNil := queuedMsg{chatID: 1, text: "tg", origin: OriginWeb} // telegram stream, Handle path
	tgWeb := queuedMsg{chatID: 1, text: "tgweb", origin: OriginWeb, target: &tg}
	otherChat := m("other")
	otherChat.chatID = 2
	for name, q := range map[string][]queuedMsg{
		"bang":       {m("a"), m("b"), bang, m("c")},
		"noMerge":    {m("a"), m("b"), noMerge, m("c")},
		"task":       {m("a"), m("b"), task, m("c")},
		"chat":       {m("a"), m("b"), otherChat, m("c")},
		"entrypoint": {tgWeb, tgWeb, tgNil, tgWeb},
	} {
		got := mergeQueuedHead(q)
		if len(got) != 3 || got[0].merged != 2 || got[0].text != q[0].text+"\n\n"+q[1].text ||
			got[1].text != q[2].text || got[2].text != q[3].text {
			t.Errorf("%s: got %q", name, texts(got))
		}
	}

	// A head that can't merge leaves the queue alone.
	if got := mergeQueuedHead([]queuedMsg{noMerge, m("x")}); len(got) != 2 || got[0].merged != 0 {
		t.Errorf("unmergeable head changed the queue: %q", texts(got))
	}
}

// promptClient records every worker prompt and parks the first turn until the
// test releases it.
type promptClient struct {
	mu      sync.Mutex
	prompts []string
	firstIn chan struct{}
	release chan struct{}
}

func (c *promptClient) Route(context.Context, RouteRequest) (RouteDecision, error) {
	return RouteDecision{}, nil
}

func (c *promptClient) Run(_ context.Context, req RunRequest) (RunResult, error) {
	c.mu.Lock()
	c.prompts = append(c.prompts, req.Prompt)
	first := len(c.prompts) == 1
	c.mu.Unlock()
	if first {
		close(c.firstIn)
		<-c.release
	}
	return RunResult{Text: "done"}, nil
}

func (c *promptClient) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.prompts...)
}

// Three web messages sent to the telegram stream while its turn runs, with a
// "!" command in between, become exactly ONE more worker turn carrying all
// three in order; the command is handled immediately (while the first turn
// still runs) and never reaches the worker. Goes through the real web entry
// point (chatcontrol send_text) and the real lane queue.
func TestDispatch_QueuedMessagesMergeIntoOneTurn(t *testing.T) {
	pc := &promptClient{firstIn: make(chan struct{}), release: make(chan struct{})}
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	b := &Bot{
		cfgh:    NewConfigHolder(&Config{MaxWorkers: 2, TimeoutMinutes: 1}),
		cancels: make(map[int]*cancelEntry),
		lanes:   make(map[string]*lane),
		store:   st,
	}
	b.manager = NewManager(pc, nil, st, NewConfigHolder(&Config{ManagerAlways: true}))
	b.out = NewHub()
	ch := &hookCh{}
	b.out.Register(7, ch)
	var cmdMu sync.Mutex
	var cmds []string
	b.commandHook = func(_ int64, text string) {
		cmdMu.Lock()
		cmds = append(cmds, text)
		cmdMu.Unlock()
	}
	s := &chatControlServer{ownerChatID: 7, bot: b, hub: b.out}
	rc := &remoteChatChannel{send: make(chan controlOut, 64), cancel: func() {}}
	send := func(text string) { s.handleInbound(rc, controlIn{Type: "send_text", Text: text, Origin: OriginWeb}) }

	send("AAA first")
	<-pc.firstIn

	// send_text dispatches on a goroutine; wait for each to land so the queue
	// order is the send order.
	send("BBB one")
	waitFor(t, func() bool { return b.queued() == 1 })
	send("!status")
	waitFor(t, func() bool { cmdMu.Lock(); defer cmdMu.Unlock(); return len(cmds) == 1 })
	if b.queued() != 1 {
		t.Fatalf("the ! command entered the lane queue (queued=%d)", b.queued())
	}
	send("CCC two")
	waitFor(t, func() bool { return b.queued() == 2 })
	send("DDD three")
	waitFor(t, func() bool { return b.queued() == 3 })

	close(pc.release)
	waitFor(t, func() bool { return b.active() == 0 && b.queued() == 0 })
	time.Sleep(50 * time.Millisecond) // a wrongly chained extra turn would show up here

	prompts := pc.seen()
	if len(prompts) != 2 {
		t.Fatalf("worker runs = %d, want 2 (first turn + one merged turn)", len(prompts))
	}
	merged := prompts[1]
	if !strings.Contains(merged, "BBB one\n\nCCC two\n\nDDD three") {
		t.Errorf("merged prompt lacks the three messages in order:\n%s", merged)
	}
	if strings.Contains(merged, "!status") {
		t.Errorf("merged prompt contains the ! command:\n%s", merged)
	}
	if !sentContaining(ch.texts, "메시지 3건을 하나로 합쳐") {
		t.Errorf("no merge notice sent: %q", ch.texts)
	}

	// History: one user turn per worker run; the merged one keeps every message.
	h := st.TelegramConversation().History
	if len(h) != 2 || h[1].Prompt != "BBB one\n\nCCC two\n\nDDD three" {
		var ps []string
		for _, x := range h {
			ps = append(ps, x.Prompt)
		}
		t.Errorf("history prompts = %q", ps)
	}
}
