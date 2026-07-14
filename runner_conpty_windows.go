//go:build windows

package main

// Phase 1 of "B안": a ClaudeClient backed by a persistent interactive `claude`
// TUI kept alive under a Windows ConPTY, instead of spawning a fresh headless
// `claude -p` process per turn (see runner.go / claudeRunner). This lets a
// mid-turn message be steered into the same running session — which the
// Claude Code CLI already supports natively via its own message queue — so
// teleclaude does not need to reimplement queuing/steering itself.
//
// Feasibility was proven separately in a throwaway probe (see
// C:\Users\lab\conpty-test and the "teleclaude interactive CLI feasibility"
// memory): a live ConPTY session preserves conversation context across
// writes, and the CLI shows "Press up to edit queued messages · esc to
// interrupt" when a second message is sent while a turn is still running.
//
// Scope of this file: session lifecycle (spawn/reuse one ConPTY per
// RunRequest.SessionID, an idle-based turn-completion heuristic, ANSI
// stripping) plus concurrent mid-turn steering (multiple Run() calls in
// flight against the same session, each resolved independently in
// submission order — see ptySession.watch) so ClaudeClient.Run can be
// exercised end-to-end. NOT yet wired into manager.go/bot.go's dispatch
// loop (steering only helps if a lane can start a second concurrent Run()
// instead of queueing behind the first — see taskId 4) or into main.go's
// backend selection, and process lifecycle (restart/!update/idle reaping)
// is still open — see taskId 3 (renumbered Phase 3) follow-up work.
//
// Known gaps, deliberately left for later phases rather than guessed at
// here: (a) multi-line prompt submission fidelity inside the TUI input box
// is unverified beyond the single-line probe; (b) the onboarding/permission
// modal catalog below only covers the phrases seen in the probe run, not a
// complete set.

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/UserExistsError/conpty"
)

// interactiveClaudeRunner is a ClaudeClient that keeps one interactive
// claude.exe process alive per session, reused across Run calls, instead of
// spawning one per turn. Route is delegated to a plain headless runner: the
// Manager routing call is a single cheap classification request with no need
// for a persistent interactive session.
type interactiveClaudeRunner struct {
	claudePath string
	cfgh       *ConfigHolder
	router     ClaudeClient

	mu       sync.Mutex
	sessions map[string]*ptySession // keyed by RunRequest.SessionID

	stopReap  chan struct{}
	stopReapO sync.Once
}

// NewInteractiveClaudeRunner builds a ClaudeClient backed by persistent
// ConPTY-attached claude.exe sessions (Phase 1 of "B안"; see file doc comment).
// Returns the interface type (rather than *interactiveClaudeRunner) so
// main.go's call site compiles unchanged against runner_conpty_stub.go's
// always-nil non-Windows stand-in.
func NewInteractiveClaudeRunner(claudePath string, cfgh *ConfigHolder) ClaudeClient {
	r := &interactiveClaudeRunner{
		claudePath: claudePath,
		cfgh:       cfgh,
		router:     NewClaudeRunner(claudePath, cfgh),
		sessions:   make(map[string]*ptySession),
		stopReap:   make(chan struct{}),
	}
	go r.reapLoop()
	return r
}

// reapLoop periodically closes sessions that have had no PTY output (and no
// turn in flight) for longer than sessionIdleTimeout, so a web conversation
// left with "!interactive on" and then abandoned does not keep a claude.exe
// TUI process resident forever. Stops when Close() is called.
func (r *interactiveClaudeRunner) reapLoop() {
	ticker := time.NewTicker(sessionIdleReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stopReap:
			return
		case <-ticker.C:
			r.reapIdleSessions()
		}
	}
}

func (r *interactiveClaudeRunner) reapIdleSessions() {
	r.mu.Lock()
	var stale []*ptySession
	for id, sess := range r.sessions {
		// reapIfIdle makes the "no turn in flight and idle long enough"
		// check plus the closing-flag mark a single atomic step under the
		// session's own pendMu, so it can never race a submit() the way two
		// separate peekHead()/idleFor() reads followed by a plain stop()
		// could — see reapIfIdle's doc comment.
		if sess.reapIfIdle(sessionIdleTimeout) {
			stale = append(stale, sess)
			delete(r.sessions, id)
		}
	}
	r.mu.Unlock()

	for _, s := range stale {
		log.Printf("[interactive] reaping idle session (no activity for %v)", sessionIdleTimeout)
		s.stop()
	}
}

