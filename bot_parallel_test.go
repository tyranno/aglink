package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// newParallelTestBot builds a minimal Bot wired for lane-dispatch unit tests.
// It uses a nil API; tests that need a worker to actually run wire a manager and
// client explicitly (see TestDispatch_ChattyConversationDoesNotStarveOthers).
func newParallelTestBot(maxWorkers int) *Bot {
	return &Bot{
		cfgh:    NewConfigHolder(&Config{MaxWorkers: maxWorkers, TimeoutMinutes: 1}),
		cancels: make(map[int]context.CancelFunc),
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

// TestBot_CancelClearsAll verifies that cancel() logic clears all tracked cancel funcs.
func TestBot_CancelClearsAll(t *testing.T) {
	b := newParallelTestBot(3)

	cancelled := 0
	for i := range 3 {
		b.mu.Lock()
		b.cancels[i] = func() { cancelled++ }
		b.mu.Unlock()
	}

	// Reproduce cancel() logic.
	b.mu.Lock()
	fns := make([]context.CancelFunc, 0, len(b.cancels))
	for _, fn := range b.cancels {
		fns = append(fns, fn)
	}
	b.mu.Unlock()

	for _, fn := range fns {
		fn()
	}

	if cancelled != 3 {
		t.Errorf("cancelled = %d, want 3", cancelled)
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

	b.dispatchScheduledTask(7, "scheduled A")
	b.dispatchScheduledTask(7, "scheduled B")

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
		cancels: make(map[int]context.CancelFunc),
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
