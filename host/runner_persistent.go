package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Persistent claude worker processes (runtime.persistent_worker).
//
// A one-shot worker turn spawns `claude -p ... --resume <sid>` and pays node
// start-up, MCP server start-up and the session load (~4–6s measured) before the
// model even sees the prompt. With `--input-format stream-json` the CLI instead
// stays up and reads user messages as NDJSON lines from stdin, so the pool below
// keeps ONE such process per conversation and feeds each follow-up turn into it.
//
// CLI contract, verified on claude CLI 2.1.283 (haiku, temp dir):
//   - stdin line: {"type":"user","message":{"role":"user","content":[{"type":"text","text":"..."}]}}
//   - every user message produces its own system/init, assistant…, and exactly one
//     terminal {"type":"result"} line; nothing is emitted between turns.
//   - result.usage is THIS message's usage (like a one-shot run), while
//     total_cost_usd is session-cumulative — across turns of one process AND across
//     separate --resume processes alike — so claudeTurnCost's per-session delta stays
//     correct unchanged.
//   - --session-id <new uuid> and --resume <sid> both work with stream-json input.
//   - stdin EOF: the CLI finishes and exits 0 (≈1s). This is also the orphan safety
//     net: if aglink dies without cleanup, the pipe's write end closes with it and
//     the resident claude exits on its own.
//   - --resume of a missing session: a result line (subtype error_during_execution,
//     is_error) + "No conversation found…" on stderr + exit 1. --session-id of an
//     existing session: "…is already in use." on stderr + exit 1, no stdout. Both
//     are surfaced exactly like the one-shot path, so Manager.runWorker's
//     session-recovery logic matches the same errors.

// errPersistentUnavailable means the pool can't serve this turn (the process is
// busy with another turn, the pool is shut down, or the spawn itself failed); the
// caller runs the one-shot path instead.
var errPersistentUnavailable = errors.New("persistent worker unavailable")

// defaultPersistentIdle is how long an idle resident process is kept when
// runtime.persistent_worker_idle_minutes is unset.
const defaultPersistentIdle = 10 * time.Minute

// persistentCloseGrace bounds how long a retired process may take to exit after
// its stdin is closed before its tree is killed.
const persistentCloseGrace = 5 * time.Second

// persistentPipeDelay bounds how long a resident process's output pipes are
// waited on after the process itself exited (workerWaitDelay; a var for tests).
var persistentPipeDelay = workerWaitDelay

// persistentSpec is everything that determines a resident process: the binary,
// cwd, argv (prompt excluded — it goes to stdin) and environment.
type persistentSpec struct {
	path string
	dir  string
	args []string
	env  []string // nil = inherit the parent environment
}

// persistentProc is one long-lived CLI process. The real implementation is
// cliProc; tests substitute a fake (persistentPool.spawn).
type persistentProc interface {
	send(line []byte) error // write one NDJSON message to stdin
	lines() <-chan string   // stdout lines; closed at EOF
	done() <-chan struct{}  // closed once the process has exited and been reaped
	exitErr() error         // the exit result; valid after done()
	takeStderr() string     // stderr captured since the previous call
	closeStdin()            // ask for a graceful exit
	kill()                  // kill the whole process tree now
	pid() int
}

// persistentEntry is one pooled process. busy is set while a turn owns it — at
// most one turn writes to a process at a time.
type persistentEntry struct {
	key      string
	sig      string
	proc     persistentProc // nil while the spawn is in flight
	busy     bool
	turns    int
	lastUsed time.Time
	timer    *time.Timer // idle eviction
}

// persistentPool keeps one resident claude process per conversation.
type persistentPool struct {
	spawn func(persistentSpec) (persistentProc, error)
	// limits reports the live config: whether the pool is enabled, the idle
	// eviction time and the pool size cap.
	limits func() (enabled bool, idle time.Duration, max int)

	mu      sync.Mutex
	entries map[string]*persistentEntry
	closed  bool
}

func newPersistentPool(spawn func(persistentSpec) (persistentProc, error), limits func() (bool, time.Duration, int)) *persistentPool {
	return &persistentPool{spawn: spawn, limits: limits, entries: map[string]*persistentEntry{}}
}