func (r *interactiveClaudeRunner) cfg() *Config { return r.cfgh.Get() }

func (r *interactiveClaudeRunner) Route(ctx context.Context, req RouteRequest) (RouteDecision, error) {
	return r.router.Route(ctx, req)
}

// Run sends req.Prompt into the persistent session for req.SessionID
// (spawning it on first use) and returns the cleaned output produced up to
// that message's own idle boundary.
//
// Run does not serialize on the session: a second Run() call for the same
// SessionID while a first is still in flight (steering a message into a
// live turn) registers as its own pendingTurn and blocks only on that
// turn's own completion, delivered by sess.watch() in submission order. See
// pendingTurn / ptySession.watch for the boundary-detection model.
func (r *interactiveClaudeRunner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	if req.SessionID == "" {
		return RunResult{}, fmt.Errorf("interactiveClaudeRunner.Run: empty SessionID")
	}

	sess, err := r.sessionFor(ctx, req)
	if err != nil {
		return RunResult{}, err
	}

	turn, err := sess.submit(req.Prompt)
	if err != nil {
		return RunResult{}, fmt.Errorf("conpty write failed: %w", err)
	}
	if req.OnProgress != nil {
		req.OnProgress("💬 sent to interactive session")
	}

	select {
	case out := <-turn.done:
		if out.err != nil {
			return RunResult{}, out.err
		}
		return RunResult{Text: out.text, SessionID: req.SessionID}, nil
	case <-ctx.Done():
		// The turn stays in the pending queue and is still resolved (and
		// discarded) by watch() in order, so turns behind it in the same
		// session keep correct offsets. Only this caller gives up early.
		return RunResult{}, ctx.Err()
	}
}

// Close stops the idle reaper and terminates every live session. Best-effort;
// errors from individual sessions are ignored since we're tearing everything
// down regardless. Safe to call multiple times. Called from main.go's normal
// shutdown path and from bot.go's !update handoff (see the file doc comment
// on process lifecycle) so a restart never leaves an orphaned claude.exe TUI
// process behind.
func (r *interactiveClaudeRunner) Close() {
	r.stopReapO.Do(func() { close(r.stopReap) })

	r.mu.Lock()
	sessions := make([]*ptySession, 0, len(r.sessions))
	for k, s := range r.sessions {
		sessions = append(sessions, s)
		delete(r.sessions, k)
	}
	r.mu.Unlock()

	for _, s := range sessions {
		s.stop()
	}
}

func (r *interactiveClaudeRunner) sessionFor(ctx context.Context, req RunRequest) (*ptySession, error) {
	r.mu.Lock()
	if sess, ok := r.sessions[req.SessionID]; ok {
		r.mu.Unlock()
		return sess, nil
	}
	r.mu.Unlock()

	sess, err := r.spawnSession(ctx, req)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	// Close() closes r.stopReap BEFORE it takes r.mu to snapshot+clear
	// r.sessions (see Close). So checking stopReap here, inside the same
	// critical section as the map insert below, is race-free: if it's still
	// open, Close's snapshot (which also needs r.mu) cannot have run yet, so
	// it is guaranteed to observe whatever we insert next. spawnSession does
	// its up-to-bootIdleTimeout boot wait without holding r.mu, so without
	// this check a spawn that straddles a Close() call could insert into a
	// runner that already tore everything down — orphaned, since nothing
	// stops it afterward and the process is about to os.Exit.
	select {
	case <-r.stopReap:
		r.mu.Unlock()
		sess.stop()
		return nil, fmt.Errorf("interactive session: runner closed during spawn")
	default:
	}
	// Re-check: two concurrent first-turns for the same never-before-seen
	// SessionID would otherwise both spawn. Keep whichever won the race and
	// stop the loser's session — stop() (not just cpty.Close()) so its
	// watch() goroutine and ticker actually exit instead of leaking for the
	// rest of the process lifetime.
	if existing, ok := r.sessions[req.SessionID]; ok {
		r.mu.Unlock()
		sess.stop()
		return existing, nil
	}
	r.sessions[req.SessionID] = sess
	r.mu.Unlock()
	return sess, nil
}

