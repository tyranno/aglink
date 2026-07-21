package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newParallelTestBot builds a minimal Bot wired for lane-dispatch unit tests.
// It uses a nil API; tests that need a worker to actually run wire a manager and
// client explicitly (see TestDispatch_ChattyConversationDoesNotStarveOthers).
func newParallelTestBot(maxWorkers int) *Bot {
	return &Bot{
		cfgh:    NewConfigHolder(&Config{MaxWorkers: maxWorkers, TimeoutMinutes: 1}),
		cancels: make(map[int]*cancelEntry),
		lanes:   make(map[string]*lane),
	}
}

func TestBot_InitialLaneState(t *testing.T) {
	b := newParallelTestBot(3)
	if running, queued := b.dispatchLoad(); running != 0 || queued != 0 {
		t.Errorf("initial load = (running %d, queued %d), want (0, 0)", running, queued)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.cancels) != 0 {
		t.Errorf("cancels len = %d, want 0", len(b.cancels))
	}
	if len(b.lanes) != 0 {
		t.Errorf("lanes len = %d, want 0", len(b.lanes))
	}
}

// noopReply is a replySender that discards everything; only used to satisfy
// b.cancel's signature in tests that don't inspect the reply text.
type noopReply struct{}

func (noopReply) Send(int64, string) error              { return nil }
func (noopReply) SendPhoto(int64, []byte, string) error { return nil }
func (noopReply) Typing(int64)                          {}
func (noopReply) Done(int64)                            {}

// TestBot_CancelOnlyOwnLane verifies !cancel stops only the caller's own lane,
// leaving other conversations' running workers untouched — regression test for
// the bug where !cancel tore down every lane in the process.
func TestBot_CancelOnlyOwnLane(t *testing.T) {
	b := newParallelTestBot(3)

	cancelledA, cancelledB := 0, 0
	b.mu.Lock()
	b.cancels[1] = &cancelEntry{key: "telegram", cancel: func() { cancelledA++ }}
	b.cancels[2] = &cancelEntry{key: "web:other-topic", cancel: func() { cancelledB++ }}
	b.mu.Unlock()

	b.cancel(noopReply{}, 0, "telegram")

	if cancelledA != 1 {
		t.Errorf("cancelledA = %d, want 1 (own lane must be cancelled)", cancelledA)
	}
	if cancelledB != 0 {
		t.Errorf("cancelledB = %d, want 0 (other lane must survive)", cancelledB)
	}
}

// TestBot_AdjustTimeout checks the deadline math of !timeout: extend/reduce move
// the deadline, the effective total never drops below the base TimeoutMinutes,
// reset snaps back to the base, and only the caller's own lane is touched.
func TestBot_AdjustTimeout(t *testing.T) {
	b := newParallelTestBot(3) // base TimeoutMinutes = 1
	base := time.Minute
	start := time.Now()

	// A far-future timer so it never fires mid-test; adjustTimeout only Resets it.
	mk := func(key string) *cancelEntry {
		return &cancelEntry{key: key, cancel: func() {}, timer: time.AfterFunc(time.Hour, func() {}), start: start, deadline: start.Add(base)}
	}
	b.mu.Lock()
	eMine := mk("telegram")
	eOther := mk("web:other")
	b.cancels[1] = eMine
	b.cancels[2] = eOther
	b.mu.Unlock()
	defer func() { eMine.timer.Stop(); eOther.timer.Stop() }()

	effOf := func(e *cancelEntry) float64 { return e.deadline.Sub(e.start).Minutes() }

	// Extend +10 → effective ~11 minutes.
	if n, eff := b.adjustTimeout("telegram", timeoutOp{delta: 10 * time.Minute}); n != 1 || eff != 11 {
		t.Fatalf("extend +10: n=%d eff=%d, want 1/11", n, eff)
	}
	if got := effOf(eMine); got < 10.99 || got > 11.01 {
		t.Errorf("after +10 effective = %.2f min, want ~11", got)
	}

	// Reduce -5 → ~6 minutes (still above base).
	if _, eff := b.adjustTimeout("telegram", timeoutOp{delta: -5 * time.Minute}); eff != 6 {
		t.Errorf("reduce -5: eff=%d, want 6", eff)
	}

	// Reduce far below base → floored at base (1 min), never below.
	if _, eff := b.adjustTimeout("telegram", timeoutOp{delta: -60 * time.Minute}); eff != 1 {
		t.Errorf("reduce past base: eff=%d, want 1 (floored at base)", eff)
	}

	// Absolute 30 → exactly 30 minutes.
	if _, eff := b.adjustTimeout("telegram", timeoutOp{absolute: 30 * time.Minute}); eff != 30 {
		t.Errorf("absolute 30: eff=%d, want 30", eff)
	}

	// Reset → back to base.
	if _, eff := b.adjustTimeout("telegram", timeoutOp{reset: true}); eff != 1 {
		t.Errorf("reset: eff=%d, want 1 (base)", eff)
	}

	// The other lane must be untouched throughout.
	if got := effOf(eOther); got < 0.99 || got > 1.01 {
		t.Errorf("other lane effective = %.2f min, want ~1 (untouched)", got)
	}

	// A lane with no running worker adjusts nothing.
	if n, _ := b.adjustTimeout("web:nobody", timeoutOp{delta: time.Minute}); n != 0 {
		t.Errorf("adjust on empty lane: n=%d, want 0", n)
	}
}