// persistentKey identifies a conversation's resident process: the owner label
// ("chat:<id>/conv:<id>", unique per conversation) or, without one, the session.
// "" = don't pool (no session id — the CLI would pick one of its own).
func persistentKey(req RunRequest) string {
	if strings.TrimSpace(req.SessionID) == "" {
		return ""
	}
	if req.OwnerLabel != "" {
		return req.OwnerLabel
	}
	return "session:" + req.SessionID
}

// persistentArgs turns one-shot stream-json worker args into resident ones: the
// same argv plus --input-format stream-json and --replay-user-messages right
// after -p. The replay echoes each stdin message back (with the uuid we sent),
// which marks where this turn's output starts — see persistentPool.turn.
func persistentArgs(args []string) []string {
	out := make([]string, 0, len(args)+3)
	for i, a := range args {
		out = append(out, a)
		if i == 0 && a == "-p" {
			out = append(out, "--input-format", "stream-json", "--replay-user-messages")
		}
	}
	return out
}

// persistentSignature hashes everything that shapes a resident process. A live
// process is reused only for an identical signature; any difference (model,
// workdir, plugins, system prompt, env, binary) means a new process.
//
// The session flag is normalized: `--session-id X` (the turn that created the
// session) and `--resume X` (every later turn) name the same live session, so a
// process started by the first turn serves the resumes that follow. The
// --mcp-config file's CONTENT is included, since the path stays the same when
// the plugin config behind it changes.
func persistentSignature(spec persistentSpec) string {
	h := sha256.New()
	write := func(s string) {
		io.WriteString(h, s)
		h.Write([]byte{0})
	}
	write(spec.path)
	write(spec.dir)
	for i := 0; i < len(spec.args); i++ {
		a := spec.args[i]
		if (a == "--resume" || a == "--session-id") && i+1 < len(spec.args) {
			write("session=" + spec.args[i+1])
			i++
			continue
		}
		write(a)
		if a == "--mcp-config" && i+1 < len(spec.args) {
			if data, err := os.ReadFile(spec.args[i+1]); err == nil {
				write(string(data))
			}
		}
	}
	if spec.env == nil {
		write("env=inherit")
	} else {
		for _, kv := range spec.env {
			write(kv)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// persistentUserMessage renders a prompt as one stream-json stdin line tagged
// with uuid; the CLI echoes that uuid back on the replayed message.
func persistentUserMessage(prompt, uuid string) ([]byte, error) {
	b, err := json.Marshal(map[string]any{
		"type": "user",
		"uuid": uuid,
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]string{{"type": "text", "text": prompt}},
		},
	})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// resultLineStatus reports whether line is the terminal result line and, if so,
// whether it is an error result.
func resultLineStatus(line string) (isResult, isError bool) {
	if !strings.Contains(line, `"result"`) {
		return false, false // cheap pre-filter
	}
	var probe struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		IsError bool   `json:"is_error"`
	}
	if json.Unmarshal([]byte(line), &probe) != nil || probe.Type != "result" {
		return false, false
	}
	return true, probe.IsError || (probe.Subtype != "" && probe.Subtype != "success")
}

// replayLineUUID returns the uuid of a replayed user message line
// (--replay-user-messages), or "" when line is not one.
func replayLineUUID(line string) string {
	if !strings.Contains(line, `"isReplay"`) {
		return "" // cheap pre-filter
	}
	var probe struct {
		Type     string `json:"type"`
		UUID     string `json:"uuid"`
		IsReplay bool   `json:"isReplay"`
	}
	if json.Unmarshal([]byte(line), &probe) != nil || probe.Type != "user" || !probe.IsReplay {
		return ""
	}
	return probe.UUID
}

// runTurn runs one worker turn on key's resident process and returns the same
// (stdout, stderr, err) triple execStream would, stdout holding only this turn's
// lines. errPersistentUnavailable means the caller must run the one-shot path.
//
// A reused process that fails before producing any output for this turn (it died
// while idle, stdin is broken, or it emitted something on its own between turns)
// is replaced by a fresh spawn once — nothing of the turn has run yet, so that is
// the same as a one-shot run.
func (p *persistentPool) runTurn(ctx context.Context, key string, spec persistentSpec, prompt string, onLine func(string)) (stdout, stderr string, err error) {
	id := newUUID()
	msg, merr := persistentUserMessage(prompt, id)
	if merr != nil {
		return "", "", errPersistentUnavailable
	}
	sig := persistentSignature(spec)
	for attempt := 0; ; attempt++ {
		e, reused, aerr := p.acquire(key, sig, spec)
		if aerr != nil {
			return "", "", errPersistentUnavailable
		}
		out, serr, terr, retry := p.turn(ctx, e, reused, msg, id, onLine)
		if retry && attempt == 0 && ctx.Err() == nil {
			log.Printf("[worker] persistent claude conv=%s: reused process failed before output (%v) — respawning", key, terr)
			continue
		}
		return out, serr, terr
	}
}

// acquire returns key's process marked busy: the live one when its signature
// matches, otherwise a fresh spawn (retiring a stale/dead/mismatched one first).
func (p *persistentPool) acquire(key, sig string, spec persistentSpec) (*persistentEntry, bool, error) {
	_, _, max := p.limits()
	if max < 1 {
		max = 1
	}
	var retire []retiring
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, false, errPersistentUnavailable
	}
	if e := p.entries[key]; e != nil {
		switch {
		case e.busy:
			// Lanes serialize a conversation, so this is a guard, not a path: never
			// let two turns write into one process.
			p.mu.Unlock()
			log.Printf("[worker] persistent claude conv=%s busy — running this turn one-shot", key)
			return nil, false, errPersistentUnavailable
		case e.sig != sig:
			p.removeLocked(e)
			retire = append(retire, retiring{e, "args changed"})
		case procExited(e.proc):
			p.removeLocked(e)
			retire = append(retire, retiring{e, "exited while idle"})
		default:
			e.busy = true
			e.turns++
			if e.timer != nil {
				e.timer.Stop()
			}
			p.mu.Unlock()
			log.Printf("[worker] persistent claude reuse conv=%s pid=%d turn=%d idle=%s",
				key, e.proc.pid(), e.turns, time.Since(e.lastUsed).Round(time.Second))
			return e, true, nil
		}
	}
	// Cap the pool: retire least-recently-used idle processes to make room.
	for len(p.entries) >= max {
		var lru *persistentEntry
		for _, c := range p.entries {
			if !c.busy && (lru == nil || c.lastUsed.Before(lru.lastUsed)) {
				lru = c
			}
		}
		if lru == nil {
			break // all busy — briefly exceed the cap rather than block a turn
		}
		p.removeLocked(lru)
		retire = append(retire, retiring{lru, "pool full (LRU)"})
	}
	e := &persistentEntry{key: key, sig: sig, busy: true, turns: 1}
	p.entries[key] = e
	p.mu.Unlock()
	for _, r := range retire {
		go r.retire()
	}

	proc, err := p.spawn(spec)
	p.mu.Lock()
	if err != nil {
		if p.entries[key] == e {
			delete(p.entries, key)
		}
		p.mu.Unlock()
		log.Printf("[worker] persistent claude conv=%s spawn failed: %v — running this turn one-shot", key, err)
		return nil, false, err
	}
	e.proc = proc
	closed := p.closed || p.entries[key] != e
	p.mu.Unlock()
	if closed {
		proc.kill()
		return nil, false, errPersistentUnavailable
	}
	log.Printf("[worker] persistent claude spawn conv=%s pid=%d", key, proc.pid())
	return e, false, nil
}