// turnIdleQuiet/turnIdleTimeout are vars, not consts, so tests can shrink
// them to keep the watch()-loop tests fast instead of waiting out the real
// production durations.
var (
	turnIdleQuiet   = 1500 * time.Millisecond
	turnIdleTimeout = 10 * time.Minute
)

const (
	bootIdleQuiet   = 1200 * time.Millisecond
	bootIdleTimeout = 15 * time.Second
)

// sessionIdleReapInterval/sessionIdleTimeout control reapLoop: sessions are
// checked every sessionIdleReapInterval and closed once idle (no PTY output,
// no turn in flight) for at least sessionIdleTimeout. Vars, not consts, so
// tests can shrink them instead of waiting out the real durations.
var (
	sessionIdleReapInterval = 5 * time.Minute
	sessionIdleTimeout      = 30 * time.Minute
)

func (r *interactiveClaudeRunner) spawnSession(ctx context.Context, req RunRequest) (*ptySession, error) {
	args := interactiveSessionArgs(r.cfg(), req)
	cmdLine := buildWindowsCommandLine(r.claudePath, args)

	env := sessionEnv(r.cfg().ClaudeOauthToken, req.OwnerLabel)
	cpty, err := conpty.Start(
		cmdLine,
		conpty.ConPtyDimensions(120, 40),
		conpty.ConPtyWorkDir(req.WorkDir),
		conpty.ConPtyEnv(env),
	)
	if err != nil {
		return nil, fmt.Errorf("conpty.Start failed: %w", err)
	}

	sess := &ptySession{
		cpty:   cpty,
		tail:   newIdleTracker(),
		buf:    &safeBuffer{},
		stopCh: make(chan struct{}),
	}
	go sess.readLoop()
	go sess.watch()

	// First boot: wait for the process to settle, then blind-Enter through a
	// known onboarding dialog (trust-folder / theme picker) if one appears.
	// This mirrors the probe's finding that these are one-time-per-workdir.
	// No turn is pending yet, so this uses the raw idle wait directly rather
	// than going through the pending-turn queue watch() drives.
	//
	// Known gap (not fixed here): onboardingMarkers is not exhaustive and is
	// only checked once, right after this wait. A dialog it doesn't
	// recognize, or one that appears after this point, leaves every
	// subsequent submit() typing into a modal instead of the input box —
	// each turn then fails only after the full turnIdleTimeout, with no
	// automatic recovery. Logging the wait outcome at least makes a stuck
	// boot visible instead of silent; a real fix needs a broader dialog
	// catalog or a continuous (not boot-only) modal check.
	if err := sess.blockUntilIdle(ctx, bootIdleQuiet, bootIdleTimeout); err != nil {
		log.Printf("[interactive] session boot: idle wait: %v", err)
	}
	if looksLikeOnboardingPrompt(sess.buf.Since(0)) {
		sess.cpty.Write([]byte("\r"))
		if err := sess.blockUntilIdle(ctx, bootIdleQuiet, bootIdleTimeout); err != nil {
			log.Printf("[interactive] session boot: onboarding-dismiss idle wait: %v", err)
		}
	}

	return sess, nil
}

// interactiveSessionArgs builds the claude CLI flags for spawning a
// persistent interactive session. Unlike workerBaseArgs (runner.go), no -p /
// --output-format / prompt-via-stdin: this is the real TUI, and the prompt is
// typed into it after spawn via ptySession.submit.
func interactiveSessionArgs(cfg *Config, req RunRequest) []string {
	args := []string{"--dangerously-skip-permissions"}
	args = append(args, isolationArgs...)
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	return args
}

// sessionEnv builds the child process environment, replacing (not
// inheriting) AGLINK_OWNER_LABEL: teleclaude worker processes may themselves
// be spawned with that variable set for the screen-control lease, and
// blindly inheriting it via os.Environ() would leak the wrong owner label
// into a freshly spawned interactive session.
//
// oauthToken mirrors workerCmdEnv (runner.go), which the normal headless
// claudeRunner uses: CLAUDE_CODE_OAUTH_TOKEN is how a deployment with no
// interactive login state (a service account, a fresh machine with a stale
// or absent ~/.claude/.credentials.json) authenticates. Without it here, an
// interactive session on such a deployment would boot straight into claude's
// own login prompt — which looksLikeOnboardingPrompt does not recognize —
// and hang until turnIdleTimeout on every turn with no clear error.
func sessionEnv(oauthToken, ownerLabel string) []string {
	inherited := os.Environ()
	env := make([]string, 0, len(inherited)+2)
	for _, e := range inherited {
		if strings.HasPrefix(e, "AGLINK_OWNER_LABEL=") {
			continue
		}
		env = append(env, e)
	}
	if oauthToken != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)
	}
	if ownerLabel != "" {
		env = append(env, "AGLINK_OWNER_LABEL="+ownerLabel)
	}
	return env
}

