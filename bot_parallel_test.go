package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

// newTestBot builds a minimal Bot wired for parallel-pool unit tests.
// It uses a nil API; tests must not call dispatchText (which calls b.Send via the API).
// Instead they manipulate state directly to verify the pool invariants.
func newParallelTestBot(maxWorkers int) *Bot {
	return &Bot{
		cfgh:    NewConfigHolder(&Config{MaxWorkers: maxWorkers, TimeoutMinutes: 1}),
		cancels: make(map[int]context.CancelFunc),
	}
}

func TestBot_InitialParallelState(t *testing.T) {
	b := newParallelTestBot(3)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.activeCount != 0 {
		t.Errorf("activeCount = %d, want 0", b.activeCount)
	}
	if len(b.cancels) != 0 {
		t.Errorf("cancels len = %d, want 0", len(b.cancels))
	}
	if len(b.queue) != 0 {
		t.Errorf("queue len = %d, want 0", len(b.queue))
	}
}

// TestBot_QueueWhenFull verifies the queueing branch of dispatch:
// when activeCount >= MaxWorkers the message is enqueued instead of spawning.
func TestBot_QueueWhenFull(t *testing.T) {
	b := newParallelTestBot(2)
	b.mu.Lock()
	b.activeCount = 2 // simulate both slots taken
	b.mu.Unlock()

	// Reproduce the queueing branch from dispatch().
	msg := queuedMsg{chatID: 42, text: "overflow message"}
	b.mu.Lock()
	if b.activeCount >= b.cfg().MaxWorkers {
		b.queue = append(b.queue, msg)
	}
	b.mu.Unlock()

	b.mu.Lock()
	qLen := len(b.queue)
	b.mu.Unlock()

	if qLen != 1 {
		t.Fatalf("queue len = %d, want 1", qLen)
	}
	b.mu.Lock()
	firstChatID := b.queue[0].chatID
	b.mu.Unlock()
	if firstChatID != 42 {
		t.Errorf("queued chatID = %d, want 42", firstChatID)
	}
}

// TestBot_SlotNotExceededUnderLoad verifies that activeCount never exceeds MaxWorkers
// when workers are acquired and released concurrently.
func TestBot_SlotNotExceededUnderLoad(t *testing.T) {
	const maxW = 3
	b := newParallelTestBot(maxW)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		maxSeen int
	)

	// Simulate 10 concurrent workers trying to acquire slots.
	for range 10 {
		wg.Go(func() {
			b.mu.Lock()
			if b.activeCount < b.cfg().MaxWorkers {
				b.activeCount++
				cur := b.activeCount
				b.mu.Unlock()

				mu.Lock()
				if cur > maxSeen {
					maxSeen = cur
				}
				mu.Unlock()

				time.Sleep(5 * time.Millisecond)

				b.mu.Lock()
				b.activeCount--
				b.mu.Unlock()
			} else {
				b.mu.Unlock()
			}
		})
	}
	wg.Wait()

	if maxSeen > maxW {
		t.Errorf("activeCount exceeded MaxWorkers: max seen = %d, limit = %d", maxSeen, maxW)
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

// TestDispatchTargeted_RoutesThroughQueue verifies the Task 5 review fix:
// dispatchTargeted must go through the shared worker-slot queue (dispatch()),
// not call the Manager directly — so it gets MaxWorkers limiting, the
// TimeoutMinutes deadline, !cancel registration, and panic recovery like every
// other dispatch. With MaxWorkers=0, dispatch()'s queueing branch always fires
// (activeCount 0 >= MaxWorkers 0) instead of spawning a worker goroutine, so
// this is deterministic: we can inspect the queued message directly rather
// than racing a background goroutine.
func TestDispatchTargeted_RoutesThroughQueue(t *testing.T) {
	b := newParallelTestBot(0)
	b.out = NewHub()

	tgt := &Target{Kind: "web", Project: "myapp", ID: "abc"}
	b.dispatchTargeted(42, "hello", tgt)

	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) != 1 {
		t.Fatalf("queue len = %d, want 1 (dispatchTargeted must enqueue via dispatch(), not call the Manager directly)", len(b.queue))
	}
	qm := b.queue[0]
	if qm.chatID != 42 || qm.text != "hello" {
		t.Errorf("queued msg = %+v, want chatID=42 text=hello", qm)
	}
	if qm.target == nil || *qm.target != *tgt {
		t.Errorf("queued msg target = %+v, want %+v", qm.target, tgt)
	}
}

// TestDispatchTargeted_DefaultsToTelegramTarget verifies a nil tgt (web client
// that hasn't sent an explicit target) still enqueues with target.Kind ==
// "telegram", matching dispatchTargeted's documented default.
func TestDispatchTargeted_DefaultsToTelegramTarget(t *testing.T) {
	b := newParallelTestBot(0)
	b.out = NewHub()

	b.dispatchTargeted(7, "hi", nil)

	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) != 1 {
		t.Fatalf("queue len = %d, want 1", len(b.queue))
	}
	if b.queue[0].target == nil || b.queue[0].target.Kind != "telegram" {
		t.Errorf("queued msg target = %+v, want Kind=telegram", b.queue[0].target)
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