// turn feeds msg (tagged msgID) to e's process and reads this turn's lines up
// to its result. retry reports a reused process that failed before emitting
// anything.
//
// The CLI can run turns of its own: a resumed session with pending background
// task notifications (e.g. agents stopped when an earlier process ended) is
// answered first, with its own result ("No response requested."). Lines before
// the replay of OUR message belong to such turns — they are skipped, and their
// result does not end this turn, which would otherwise come back empty while
// the real answer arrived later as "output between turns".
func (p *persistentPool) turn(ctx context.Context, e *persistentEntry, reused bool, msg []byte, msgID string, onLine func(string)) (stdout, stderr string, err error, retry bool) {
	proc := e.proc
	if reused {
		// Anything waiting on stdout means the process did something on its own
		// since its last result (or exited) — writing now could interleave our turn
		// with that output. Start over on a fresh process instead.
		select {
		case _, ok := <-proc.lines():
			p.drop(e, "output between turns")
			if !ok {
				return "", "", errors.New("process exited"), true
			}
			return "", "", errors.New("unexpected output between turns"), true
		default:
		}
	}
	if reused {
		// Only this turn's stderr is reported. A fresh process's start-up stderr
		// (e.g. "No conversation found…") does belong to this turn — keep it.
		proc.takeStderr()
	}

	// The write runs on its own goroutine so a process that stops reading stdin
	// can't pin the turn past ctx (a kill breaks the pipe and ends the write), and
	// so stdout is consumed while a large prompt is still being written.
	sendErr := make(chan error, 1)
	go func() { sendErr <- proc.send(msg) }()

	// Every stdout line is progress: it resets the turn watchdog's idle window
	// (turnwatch.go), exactly as in execStream.
	act := turnActivityFrom(ctx)
	var outBuf bytes.Buffer
	started := false // the replay of msgID was seen (see the doc comment)
	// pre holds lines from before the replay. They are not this turn's output,
	// but when the process dies before ever reading our message (e.g. --resume
	// of a missing session) they are all there is, and the caller's recovery
	// matching needs them exactly as a one-shot run would have printed them.
	var pre bytes.Buffer
	stdoutText := func() string {
		if started {
			return outBuf.String()
		}
		return pre.String() + outBuf.String()
	}
	for {
		select {
		case <-ctx.Done():
			// Watchdog stop or !cancel: kill the tree and forget the process; the
			// next turn respawns with --resume.
			p.drop(e, "cancelled")
			return stdoutText(), proc.takeStderr(), ctx.Err(), false
		case werr := <-sendErr:
			sendErr = nil
			if werr == nil {
				continue
			}
			// The process can't take the message (it exited, or its stdin broke).
			// Let it end the way a one-shot run would and report its own exit
			// status/stdout/stderr (e.g. "No conversation found…"), so the
			// manager's recovery sees the same errors.
			p.forget(e)
			log.Printf("[worker] persistent claude evict conv=%s pid=%d reason=stdin write failed (%v)", e.key, proc.pid(), werr)
			proc.closeStdin()
			p.drainUntilExit(ctx, proc, &outBuf, onLine)
			if ctx.Err() != nil {
				return stdoutText(), proc.takeStderr(), ctx.Err(), false
			}
			err := proc.exitErr()
			if err == nil {
				err = fmt.Errorf("stdin write: %w", werr)
			}
			return stdoutText(), proc.takeStderr(), err, reused && outBuf.Len() == 0
		case line, ok := <-proc.lines():
			if !ok {
				// Exited before this turn's result: report it like a one-shot run
				// whose process ended (exit error + stderr, partial stdout).
				p.forget(e)
				<-proc.done()
				return stdoutText(), proc.takeStderr(), proc.exitErr(), reused && outBuf.Len() == 0
			}
			act.Touch()
			if id := replayLineUUID(line); id != "" {
				if id == msgID {
					started = true
				}
				continue
			}
			if !started {
				pre.WriteString(line)
				pre.WriteByte(10)
				continue
			}
			outBuf.WriteString(line)
			outBuf.WriteByte('\n')
			if onLine != nil {
				onLine(line)
			}
			isResult, isErr := resultLineStatus(line)
			if !isResult {
				continue
			}
			if !isErr {
				p.release(e)
				return outBuf.String(), proc.takeStderr(), nil, false
			}
			// Error result: retire the process the way a one-shot run ends (stdin
			// EOF, exit) so the caller sees the same exit status/stderr and the
			// manager's recovery logic applies unchanged.
			p.forget(e)
			proc.closeStdin()
			p.drainUntilExit(ctx, proc, &outBuf, onLine)
			return outBuf.String(), proc.takeStderr(), proc.exitErr(), false
		}
	}
}