// ptySession is one persistent claude.exe TUI process attached to a ConPTY,
// serving every turn for one SessionID until the owning
// interactiveClaudeRunner is closed (process lifecycle beyond that — restart
// recovery, idle reaping — is Phase 3, not handled here).
//
// Multiple Run() calls may be in flight concurrently against the same
// session (a steered message sent while an earlier one is still being
// answered): each becomes a pendingTurn appended to the FIFO queue, and
// watch() is the only goroutine that ever pops it or mutates a turn's own
// fields — pendMu guards just the slice (append vs. pop/peek), not the
// turns' contents.
type ptySession struct {
	cpty *conpty.ConPty
	tail *idleTracker
	buf  *safeBuffer // cumulative ANSI-stripped output since session start

	submitMu sync.Mutex // serializes the two PTY writes per submit, so two concurrent submits can't interleave their keystrokes
	pendMu   sync.Mutex // guards s.pending and closing (append/read in submit and reapIfIdle, peek/pop in watch)
	pending  []*pendingTurn
	closing  bool // set by reapIfIdle under pendMu; submit() checks it under the same lock so the idle-reap decision and a racing submit can never both "win"

	stopCh   chan struct{}
	stopOnce sync.Once
}

// pendingTurn is one submitted-but-not-yet-resolved message. startOffset is
// assigned lazily by watch() the instant the turn becomes the head of the
// queue (not at submission time) — see watch's doc comment for why that
// timing, not submission time, is what makes concurrent submits resolve to
// non-overlapping output.
type pendingTurn struct {
	startOffset int // -1 until armed by watch()
	armedAt     time.Time
	done        chan turnOutcome // buffered(1): watch() never blocks on a caller that gave up
}

type turnOutcome struct {
	text string
	err  error
}

func (s *ptySession) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := s.cpty.Read(buf)
		if n > 0 {
			s.buf.Write(stripANSI(buf[:n]))
			s.tail.touch()
		}
		if err != nil {
			return
		}
	}
}

// submit appends a new pendingTurn to the queue and writes text into the
// session's input box (submitted with Enter), returning the turn so the
// caller can await its own resolution independently of any other pending
// turn. Safe to call concurrently: submitMu only guards the brief
// append+write, not the wait.
func (s *ptySession) submit(text string) (*pendingTurn, error) {
	s.submitMu.Lock()
	defer s.submitMu.Unlock()

	turn := &pendingTurn{startOffset: -1, done: make(chan turnOutcome, 1)}
	s.pendMu.Lock()
	if s.closing {
		// Lost the race against reapIdleSessions (see reapIfIdle): the
		// session is about to be stopped. Fail fast with a clear error
		// instead of writing into a PTY that's being torn down — the caller
		// (interactiveClaudeRunner.Run, via sessionFor on the next call)
		// spawns a fresh session for this SessionID.
		s.pendMu.Unlock()
		return nil, fmt.Errorf("interactive session: closing, retry")
	}
	becomesHead := len(s.pending) == 0
	s.pending = append(s.pending, turn)
	if becomesHead {
		// Arm synchronously, still holding pendMu, instead of leaving it for
		// watch()'s next 50ms poll tick. tick() only arms lazily when it
		// notices head.startOffset<0 — for the common case (a turn submitted
		// while the queue is already empty), that lazy arm can land up to
		// turnPollInterval after Enter was sent, so any output produced by
		// the CLI within that window (echo, spinner, first tokens) would
		// arrive before startOffset is set and be silently excluded from the
		// reply — see the "leading output" finding from the interactive-CLI
		// review. becomesHead means no earlier turn currently owns the
		// buffer tail, so arming right now (before any byte of this turn's
		// own output can possibly exist) is exactly the offset tick() would
		// have chosen anyway, just without the latency gap. tick() itself is
		// harmless if it also observes this turn as head afterward: it will
		// see startOffset>=0 already and skip its own arm branch (see tick).
		s.arm(turn)
	}
	s.pendMu.Unlock()

	if _, err := s.cpty.Write([]byte(text)); err != nil {
		return nil, err
	}
	// Brief pause mirrors the probe script and gives the TUI's paste
	// detection time to settle before Enter is sent.
	time.Sleep(80 * time.Millisecond)
	if _, err := s.cpty.Write([]byte("\r")); err != nil {
		return nil, err
	}
	return turn, nil
}

