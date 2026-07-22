package main

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

// A worker turn can start a process that outlives the CLI (an app it just built, a
// server it launched). That process inherits the CLI's stdout/stderr handles, so the
// pipes never reach EOF and cmd.Wait() used to block forever — the worker never
// returned, the conversation never completed, and !cancel could not free it either,
// because the block was on the pipe, not on the process. cmd.WaitDelay bounds that
// wait; these tests pin down how we interpret what it reports back.

func TestIgnoreWaitDelayTreatsHeldPipesAsSuccess(t *testing.T) {
	// Wait returns ErrWaitDelay only when the process itself exited cleanly and
	// merely its inherited pipes were still open. The turn's output is already
	// captured by then, so it must not be reported as a failed turn.
	if err := ignoreWaitDelay(exec.ErrWaitDelay, "codex"); err != nil {
		t.Fatalf("ErrWaitDelay should be treated as success, got %v", err)
	}
}

func TestIgnoreWaitDelayTreatsWrappedHeldPipesAsSuccess(t *testing.T) {
	wrapped := fmt.Errorf("codex: %w", exec.ErrWaitDelay)
	if err := ignoreWaitDelay(wrapped, "codex"); err != nil {
		t.Fatalf("wrapped ErrWaitDelay should be treated as success, got %v", err)
	}
}

func TestIgnoreWaitDelayPassesOtherErrorsThrough(t *testing.T) {
	// A real failure (non-zero exit, start failure) must still surface, or a broken
	// turn would silently look like a successful one.
	boom := errors.New("exit status 1")
	if err := ignoreWaitDelay(boom, "codex"); !errors.Is(err, boom) {
		t.Fatalf("non-WaitDelay error must pass through, got %v", err)
	}
	if err := ignoreWaitDelay(nil, "codex"); err != nil {
		t.Fatalf("nil must stay nil, got %v", err)
	}
}

func TestWorkerWaitDelayIsBounded(t *testing.T) {
	// Zero would mean "wait forever on the pipes", which is exactly the hang this
	// guards against; an overlong delay would keep the whole bot wedged that long,
	// since a blocked worker holds up everything behind it.
	if workerWaitDelay <= 0 {
		t.Fatalf("workerWaitDelay must be positive, got %v", workerWaitDelay)
	}
	if workerWaitDelay > 60*1e9 { // 60s in nanoseconds
		t.Fatalf("workerWaitDelay unreasonably long: %v", workerWaitDelay)
	}
}