// drainUntilExit collects proc's remaining output until it exits, killing it if
// it doesn't within persistentCloseGrace or ctx is cancelled.
func (p *persistentPool) drainUntilExit(ctx context.Context, proc persistentProc, outBuf *bytes.Buffer, onLine func(string)) {
	act := turnActivityFrom(ctx)
	grace := time.NewTimer(persistentCloseGrace)
	defer grace.Stop()
	ctxDone := ctx.Done()
	for {
		select {
		case <-ctxDone:
			proc.kill()
			ctxDone = nil
		case <-grace.C:
			proc.kill()
		case line, ok := <-proc.lines():
			if !ok {
				<-proc.done()
				return
			}
			act.Touch()
			outBuf.WriteString(line)
			outBuf.WriteByte('\n')
			if onLine != nil {
				onLine(line)
			}
		}
	}
}

// release returns e to the pool after a successful turn and arms its idle
// eviction — or retires it when the pool was disabled meanwhile.
func (p *persistentPool) release(e *persistentEntry) {
	enabled, idle, _ := p.limits()
	if idle <= 0 {
		idle = defaultPersistentIdle
	}
	p.mu.Lock()
	if p.entries[e.key] != e {
		p.mu.Unlock()
		return // removed meanwhile (shutdown) — whoever removed it retires it
	}
	if !enabled || p.closed {
		p.removeLocked(e)
		p.mu.Unlock()
		go retiring{e, "disabled"}.retire()
		return
	}
	e.busy = false
	e.lastUsed = time.Now()
	e.timer = time.AfterFunc(idle, func() { p.evictIdle(e, idle) })
	p.mu.Unlock()
}