// reapIfIdle atomically (under pendMu, the same lock submit() checks) decides
// whether this session has no turn in flight and has been idle for at least
// timeout, and if so marks it closing so any submit() racing in after this
// point fails fast instead of writing into a PTY about to be stopped. This
// must be a single lock-protected decision+mark, not two separate reads
// (peekHead() then idleFor()) with a plain stop() afterward — that older
// shape left a window where a submit() landing between the check and stop()
// would have its turn silently killed with the message already sent into a
// dying PTY.
func (s *ptySession) reapIfIdle(timeout time.Duration) bool {
	s.pendMu.Lock()
	defer s.pendMu.Unlock()
	if len(s.pending) != 0 || s.tail.idleFor() < timeout {
		return false
	}
	s.closing = true
	return true
}

// watch is the single goroutine (one per session, started at spawn) that
// resolves pendingTurns in FIFO submission order and is the only place that
// ever reads or mutates s.pending, so no lock is needed around the queue
// itself.
//
// The Claude Code CLI processes queued messages strictly one at a time with
// no interleaving, but it is unverified whether it leaves an observable idle
// gap between finishing one queued message and starting the next (see the
// "steering reply delivery" design note in project memory) — the CLI could
// plausibly dequeue the next message the instant the previous one settles,
// with zero idle in between. armed-at-handoff-time, not
// armed-at-submission-time, is what makes this safe either way: a turn's
// startOffset is only set the moment it becomes head (i.e. right as the
// turn ahead of it resolves), and it is only resolved once *new* output has
// arrived past that offset and then gone idle again. So a zero-gap dequeue
// still requires the next turn's own output to actually start flowing
// before it can be mistaken for "done" with an empty/stolen slice — the
// risky case (offsets assigned at submission time, while an earlier turn is
// still mid-response) is what this deliberately avoids.
func (s *ptySession) watch() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			s.drainPending(fmt.Errorf("interactive session: closed"))
			return
		case <-ticker.C:
		}
		s.tick()
	}
}

// tick is one watch iteration: arm the head turn if it isn't yet, or check
// whether it can now be resolved (new output has arrived since it was armed
// and the session has since gone idle) or has timed out waiting.
//
// Resolving immediately arms the next pending turn (if any) in the same
// call, using the buffer offset at that same instant, instead of waiting
// for the next 50ms poll tick to notice the head changed. That keeps the
// "could output land in the gap and be lost" window a function call wide
// rather than one full poll interval — the case that actually matters if
// the CLI ever dequeues the next queued message with no observable idle of
// its own between turns (see watch's doc comment above).
func (s *ptySession) tick() {
	head := s.peekHead()
	if head == nil {
		return
	}
	if head.startOffset < 0 {
		s.arm(head)
		return
	}

	hasNewOutput := s.buf.Len() > head.startOffset
	idle := s.tail.idleFor() >= turnIdleQuiet
	switch {
	case hasNewOutput && idle:
		text := cleanTurnOutput(s.buf.Since(head.startOffset))
		s.popHead(turnOutcome{text: text})
		if next := s.peekHead(); next != nil {
			s.arm(next)
		}
	case time.Since(head.armedAt) > turnIdleTimeout:
		s.popHead(turnOutcome{err: fmt.Errorf("interactive session: timed out waiting for idle after %v", turnIdleTimeout)})
	}
}

func (s *ptySession) arm(turn *pendingTurn) {
	turn.startOffset = s.buf.Len()
	turn.armedAt = time.Now()
}

// peekHead returns the current queue head without removing it, or nil if
// the queue is empty.
func (s *ptySession) peekHead() *pendingTurn {
	s.pendMu.Lock()
	defer s.pendMu.Unlock()
	if len(s.pending) == 0 {
		return nil
	}
	return s.pending[0]
}