// TestDispatchTargeted_RoutesThroughLane verifies dispatchTargeted enqueues via
// dispatch() — so it gets MaxWorkers limiting, the TimeoutMinutes deadline,
// !cancel registration, and panic recovery like every other dispatch — rather
// than calling the Manager directly. With MaxWorkers=0 no global slot is ever
// free, so the message parks in its conversation's lane and can be inspected
// without racing a background goroutine.
func TestDispatchTargeted_RoutesThroughLane(t *testing.T) {
	b := newParallelTestBot(0)
	b.out = NewHub()

	tgt := &Target{Kind: "web", Project: "myapp", ID: "abc"}
	b.dispatchTargeted(42, "hello", tgt)

	b.mu.Lock()
	defer b.mu.Unlock()
	l := b.lanes[laneKeyOf(*tgt)]
	if l == nil || len(l.queue) != 1 {
		t.Fatalf("lane queue = %v, want one parked message (dispatchTargeted must enqueue via dispatch())", l)
	}
	qm := l.queue[0]
	if qm.chatID != 42 || qm.text != "hello" {
		t.Errorf("queued msg = %+v, want chatID=42 text=hello", qm)
	}
	if qm.target == nil || *qm.target != *tgt {
		t.Errorf("queued msg target = %+v, want %+v", qm.target, tgt)
	}
}

// TestDispatchTargeted_DefaultsToTelegramLane verifies a nil tgt (web client
// that hasn't sent an explicit target) parks in the telegram lane with
// target.Kind == "telegram", matching dispatchTargeted's documented default.
func TestDispatchTargeted_DefaultsToTelegramLane(t *testing.T) {
	b := newParallelTestBot(0)
	b.out = NewHub()

	b.dispatchTargeted(7, "hi", nil)

	b.mu.Lock()
	defer b.mu.Unlock()
	l := b.lanes[laneKeyOf(TelegramTarget())]
	if l == nil || len(l.queue) != 1 {
		t.Fatalf("telegram lane queue = %v, want one parked message", l)
	}
	if l.queue[0].target == nil || l.queue[0].target.Kind != "telegram" {
		t.Errorf("queued msg target = %+v, want Kind=telegram", l.queue[0].target)
	}
}

func TestDispatchScheduledTask_UsesIndependentLanes(t *testing.T) {
	b := newParallelTestBot(0)
	b.out = NewHub()

	b.dispatchScheduledTask(7, "scheduled A", "myapp")
	b.dispatchScheduledTask(7, "scheduled B", "myapp")

	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.lanes) != 2 {
		t.Fatalf("scheduled task lanes = %d, want 2 independent lanes: %#v", len(b.lanes), b.lanes)
	}
	for key, l := range b.lanes {
		if !strings.HasPrefix(key, "task:") {
			t.Errorf("scheduled task lane key = %q, want task:*", key)
		}
		if l == nil || len(l.queue) != 1 {
			t.Fatalf("lane %q queue = %#v, want exactly one queued task", key, l)
		}
		if !l.queue[0].isTask {
			t.Errorf("lane %q queued message isTask = false, want true", key)
		}
	}
}