// evictIdle retires e if it is still pooled and has stayed idle.
func (p *persistentPool) evictIdle(e *persistentEntry, idle time.Duration) {
	p.mu.Lock()
	if p.entries[e.key] != e || e.busy || time.Since(e.lastUsed) < idle {
		p.mu.Unlock()
		return
	}
	p.removeLocked(e)
	p.mu.Unlock()
	retiring{e, "idle"}.retire()
}

// drop removes e from the pool and kills its process tree.
func (p *persistentPool) drop(e *persistentEntry, reason string) {
	p.forget(e)
	log.Printf("[worker] persistent claude evict conv=%s pid=%d reason=%s", e.key, e.proc.pid(), reason)
	e.proc.kill()
}

// forget removes e from the pool without touching its process.
func (p *persistentPool) forget(e *persistentEntry) {
	p.mu.Lock()
	p.removeLocked(e)
	p.mu.Unlock()
}

func (p *persistentPool) removeLocked(e *persistentEntry) {
	if p.entries[e.key] == e {
		delete(p.entries, e.key)
	}
	if e.timer != nil {
		e.timer.Stop()
	}
}

// retireIdle retires every idle process (runtime.persistent_worker switched
// off). Busy ones are retired by release when their turn ends.
func (p *persistentPool) retireIdle(reason string) {
	p.mu.Lock()
	var victims []*persistentEntry
	for _, e := range p.entries {
		if !e.busy {
			p.removeLocked(e)
			victims = append(victims, e)
		}
	}
	p.mu.Unlock()
	for _, e := range victims {
		go retiring{e, reason}.retire()
	}
}

// Close retires every process (host shutdown) and waits for them to exit. Safe
// to call more than once.
func (p *persistentPool) Close() {
	p.mu.Lock()
	p.closed = true
	var victims []*persistentEntry
	for _, e := range p.entries {
		p.removeLocked(e)
		if e.proc != nil {
			victims = append(victims, e)
		}
	}
	p.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range victims {
		wg.Add(1)
		go func(e *persistentEntry) {
			defer wg.Done()
			retiring{e, "shutdown"}.retire()
		}(e)
	}
	wg.Wait()
}

// size reports the number of pooled processes (tests).
func (p *persistentPool) size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

// retiring is a removed entry on its way out.
type retiring struct {
	e      *persistentEntry
	reason string
}

// retire closes the process's stdin and kills its tree if it hasn't exited
// within persistentCloseGrace. Blocks until the process is reaped.
func (r retiring) retire() {
	proc := r.e.proc
	if proc == nil {
		return
	}
	log.Printf("[worker] persistent claude evict conv=%s pid=%d reason=%s turns=%d", r.e.key, proc.pid(), r.reason, r.e.turns)
	proc.closeStdin()
	select {
	case <-proc.done():
		return
	case <-time.After(persistentCloseGrace):
	}
	proc.kill()
	<-proc.done()
}

func procExited(proc persistentProc) bool {
	select {
	case <-proc.done():
		return true
	default:
		return false
	}
}

// --- real process ---

// cliProc is a resident claude CLI process.
type cliProc struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      *os.File // parent's read end of the child's stdout pipe
	out         chan string
	quit        chan struct{} // closed on kill: the reader then discards instead of forwarding
	exited      chan struct{} // closed once Wait returned (the process itself is gone)
	fin         chan struct{} // closed once Wait returned AND stdout hit EOF
	waitErr     error
	err         error
	forcedClose atomic.Bool // stdout closed by us after persistentPipeDelay

	stdinOnce sync.Once
	quitOnce  sync.Once
	errBuf    lockedBuffer
}