// popHead removes the current head and delivers its outcome. Only watch
// calls this, so the head it pops is always the same turn peekHead just
// returned.
func (s *ptySession) popHead(out turnOutcome) {
	s.pendMu.Lock()
	if len(s.pending) == 0 {
		s.pendMu.Unlock()
		return
	}
	head := s.pending[0]
	s.pending = s.pending[1:]
	s.pendMu.Unlock()
	head.done <- out
}

// drainPending resolves every still-queued turn with err, so no Run() call
// blocked on turn.done hangs forever past session close (ctx.Done() would
// eventually save it regardless, but this is immediate).
func (s *ptySession) drainPending(err error) {
	s.pendMu.Lock()
	pending := s.pending
	s.pending = nil
	s.pendMu.Unlock()
	for _, t := range pending {
		t.done <- turnOutcome{err: err}
	}
}

// stop terminates the session: closes the ConPTY (ending readLoop) and
// signals watch() to drain any still-pending turns and exit.
func (s *ptySession) stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.cpty.Close()
}

// blockUntilIdle blocks until no output has arrived for `quiet`, ctx is
// done, or `timeout` elapses — whichever first. Used only during session
// boot (see spawnSession), before any pendingTurn exists to hand this
// detection off to watch().
func (s *ptySession) blockUntilIdle(ctx context.Context, quiet, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.tail.idleFor() >= quiet {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			if now.After(deadline) {
				return fmt.Errorf("interactive session: timed out waiting for idle after %v", timeout)
			}
		}
	}
}

// idleTracker records the last time output arrived from the PTY.
type idleTracker struct {
	mu       sync.Mutex
	lastByte time.Time
}

func newIdleTracker() *idleTracker {
	return &idleTracker{lastByte: time.Now()}
}

func (t *idleTracker) touch() {
	t.mu.Lock()
	t.lastByte = time.Now()
	t.mu.Unlock()
}

func (t *idleTracker) idleFor() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return time.Since(t.lastByte)
}

// safeBuffer is an append-only, concurrency-safe byte accumulator for one
// session's cumulative cleaned output.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Write(p)
}

func (b *safeBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

// Since returns everything written after byte offset `from`.
func (b *safeBuffer) Since(from int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	full := b.buf.Bytes()
	if from > len(full) {
		from = len(full)
	}
	return string(full[from:])
}

var ansiRe = regexp.MustCompile(`\x1b\][^\x07]*\x07|\x1b\[[0-9;?]*[a-zA-Z]|\x1b[()][A-Z0-9]|\x1b[=>]`)

// stripANSI removes terminal escape sequences, matching the regex validated
// against a real claude TUI session in the feasibility probe. It is enough
// to make status phrases greppable in the cleaned stream; it is NOT a VT100
// screen-buffer emulation, so overlapping redraws can still leave visual
// noise around the phrases it detects.
func stripANSI(b []byte) []byte {
	return ansiRe.ReplaceAll(b, nil)
}

// onboardingMarkers are one-time-per-workdir first-run prompts observed in
// the probe (trust-folder confirmation, theme picker). Not exhaustive — see
// file doc comment.
var onboardingMarkers = []string{
	"Do you trust the files in this folder",
	"Choose the text style",
	"Enable Chrome",
}

func looksLikeOnboardingPrompt(clean string) bool {
	for _, m := range onboardingMarkers {
		if strings.Contains(clean, m) {
			return true
		}
	}
	return false
}

// cleanTurnOutput trims the leading/trailing whitespace noise that framing
// (prompt echo, box-drawing redraws) tends to leave around the actual reply
// text after ANSI stripping.
func cleanTurnOutput(s string) string {
	return strings.TrimSpace(s)
}

// buildWindowsCommandLine assembles a single Windows command-line string
// (CreateProcess takes one string, not argv) using the standard
// backslash/quote escaping rules (same algorithm as CommandLineToArgvW
// expects, mirroring Go's unexported syscall.escapeArg on windows).
func buildWindowsCommandLine(exe string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, windowsQuoteArg(exe))
	for _, a := range args {
		parts = append(parts, windowsQuoteArg(a))
	}
	return strings.Join(parts, " ")
}

func windowsQuoteArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, r := range s {
		switch r {
		case '\\':
			slashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes*2+1))
			slashes = 0
			b.WriteByte('"')
		default:
			b.WriteString(strings.Repeat(`\`, slashes))
			slashes = 0
			b.WriteRune(r)
		}
	}
	b.WriteString(strings.Repeat(`\`, slashes*2))
	b.WriteByte('"')
	return b.String()
}
