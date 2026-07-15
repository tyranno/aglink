package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// hookCh is a ChannelSender that runs a callback for each text it is handed.
type hookCh struct {
	mu     sync.Mutex
	texts  []string
	onText func(string)
}

func (h *hookCh) Send(_ Target, _ int64, text string) error {
	h.mu.Lock()
	h.texts = append(h.texts, text)
	cb := h.onText
	h.mu.Unlock()
	if cb != nil {
		cb(text)
	}
	return nil
}
func (h *hookCh) SendPhoto(Target, int64, []byte, string) error { return nil }
func (h *hookCh) Typing(Target, int64)                          {}
func (h *hookCh) Done(Target, int64)                            {}
func (h *hookCh) Progress(Target, int64, string)                {}
func (h *hookCh) EchoUser(Target, int64, string, string)        {}

// orderClient records the order turns reach the worker and parks the first one
// until the test releases it. Turns are identified by the marker appearing last
// in the prompt: buildContextPrompt appends the current message after the
// history, so earlier markers are present too and cannot be matched directly.
type orderClient struct {
	mu      sync.Mutex
	order   []string
	firstIn chan struct{} // closed once the first turn is inside Run
	release chan struct{} // closed by the test to let the first turn finish
	started bool
}

func newOrderClient() *orderClient {
	return &orderClient{firstIn: make(chan struct{}), release: make(chan struct{})}
}

func markerOf(prompt string) string {
	best, at := "", -1
	for _, m := range []string{"AAA", "BBB", "CCC"} {
		if i := strings.LastIndex(prompt, m); i > at {
			best, at = m, i
		}
	}
	return best
}

func (c *orderClient) Route(context.Context, RouteRequest) (RouteDecision, error) {
	return RouteDecision{}, nil
}

func (c *orderClient) Run(_ context.Context, req RunRequest) (RunResult, error) {
	c.mu.Lock()
	c.order = append(c.order, markerOf(req.Prompt))
	first := !c.started
	c.started = true
	c.mu.Unlock()

	if first {
		close(c.firstIn)
		<-c.release
	}
	return RunResult{Text: "done"}, nil
}

func (c *orderClient) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.order...)
}

// A message pulled off the queue is announced as starting, so it must actually
// start. It used to be handed back to dispatch, which re-checks capacity — so
// anything that grabbed the freed slot in between pushed the announced message
// back onto the tail, behind the message that stole its slot.
//
// The window is deterministic: the "starting" notice is sent before the
// re-dispatch, so a send issued from inside that notice reproduces it exactly.
// Old code runs AAA, CCC, BBB. Handing the slot over runs AAA, BBB, CCC.
func TestDispatch_QueuedMessageKeepsItsSlotAfterBeingAnnounced(t *testing.T) {
	fc := newOrderClient()

	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	b := &Bot{
		cfgh:    NewConfigHolder(&Config{MaxWorkers: 1, TimeoutMinutes: 1}),
		cancels: make(map[int]cancelEntry),
		lanes:   make(map[string]*lane),
		store:   st,
	}
	b.manager = NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true}))
	b.out = NewHub()

	ch := &hookCh{}
	var once sync.Once
	ch.onText = func(text string) {
		// The instant the queued message is announced as starting, take the slot.
		if strings.Contains(text, "대기 중이던 요청을 시작합니다") {
			once.Do(func() {
				b.dispatch(queuedMsg{chatID: 1, text: "CCC", origin: OriginTelegram})
			})
		}
	}
	b.out.Register(1, ch)

	tgt := TelegramTarget()
	b.dispatch(queuedMsg{chatID: 1, text: "AAA", origin: OriginTelegram, target: &tgt})
	<-fc.firstIn // AAA is inside Run, holding the only slot

	b.dispatch(queuedMsg{chatID: 1, text: "BBB", origin: OriginTelegram, target: &tgt})
	waitFor(t, func() bool { return b.queued() == 1 })

	close(fc.release) // AAA finishes → announces BBB → CCC takes the slot → BBB must still run

	waitFor(t, func() bool { return len(fc.seen()) == 3 })
	waitFor(t, func() bool { return b.active() == 0 && b.queued() == 0 })

	if got := strings.Join(fc.seen(), ","); got != "AAA,BBB,CCC" {
		t.Errorf("turn order = %s, want AAA,BBB,CCC — the announced message lost its "+
			"slot and was re-queued behind the send that took it", got)
	}
}

// active/queued read the dispatch bookkeeping under the bot's lock. active is
// the number of conversations currently executing a turn; queued is the number
// of turns waiting across all lanes.
func (b *Bot) active() int {
	running, _ := b.dispatchLoad()
	return running
}

func (b *Bot) queued() int {
	_, q := b.dispatchLoad()
	return q
}

// waitFor polls cond for up to ~5s. Used instead of sleeps so the test is fast
// when things go right and still fails loudly when they don't.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within timeout")
}