// TestBot_WorkerSeqMonotonic verifies workerSeq increases with each slot acquisition.
func TestBot_WorkerSeqMonotonic(t *testing.T) {
	b := newParallelTestBot(5)
	for i := 1; i <= 4; i++ {
		b.mu.Lock()
		b.workerSeq++
		got := b.workerSeq
		b.mu.Unlock()
		if got != i {
			t.Errorf("workerSeq after %d increments = %d, want %d", i, got, i)
		}
	}
}

// laneClient signals each turn as it enters Run and blocks it until released, so
// a test can observe which turns the dispatcher chose to run concurrently.
type laneClient struct {
	entered chan string   // marker of each turn as it starts running
	release chan struct{} // closed to let all parked turns finish
}

func (c *laneClient) Route(context.Context, RouteRequest) (RouteDecision, error) {
	return RouteDecision{}, nil
}

func (c *laneClient) Run(_ context.Context, req RunRequest) (RunResult, error) {
	c.entered <- laneMarkerOf(req.Prompt)
	<-c.release
	return RunResult{Text: "done"}, nil
}

// laneMarkerOf returns the marker appearing LAST in the prompt. A conversation's
// second turn inlines its first turn's text as history, so earlier markers are
// present too; only the last identifies the current turn.
func laneMarkerOf(prompt string) string {
	best, at := "", -1
	for _, m := range []string{"MK_A1", "MK_A2", "MK_B1"} {
		if i := strings.LastIndex(prompt, m); i > at {
			best, at = m, i
		}
	}
	return best
}

// A single chatty conversation must not consume every global slot and starve
// other channels. With per-conversation lanes, a conversation already running
// serializes its own backlog into one slot, leaving the other slot free for a
// different conversation. Under the old global pool, the same conversation's
// second turn grabbed the second slot and the other channel waited.
//
// Setup: MaxWorkers=2. A1 starts and holds one slot. Then A2 (same conversation)
// and B1 (different conversation) are queued. The next turn to run must be B1,
// not A2 — proving cross-conversation parallelism wins over same-conversation
// backlog.
func TestDispatch_ChattyConversationDoesNotStarveOthers(t *testing.T) {
	cl := &laneClient{entered: make(chan string, 8), release: make(chan struct{})}

	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	convA, err := st.NewWebConv("A")
	if err != nil {
		t.Fatal(err)
	}
	convB, err := st.NewWebConv("B")
	if err != nil {
		t.Fatal(err)
	}

	b := &Bot{
		cfgh:    NewConfigHolder(&Config{MaxWorkers: 2, TimeoutMinutes: 1}),
		cancels: make(map[int]*cancelEntry),
		lanes:   make(map[string]*lane),
		store:   st,
	}
	b.manager = NewManager(cl, nil, st, NewConfigHolder(&Config{ManagerAlways: true}))
	b.out = NewHub()

	tgtA := Target{Kind: "web", ID: convA.ID}
	tgtB := Target{Kind: "web", ID: convB.ID}

	// A1 starts and holds one of the two global slots.
	b.dispatchTargeted(1, "MK_A1", &tgtA)
	if got := <-cl.entered; got != "MK_A1" {
		t.Fatalf("first running turn = %q, want MK_A1", got)
	}

	// Queue a second turn for conversation A, then one for conversation B. A2
	// must wait behind A1 in lane A; B1 takes the free slot.
	b.dispatchTargeted(1, "MK_A2", &tgtA)
	b.dispatchTargeted(1, "MK_B1", &tgtB)

	if got := <-cl.entered; got != "MK_B1" {
		t.Fatalf("second concurrent turn = %q, want MK_B1 — a chatty conversation "+
			"starved another channel (global pool instead of per-conversation lanes)", got)
	}

	close(cl.release)
	// A1 finishes and lane A hands its slot to its own backlog: A2 runs next.
	if got := <-cl.entered; got != "MK_A2" {
		t.Fatalf("final turn = %q, want MK_A2 (lane A's serialized backlog)", got)
	}

	// Let every worker goroutine fully drain (including its store write) before
	// the test returns, so t.TempDir cleanup does not race an in-flight write.
	waitFor(t, func() bool {
		running, queued := b.dispatchLoad()
		return running == 0 && queued == 0
	})
}