// startCLIProc starts spec as a resident process. It is not tied to any turn's
// context: turns end, the process stays. Cancellation is explicit (kill).
//
// stdout is a pipe of our own rather than cmd.StdoutPipe: the reader must be
// able to give up on it. Anything the CLI spawns (a server a Bash tool started)
// inherits the write end and can hold it open long after claude itself has
// exited — with StdoutPipe the reader would then block forever, done() would
// never close, and retiring the process (idle eviction, host shutdown) would
// hang. Instead, persistentPipeDelay after the process exits the read end is closed,
// mirroring cmd.WaitDelay on the one-shot path.
func startCLIProc(spec persistentSpec) (persistentProc, error) {
	cmd := exec.Command(spec.path, spec.args...)
	cmd.Dir = spec.dir
	if spec.env != nil {
		cmd.Env = spec.env
	}
	p := &cliProc{cmd: cmd, out: make(chan string, 256), quit: make(chan struct{}),
		exited: make(chan struct{}), fin: make(chan struct{})}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	cmd.Stdout = pw
	cmd.Stderr = &p.errBuf
	// Bounds Wait on the stderr copier the same way (see workerWaitDelay).
	cmd.WaitDelay = persistentPipeDelay
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return nil, err
	}
	_ = pw.Close() // the child holds its own copy
	p.stdin = stdin
	p.stdout = pr
	readerDone := make(chan struct{})
	go p.waitLoop(readerDone)
	go p.readLoop(readerDone)
	return p, nil
}

// waitLoop reaps the process, then bounds how long the reader may keep waiting
// for stdout EOF.
func (p *cliProc) waitLoop(readerDone <-chan struct{}) {
	p.waitErr = ignoreWaitDelay(p.cmd.Wait(), "claude")
	close(p.exited)
	t := time.NewTimer(persistentPipeDelay)
	defer t.Stop()
	select {
	case <-readerDone:
	case <-t.C:
		log.Printf("[claude] persistent pid=%d: stdout still held open by a process it spawned — closing it", p.cmd.Process.Pid)
		p.forcedClose.Store(true)
		_ = p.stdout.Close()
	}
}

func (p *cliProc) readLoop(readerDone chan<- struct{}) {
	scanner := bufio.NewScanner(p.stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024) // tool output lines can be large
	for scanner.Scan() {
		select {
		case p.out <- scanner.Text():
		case <-p.quit: // killed — keep draining so the process can't block on a full pipe
		}
	}
	scanErr := scanner.Err()
	if p.forcedClose.Load() {
		scanErr = nil // our own close after the process exited: output so far is complete
	}
	if scanErr != nil {
		// Can't read further (e.g. a line over the buffer): the process would block
		// on a full pipe, so end it.
		p.kill()
	}
	close(readerDone)
	close(p.out)
	<-p.exited
	_ = p.stdout.Close()
	p.err = p.waitErr
	if scanErr != nil {
		p.err = fmt.Errorf("stream read error: %w", scanErr)
	}
	close(p.fin)
}

func (p *cliProc) send(line []byte) error {
	_, err := p.stdin.Write(line)
	return err
}
func (p *cliProc) lines() <-chan string  { return p.out }
func (p *cliProc) done() <-chan struct{} { return p.fin }
func (p *cliProc) exitErr() error        { return p.err }
func (p *cliProc) takeStderr() string    { return p.errBuf.take() }
func (p *cliProc) pid() int              { return p.cmd.Process.Pid }
func (p *cliProc) closeStdin()           { p.stdinOnce.Do(func() { _ = p.stdin.Close() }) }

// kill ends the whole process tree (claude spawns node + MCP server children; on
// Windows it is a .cmd shim under cmd.exe), like execStream's cmd.Cancel.
func (p *cliProc) kill() {
	p.quitOnce.Do(func() { close(p.quit) })
	select {
	case <-p.exited: // already reaped — its PID may belong to someone else now
	default:
		_ = killTree(p.cmd.Process.Pid)
	}
	p.closeStdin()
}

// lockedBuffer is a bytes.Buffer safe for the os/exec stderr copier and a reader.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// take returns and clears the buffered content.
func (b *lockedBuffer) take() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.buf.String()
	b.buf.Reset()
	return s
}
