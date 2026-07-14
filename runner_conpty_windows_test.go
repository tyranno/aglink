//go:build windows

package main

import (
	"strings"
	"testing"
	"time"
)

func TestStripANSI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text unaffected", "READY", "READY"},
		{"csi color codes removed", "\x1b[31mhello\x1b[0m", "hello"},
		{"osc title sequence removed", "\x1b]0;claude\x07status line", "status line"},
		{"cursor move sequences removed", "\x1b[2K\x1b[1Gtext", "text"},
		{"mixed real-world chunk", "\x1b[1mPress up\x1b[0m to edit queued messages \xc2\xb7 esc to interrupt", "Press up to edit queued messages \xc2\xb7 esc to interrupt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(stripANSI([]byte(c.in)))
			if got != c.want {
				t.Errorf("stripANSI(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestLooksLikeOnboardingPrompt(t *testing.T) {
	if !looksLikeOnboardingPrompt("Do you trust the files in this folder?") {
		t.Error("expected trust-folder prompt to be detected")
	}
	if !looksLikeOnboardingPrompt("noise\nChoose the text style that looks best\nmore noise") {
		t.Error("expected theme-picker prompt to be detected")
	}
	if looksLikeOnboardingPrompt("READY") {
		t.Error("did not expect a normal reply to look like onboarding")
	}
}

func TestCleanTurnOutput(t *testing.T) {
	got := cleanTurnOutput("  \n  hello world  \n\n")
	if got != "hello world" {
		t.Errorf("cleanTurnOutput = %q, want %q", got, "hello world")
	}
}

func TestSafeBufferSince(t *testing.T) {
	b := &safeBuffer{}
	b.Write([]byte("first"))
	offset := b.Len()
	b.Write([]byte("second"))
	if got := b.Since(offset); got != "second" {
		t.Errorf("Since(%d) = %q, want %q", offset, got, "second")
	}
	if got := b.Since(0); got != "firstsecond" {
		t.Errorf("Since(0) = %q, want %q", got, "firstsecond")
	}
	// offset past current length must not panic or underflow.
	if got := b.Since(1000); got != "" {
		t.Errorf("Since(huge) = %q, want empty", got)
	}
}

func TestIdleTracker(t *testing.T) {
	tr := newIdleTracker()
	if tr.idleFor() < 0 {
		t.Error("idleFor should never be negative")
	}
	tr.touch()
	if tr.idleFor() > 100*time.Millisecond {
		t.Errorf("idleFor right after touch should be tiny, got %v", tr.idleFor())
	}
}

func TestWindowsQuoteArg(t *testing.T) {
	cases := map[string]string{
		"simple":              "simple",
		"":                    `""`,
		"with space":          `"with space"`,
		`has"quote`:           `"has\"quote"`,
		`trailing\`:           `trailing\`,
		`C:\Program Files\x`:  `"C:\Program Files\x"`,
	}
	for in, want := range cases {
		if got := windowsQuoteArg(in); got != want {
			t.Errorf("windowsQuoteArg(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- pending-turn / watch() concurrency tests ---
//
// These build a ptySession directly with a nil cpty and drive it purely
// through its buffer/idle-tracker/pending-queue, standing in for what a
// real claude.exe subprocess's readLoop would otherwise produce. That's
// enough to exercise watch()'s boundary-resolution logic without spawning a
// real interactive session.

func newTestSession(t *testing.T) *ptySession {
	t.Helper()
	s := &ptySession{
		tail:   newIdleTracker(),
		buf:    &safeBuffer{},
		stopCh: make(chan struct{}),
	}
	go s.watch()
	t.Cleanup(func() { s.stopOnce.Do(func() { close(s.stopCh) }) })
	return s
}

// testSubmit mimics ptySession.submit's pending-queue append without
// touching s.cpty (nil in these tests), returning the turn to await.
func testSubmit(s *ptySession) *pendingTurn {
	turn := &pendingTurn{startOffset: -1, done: make(chan turnOutcome, 1)}
	s.pendMu.Lock()
	s.pending = append(s.pending, turn)
	s.pendMu.Unlock()
	return turn
}

// simulateOutput mimics what readLoop does when bytes arrive from the PTY.
func simulateOutput(s *ptySession, text string) {
	s.buf.Write([]byte(text))
	s.tail.touch()
}

func withShortIdleTiming(t *testing.T, quiet, timeout time.Duration) {
	t.Helper()
	origQuiet, origTimeout := turnIdleQuiet, turnIdleTimeout
	turnIdleQuiet, turnIdleTimeout = quiet, timeout
	t.Cleanup(func() { turnIdleQuiet, turnIdleTimeout = origQuiet, origTimeout })
}

// awaitTurn blocks on turn.done up to timeout, failing the test if it never
// fires — a stuck watch() bug should fail fast, not hang the suite.
func awaitTurn(t *testing.T, turn *pendingTurn, timeout time.Duration) turnOutcome {
	t.Helper()
	select {
	case out := <-turn.done:
		return out
	case <-time.After(timeout):
		t.Fatal("timed out waiting for turn to resolve")
		return turnOutcome{}
	}
}

func TestPtySessionWatch_SingleTurnResolves(t *testing.T) {
	withShortIdleTiming(t, 60*time.Millisecond, 5*time.Second)
	s := newTestSession(t)

	turn := testSubmit(s)
	time.Sleep(150 * time.Millisecond) // let watch's ticker observe and arm it
	simulateOutput(s, "hello reply")

	out := awaitTurn(t, turn, 2*time.Second)
	if out.err != nil {
		t.Fatalf("unexpected err: %v", out.err)
	}
	if out.text != "hello reply" {
		t.Errorf("text = %q, want %q", out.text, "hello reply")
	}
}

// TestPtySessionWatch_SteeredTurnResolvesSeparately submits a second message
// while the first is still producing output (the actual steering scenario:
// a mid-turn message sent into a live PTY) and verifies each turn's caller
// gets back only its own reply text, with no overlap in either direction.
func TestPtySessionWatch_SteeredTurnResolvesSeparately(t *testing.T) {
	withShortIdleTiming(t, 60*time.Millisecond, 5*time.Second)
	s := newTestSession(t)

	turnA := testSubmit(s)
	time.Sleep(150 * time.Millisecond) // let watch arm A
	simulateOutput(s, "partial A ")

	// Steer turn B in while A is still active (idleFor is well under quiet
	// right after the touch above) — this is the concurrent-Run() case.
	turnB := testSubmit(s)
	simulateOutput(s, "rest of A")

	outA := awaitTurn(t, turnA, 2*time.Second)
	if outA.err != nil {
		t.Fatalf("turn A: unexpected err: %v", outA.err)
	}
	if outA.text != "partial A rest of A" {
		t.Errorf("turn A text = %q, want %q", outA.text, "partial A rest of A")
	}

	// The instant A resolves, watch() arms B in the same tick — write B's
	// output right away to probe that near-zero handoff window rather than
	// giving watch() a generous head start.
	simulateOutput(s, "B's answer")

	outB := awaitTurn(t, turnB, 2*time.Second)
	if outB.err != nil {
		t.Fatalf("turn B: unexpected err: %v", outB.err)
	}
	if outB.text != "B's answer" {
		t.Errorf("turn B text = %q, want %q (must not include any of turn A's output)", outB.text, "B's answer")
	}
}

// TestPtySessionWatch_ThirdTurnQueuedBehindTwo checks the FIFO ordering
// holds for three turns deep, not just two.
func TestPtySessionWatch_ThirdTurnQueuedBehindTwo(t *testing.T) {
	withShortIdleTiming(t, 60*time.Millisecond, 5*time.Second)
	s := newTestSession(t)

	turnA := testSubmit(s)
	turnB := testSubmit(s)
	turnC := testSubmit(s)
	time.Sleep(150 * time.Millisecond) // let watch arm A (B, C still queued behind it)

	simulateOutput(s, "A out")
	outA := awaitTurn(t, turnA, 2*time.Second)
	if outA.text != "A out" {
		t.Errorf("turn A text = %q, want %q", outA.text, "A out")
	}

	simulateOutput(s, "B out")
	outB := awaitTurn(t, turnB, 2*time.Second)
	if outB.text != "B out" {
		t.Errorf("turn B text = %q, want %q", outB.text, "B out")
	}

	simulateOutput(s, "C out")
	outC := awaitTurn(t, turnC, 2*time.Second)
	if outC.text != "C out" {
		t.Errorf("turn C text = %q, want %q", outC.text, "C out")
	}
}

func TestPtySessionWatch_TimeoutResolvesWithErr(t *testing.T) {
	withShortIdleTiming(t, 40*time.Millisecond, 150*time.Millisecond)
	s := newTestSession(t)

	turn := testSubmit(s)
	// Never produce any output — the turn should time out rather than hang
	// or be falsely resolved as "idle and done" with an empty slice.
	out := awaitTurn(t, turn, 2*time.Second)
	if out.err == nil {
		t.Fatal("expected a timeout error, got a resolved (nil-error) outcome")
	}
}

func TestPtySessionDrainPending_OnStop(t *testing.T) {
	s := newTestSession(t)
	turnA := testSubmit(s)
	turnB := testSubmit(s)

	s.stopOnce.Do(func() { close(s.stopCh) })

	outA := awaitTurn(t, turnA, 2*time.Second)
	if outA.err == nil {
		t.Error("turn A: expected an error after session stop, got nil")
	}
	outB := awaitTurn(t, turnB, 2*time.Second)
	if outB.err == nil {
		t.Error("turn B: expected an error after session stop, got nil")
	}
}

func TestBuildWindowsCommandLine(t *testing.T) {
	got := buildWindowsCommandLine(`C:\Program Files\claude.exe`, []string{"--model", "sonnet", "--dangerously-skip-permissions"})
	want := `"C:\Program Files\claude.exe" --model sonnet --dangerously-skip-permissions`
	if got != want {
		t.Errorf("buildWindowsCommandLine = %q, want %q", got, want)
	}
}

func TestInteractiveSessionArgsIncludesIsolationAndModel(t *testing.T) {
	cfg := &Config{}
	req := RunRequest{Model: "opus"}
	args := interactiveSessionArgs(cfg, req)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--dangerously-skip-permissions") {
		t.Errorf("expected --dangerously-skip-permissions in args, got %v", args)
	}
	if !strings.Contains(joined, "--model opus") {
		t.Errorf("expected --model opus in args, got %v", args)
	}
	if !strings.Contains(joined, "--strict-mcp-config") {
		t.Errorf("expected isolationArgs to be included, got %v", args)
	}
}

func TestSessionEnvAddsOwnerLabelOnlyWhenSet(t *testing.T) {
	withLabel := sessionEnv("web:42")
	found := false
	for _, e := range withLabel {
		if e == "AGLINK_OWNER_LABEL=web:42" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected AGLINK_OWNER_LABEL=web:42 in env, got %v", withLabel)
	}

	withoutLabel := sessionEnv("")
	for _, e := range withoutLabel {
		if strings.HasPrefix(e, "AGLINK_OWNER_LABEL=") {
			t.Errorf("did not expect AGLINK_OWNER_LABEL in env when ownerLabel is empty, got %v", withoutLabel)
		}
	}
}
