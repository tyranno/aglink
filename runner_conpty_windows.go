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
// stripping) so ClaudeClient.Run can be exercised end-to-end. NOT yet wired
// into manager.go/bot.go — see taskId 2/3 for mid-turn steering and process
// lifecycle (restart/!update/idle reaping) follow-up work.
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
}

// NewInteractiveClaudeRunner builds a ClaudeClient backed by persistent
// ConPTY-attached claude.exe sessions (Phase 1 of "B안"; see file doc comment).
func NewInteractiveClaudeRunner(claudePath string, cfgh *ConfigHolder) *interactiveClaudeRunner {
	return &interactiveClaudeRunner{
		claudePath: claudePath,
		cfgh:       cfgh,
		router:     NewClaudeRunner(claudePath, cfgh),
		sessions:   make(map[string]*ptySession),
	}
}

func (r *interactiveClaudeRunner) cfg() *Config { return r.cfgh.Get() }

func (r *interactiveClaudeRunner) Route(ctx context.Context, req RouteRequest) (RouteDecision, error) {
	return r.router.Route(ctx, req)
}

// Run sends req.Prompt into the persistent session for req.SessionID
// (spawning it on first use) and returns the cleaned output produced up to
// the next idle boundary.
func (r *interactiveClaudeRunner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	if req.SessionID == "" {
		return RunResult{}, fmt.Errorf("interactiveClaudeRunner.Run: empty SessionID")
	}

	sess, err := r.sessionFor(ctx, req)
	if err != nil {
		return RunResult{}, err
	}

	// Serialize turns within a single session — the TUI is a single input
	// stream and only one Run() at a time may drive it. Cross-session Run
	// calls (different lanes) proceed fully in parallel since each has its
	// own ConPty/goroutine/buffer.
	sess.mu.Lock()
	defer sess.mu.Unlock()

	startOffset := sess.buf.Len()
	if err := sess.send(req.Prompt); err != nil {
		return RunResult{}, fmt.Errorf("conpty write failed: %w", err)
	}
	if req.OnProgress != nil {
		req.OnProgress("💬 sent to interactive session")
	}

	if err := sess.waitForIdle(ctx, turnIdleQuiet, turnIdleTimeout); err != nil {
		return RunResult{}, err
	}

	text := cleanTurnOutput(sess.buf.Since(startOffset))
	return RunResult{Text: text, SessionID: req.SessionID}, nil
}

// Close terminates every live session. Best-effort; errors from individual
// sessions are ignored since we're tearing everything down regardless.
func (r *interactiveClaudeRunner) Close() {
	r.mu.Lock()
	sessions := make([]*ptySession, 0, len(r.sessions))
	for k, s := range r.sessions {
		sessions = append(sessions, s)
		delete(r.sessions, k)
	}
	r.mu.Unlock()

	for _, s := range sessions {
		s.cpty.Close()
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
	// Re-check: two concurrent first-turns for the same never-before-seen
	// SessionID would otherwise both spawn. Keep whichever won the race and
	// close the loser's process.
	if existing, ok := r.sessions[req.SessionID]; ok {
		r.mu.Unlock()
		sess.cpty.Close()
		return existing, nil
	}
	r.sessions[req.SessionID] = sess
	r.mu.Unlock()
	return sess, nil
}

const (
	turnIdleQuiet   = 1500 * time.Millisecond
	turnIdleTimeout = 10 * time.Minute
	bootIdleQuiet   = 1200 * time.Millisecond
	bootIdleTimeout = 15 * time.Second
)

func (r *interactiveClaudeRunner) spawnSession(ctx context.Context, req RunRequest) (*ptySession, error) {
	args := interactiveSessionArgs(r.cfg(), req)
	cmdLine := buildWindowsCommandLine(r.claudePath, args)

	env := sessionEnv(req.OwnerLabel)
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
		cpty: cpty,
		tail: newIdleTracker(),
		buf:  &safeBuffer{},
	}
	go sess.readLoop()

	// First boot: wait for the process to settle, then blind-Enter through a
	// known onboarding dialog (trust-folder / theme picker) if one appears.
	// This mirrors the probe's finding that these are one-time-per-workdir.
	sess.waitForIdle(ctx, bootIdleQuiet, bootIdleTimeout)
	if looksLikeOnboardingPrompt(sess.buf.Since(0)) {
		sess.cpty.Write([]byte("\r"))
		sess.waitForIdle(ctx, bootIdleQuiet, bootIdleTimeout)
	}

	return sess, nil
}

// interactiveSessionArgs builds the claude CLI flags for spawning a
// persistent interactive session. Unlike workerBaseArgs (runner.go), no -p /
// --output-format / prompt-via-stdin: this is the real TUI, and the prompt is
// typed into it after spawn via ptySession.send.
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
func sessionEnv(ownerLabel string) []string {
	inherited := os.Environ()
	env := make([]string, 0, len(inherited)+1)
	for _, e := range inherited {
		if strings.HasPrefix(e, "AGLINK_OWNER_LABEL=") {
			continue
		}
		env = append(env, e)
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
type ptySession struct {
	mu   sync.Mutex // serializes Run() turns for this session
	cpty *conpty.ConPty
	tail *idleTracker
	buf  *safeBuffer // cumulative ANSI-stripped output since session start
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

// send types text into the session's input box and submits it with Enter.
// The brief pause mirrors the probe script and gives the TUI's paste
// detection time to settle before Enter is sent.
func (s *ptySession) send(text string) error {
	if _, err := s.cpty.Write([]byte(text)); err != nil {
		return err
	}
	time.Sleep(80 * time.Millisecond)
	_, err := s.cpty.Write([]byte("\r"))
	return err
}

// waitForIdle blocks until no output has arrived for `quiet`, ctx is done, or
// `timeout` elapses — whichever first. This is the turn-completion heuristic
// validated by the probe (no full VT100 screen-state parsing needed to
// detect "done", only to detect known status phrases — see
// looksLikeOnboardingPrompt).
func (s *ptySession) waitForIdle(ctx context.Context, quiet, timeout time.Duration) error {
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
