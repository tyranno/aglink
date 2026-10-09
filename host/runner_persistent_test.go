package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- fake process seam ---

// fakeProc is a scripted persistentProc: respond is called for every message
// written to stdin and pushes the turn's stdout lines.
type fakeProc struct {
	n       int
	out     chan string
	fin     chan struct{}
	respond func(f *fakeProc, turn int, msg string)

	mu          sync.Mutex
	sent        []string
	killed      bool
	stdinClosed bool
	err         error
	finOnce     sync.Once
	stderr      string
}

func newFakeProc(n int, respond func(f *fakeProc, turn int, msg string)) *fakeProc {
	return &fakeProc{n: n, out: make(chan string, 64), fin: make(chan struct{}), respond: respond}
}

func (f *fakeProc) exit(err error) {
	f.finOnce.Do(func() {
		f.mu.Lock()
		f.err = err
		f.mu.Unlock()
		close(f.out)
		close(f.fin)
	})
}

func (f *fakeProc) send(line []byte) error {
	f.mu.Lock()
	if f.killed || f.stdinClosed || procExited(f) {
		f.mu.Unlock()
		return errors.New("broken pipe")
	}
	f.sent = append(f.sent, string(line))
	turn := len(f.sent)
	f.mu.Unlock()
	if f.respond != nil {
		f.respond(f, turn, string(line))
	}
	return nil
}
func (f *fakeProc) lines() <-chan string  { return f.out }
func (f *fakeProc) done() <-chan struct{} { return f.fin }
func (f *fakeProc) exitErr() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}
func (f *fakeProc) takeStderr() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.stderr
	f.stderr = ""
	return s
}
func (f *fakeProc) closeStdin() {
	f.mu.Lock()
	f.stdinClosed = true
	f.mu.Unlock()
	f.exit(nil)
}
func (f *fakeProc) kill() {
	f.mu.Lock()
	f.killed = true
	f.mu.Unlock()
	f.exit(errors.New("killed"))
}
func (f *fakeProc) pid() int { return 1000 + f.n }

func (f *fakeProc) isKilled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.killed
}

func (f *fakeProc) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// emit pushes stream-json lines (a stand-in for the CLI's stdout).
func (f *fakeProc) emit(lines ...string) {
	for _, l := range lines {
		f.out <- l
	}
}

// okTurn answers every message like the real CLI: init, an assistant message
// with usage, then a success result echoing the prompt.
func okTurn(f *fakeProc, turn int, msg string) {
	var m struct {
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	_ = json.Unmarshal([]byte(msg), &m)
	text := ""
	if len(m.Message.Content) > 0 {
		text = m.Message.Content[0].Text
	}
	f.emit(
		`{"type":"system","subtype":"init","session_id":"s"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":10,"cache_read_input_tokens":1000,"cache_creation_input_tokens":5}}}`,
		fmt.Sprintf(`{"type":"result","subtype":"success","is_error":false,"result":"reply %d: %s","session_id":"s","total_cost_usd":0.01,"usage":{"input_tokens":10,"cache_read_input_tokens":1000,"cache_creation_input_tokens":5,"output_tokens":7}}`, turn, text),
	)
}

// fakeSpawner records every spawned fakeProc.
type fakeSpawner struct {
	mu      sync.Mutex
	procs   []*fakeProc
	specs   []persistentSpec
	respond func(f *fakeProc, turn int, msg string)
	fail    error
	onSpawn func(f *fakeProc) // runs before the process is handed out
}

func (s *fakeSpawner) spawn(spec persistentSpec) (persistentProc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return nil, s.fail
	}
	p := newFakeProc(len(s.procs), s.respond)
	if s.onSpawn != nil {
		s.onSpawn(p)
	}
	s.procs = append(s.procs, p)
	s.specs = append(s.specs, spec)
	return p, nil
}

func (s *fakeSpawner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.procs)
}

func (s *fakeSpawner) proc(i int) *fakeProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.procs[i]
}

func testPool(sp *fakeSpawner, idle time.Duration, max int) *persistentPool {
	return newPersistentPool(sp.spawn, func() (bool, time.Duration, int) { return true, idle, max })
}

func workerSpec(model, sessionFlag, sid string) persistentSpec {
	return persistentSpec{path: "claude", dir: "C:/w", args: persistentArgs([]string{
		"-p", "--output-format", "stream-json", "--verbose", "--model", model, sessionFlag, sid,
	})}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// --- tests ---

func TestPersistentArgsInsertsInputFormat(t *testing.T) {
	got := persistentArgs([]string{"-p", "--output-format", "stream-json", "--resume", "x"})
	want := "-p --input-format stream-json --output-format stream-json --resume x"
	if strings.Join(got, " ") != want {
		t.Fatalf("persistentArgs = %q, want %q", strings.Join(got, " "), want)
	}
}

func TestPersistentUserMessageFormat(t *testing.T) {
	b, err := persistentUserMessage("안녕 \"q\"\nline2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), "\n") || strings.Count(string(b), "\n") != 1 {
		t.Fatalf("message must be exactly one NDJSON line: %q", b)
	}
	var m struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.Type != "user" || m.Message.Role != "user" || len(m.Message.Content) != 1 ||
		m.Message.Content[0].Type != "text" || m.Message.Content[0].Text != "안녕 \"q\"\nline2" {
		t.Fatalf("unexpected message: %+v", m)
	}
}

func TestPersistentKey(t *testing.T) {
	if k := persistentKey(RunRequest{SessionID: ""}); k != "" {
		t.Errorf("empty session must not pool, got %q", k)
	}
	if k := persistentKey(RunRequest{SessionID: "s", OwnerLabel: "chat:1/conv:c"}); k != "chat:1/conv:c" {
		t.Errorf("key = %q", k)
	}
	if k := persistentKey(RunRequest{SessionID: "s"}); k != "session:s" {
		t.Errorf("key = %q", k)
	}
}

func TestPersistentSignature(t *testing.T) {
	a := persistentSignature(workerSpec("opus", "--session-id", "S1"))
	if b := persistentSignature(workerSpec("opus", "--resume", "S1")); a != b {
		t.Error("--session-id X and --resume X must share a signature (same live session)")
	}
	if b := persistentSignature(workerSpec("opus", "--resume", "S2")); a == b {
		t.Error("different session must change the signature")
	}
	if b := persistentSignature(workerSpec("haiku", "--resume", "S1")); a == b {
		t.Error("different model must change the signature")
	}
	s := workerSpec("opus", "--resume", "S1")
	s.env = []string{"A=1"}
	if b := persistentSignature(s); a == b {
		t.Error("different env must change the signature")
	}
	s = workerSpec("opus", "--resume", "S1")
	s.dir = "C:/other"
	if b := persistentSignature(s); a == b {
		t.Error("different workdir must change the signature")
	}

	// Same --mcp-config path, different content → different signature.
	f := t.TempDir() + "/mcp.json"
	withMCP := func() string {
		sp := workerSpec("opus", "--resume", "S1")
		sp.args = append(sp.args, "--mcp-config", f)
		return persistentSignature(sp)
	}
	_ = os.WriteFile(f, []byte(`{"mcpServers":{}}`), 0o600)
	x := withMCP()
	_ = os.WriteFile(f, []byte(`{"mcpServers":{"web":{}}}`), 0o600)
	if y := withMCP(); x == y {
		t.Error("changed --mcp-config content must change the signature")
	}
}

func TestPersistentPool_ReusesProcessAcrossTurns(t *testing.T) {
	sp := &fakeSpawner{respond: okTurn}
	p := testPool(sp, time.Minute, 3)
	defer p.Close()
	ctx := context.Background()

	out1, _, err := p.runTurn(ctx, "conv", workerSpec("opus", "--session-id", "S"), "first", nil)
	if err != nil {
		t.Fatal(err)
	}
	out2, _, err := p.runTurn(ctx, "conv", workerSpec("opus", "--resume", "S"), "second", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sp.count() != 1 {
		t.Fatalf("spawned %d processes, want 1 (reuse)", sp.count())
	}
	if sp.proc(0).sentCount() != 2 {
		t.Fatalf("process got %d messages, want 2", sp.proc(0).sentCount())
	}
	// Each turn's stdout holds only that turn's lines.
	r1, err := parseStreamResult(out1)
	if err != nil || r1.Text != "reply 1: first" {
		t.Fatalf("turn 1 = %+v, %v", r1, err)
	}
	r2, err := parseStreamResult(out2)
	if err != nil || r2.Text != "reply 2: second" || strings.Contains(out2, "first") {
		t.Fatalf("turn 2 = %+v, %v (out=%q)", r2, err, out2)
	}
	if r2.ContextTokens != 1015 {
		t.Errorf("ContextTokens = %d, want 1015", r2.ContextTokens)
	}
}

func TestPersistentPool_RespawnsOnArgChange(t *testing.T) {
	sp := &fakeSpawner{respond: okTurn}
	p := testPool(sp, time.Minute, 3)
	defer p.Close()
	ctx := context.Background()

	if _, _, err := p.runTurn(ctx, "conv", workerSpec("opus", "--resume", "S"), "a", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.runTurn(ctx, "conv", workerSpec("haiku", "--resume", "S"), "b", nil); err != nil {
		t.Fatal(err)
	}
	if sp.count() != 2 {
		t.Fatalf("model change: spawned %d, want 2", sp.count())
	}
	waitUntil(t, "old process retired", func() bool { return procExited(sp.proc(0)) })
	// Session reset (new id) → new process too.
	if _, _, err := p.runTurn(ctx, "conv", workerSpec("haiku", "--session-id", "S2"), "c", nil); err != nil {
		t.Fatal(err)
	}
	if sp.count() != 3 {
		t.Fatalf("session change: spawned %d, want 3", sp.count())
	}
	if !strings.Contains(strings.Join(sp.specs[2].args, " "), "--session-id S2") {
		t.Errorf("new process args = %v", sp.specs[2].args)
	}
	if p.size() != 1 {
		t.Errorf("pool size = %d, want 1", p.size())
	}
}

func TestPersistentPool_IdleEviction(t *testing.T) {
	sp := &fakeSpawner{respond: okTurn}
	p := testPool(sp, 30*time.Millisecond, 3)
	defer p.Close()
	if _, _, err := p.runTurn(context.Background(), "conv", workerSpec("opus", "--resume", "S"), "a", nil); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "idle eviction", func() bool { return p.size() == 0 && procExited(sp.proc(0)) })
	if sp.proc(0).isKilled() {
		t.Error("idle eviction should close stdin gracefully, not kill")
	}
}

func TestPersistentPool_LRUCap(t *testing.T) {
	sp := &fakeSpawner{respond: okTurn}
	p := testPool(sp, time.Minute, 2)
	defer p.Close()
	ctx := context.Background()
	for _, k := range []string{"a", "b"} {
		if _, _, err := p.runTurn(ctx, k, workerSpec("opus", "--resume", k), "x", nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Touch "a" so "b" becomes least recently used.
	if _, _, err := p.runTurn(ctx, "a", workerSpec("opus", "--resume", "a"), "y", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.runTurn(ctx, "c", workerSpec("opus", "--resume", "c"), "z", nil); err != nil {
		t.Fatal(err)
	}
	if p.size() != 2 {
		t.Fatalf("pool size = %d, want 2", p.size())
	}
	waitUntil(t, "LRU (b) retired", func() bool { return procExited(sp.proc(1)) })
	if procExited(sp.proc(0)) {
		t.Error("recently used process (a) must survive")
	}
}

func TestPersistentPool_CancelKillsAndDrops(t *testing.T) {
	sp := &fakeSpawner{respond: func(f *fakeProc, turn int, msg string) {
		if turn == 1 && f.n == 0 {
			f.emit(`{"type":"system","subtype":"init"}`) // then hangs
			return
		}
		okTurn(f, turn, msg)
	}}
	p := testPool(sp, time.Minute, 3)
	defer p.Close()

	start := time.Now().Add(-time.Hour)
	act := newTurnActivity(start, time.Hour, start.Add(2*time.Hour))
	ctx, cancel := context.WithCancel(withTurnActivity(context.Background(), act))
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	_, _, err := p.runTurn(ctx, "conv", workerSpec("opus", "--resume", "S"), "a", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !sp.proc(0).isKilled() {
		t.Error("cancelled turn must kill the process")
	}
	if p.size() != 0 {
		t.Error("cancelled process must leave the pool")
	}
	if !act.Last().After(start) {
		t.Error("stdout line must touch the turn activity")
	}
	// Next turn respawns.
	out, _, err := p.runTurn(context.Background(), "conv", workerSpec("opus", "--resume", "S"), "b", nil)
	if err != nil || !strings.Contains(out, "reply 1: b") || sp.count() != 2 {
		t.Fatalf("respawn turn: out=%q err=%v spawns=%d", out, err, sp.count())
	}
}

func TestPersistentPool_DeadIdleProcessIsReplaced(t *testing.T) {
	sp := &fakeSpawner{respond: okTurn}
	p := testPool(sp, time.Minute, 3)
	defer p.Close()
	ctx := context.Background()
	if _, _, err := p.runTurn(ctx, "conv", workerSpec("opus", "--resume", "S"), "a", nil); err != nil {
		t.Fatal(err)
	}
	sp.proc(0).exit(errors.New("crashed")) // dies while idle
	out, _, err := p.runTurn(ctx, "conv", workerSpec("opus", "--resume", "S"), "b", nil)
	if err != nil || !strings.Contains(out, "reply 1: b") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if sp.count() != 2 {
		t.Fatalf("spawns = %d, want 2", sp.count())
	}
}

// A reused process that dies on the new message without emitting anything is
// replaced once — nothing of the turn ran yet.
func TestPersistentPool_ReusedProcessDiesBeforeOutputFallsBack(t *testing.T) {
	sp := &fakeSpawner{respond: func(f *fakeProc, turn int, msg string) {
		if f.n == 0 && turn == 2 {
			go f.exit(errors.New("exit status 1"))
			return
		}
		okTurn(f, turn, msg)
	}}
	p := testPool(sp, time.Minute, 3)
	defer p.Close()
	ctx := context.Background()
	if _, _, err := p.runTurn(ctx, "conv", workerSpec("opus", "--resume", "S"), "a", nil); err != nil {
		t.Fatal(err)
	}
	out, _, err := p.runTurn(ctx, "conv", workerSpec("opus", "--resume", "S"), "b", nil)
	if err != nil || !strings.Contains(out, "reply 1: b") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if sp.count() != 2 {
		t.Fatalf("spawns = %d, want 2", sp.count())
	}
}

// Output between turns (the CLI acted on its own) → don't interleave; respawn.
func TestPersistentPool_StrayOutputBetweenTurnsRespawns(t *testing.T) {
	sp := &fakeSpawner{respond: okTurn}
	p := testPool(sp, time.Minute, 3)
	defer p.Close()
	ctx := context.Background()
	if _, _, err := p.runTurn(ctx, "conv", workerSpec("opus", "--resume", "S"), "a", nil); err != nil {
		t.Fatal(err)
	}
	sp.proc(0).emit(`{"type":"system","subtype":"task_notification"}`)
	out, _, err := p.runTurn(ctx, "conv", workerSpec("opus", "--resume", "S"), "b", nil)
	if err != nil || !strings.Contains(out, "reply 1: b") || strings.Contains(out, "task_notification") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if !sp.proc(0).isKilled() || sp.count() != 2 {
		t.Fatalf("stray-output process must be killed and replaced (spawns=%d)", sp.count())
	}
}

// A fresh process that exits without a result (e.g. "--session-id … already in
// use") reports exit error + stderr exactly like the one-shot path, so the
// manager's recovery sees the same error text. No retry: it was not reused.
func TestPersistentPool_FreshProcessFailureSurfacesStderr(t *testing.T) {
	sp := &fakeSpawner{respond: func(f *fakeProc, turn int, msg string) {
		f.mu.Lock()
		f.stderr = "Error: Session ID S is already in use.\n"
		f.mu.Unlock()
		go f.exit(errors.New("exit status 1"))
	}}
	p := testPool(sp, time.Minute, 3)
	defer p.Close()
	_, stderr, err := p.runTurn(context.Background(), "conv", workerSpec("opus", "--session-id", "S"), "a", nil)
	if err == nil || err.Error() != "exit status 1" {
		t.Fatalf("err = %v", err)
	}
	if !isSessionAlreadyInUse(stderr) {
		t.Fatalf("stderr = %q", stderr)
	}
	if sp.count() != 1 || p.size() != 0 {
		t.Fatalf("spawns=%d size=%d", sp.count(), p.size())
	}
}

// An error result retires the process the way a one-shot run ends.
func TestPersistentPool_ErrorResultRetiresProcess(t *testing.T) {
	sp := &fakeSpawner{respond: func(f *fakeProc, turn int, msg string) {
		f.mu.Lock()
		f.stderr = "No conversation found with session ID: S\n"
		f.mu.Unlock()
		f.emit(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"S","total_cost_usd":0}`)
	}}
	p := testPool(sp, time.Minute, 3)
	defer p.Close()
	out, stderr, err := p.runTurn(context.Background(), "conv", workerSpec("opus", "--resume", "S"), "a", nil)
	if err != nil { // the fake exits 0 on stdin close
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "error_during_execution") || !isSessionNotFound(stderr) {
		t.Fatalf("out=%q stderr=%q", out, stderr)
	}
	if p.size() != 0 || !procExited(sp.proc(0)) {
		t.Fatal("error-result process must be retired")
	}
}

func TestPersistentPool_BusyGuard(t *testing.T) {
	release := make(chan struct{})
	sp := &fakeSpawner{respond: func(f *fakeProc, turn int, msg string) {
		go func() { <-release; okTurn(f, turn, msg) }()
	}}
	p := testPool(sp, time.Minute, 3)
	defer p.Close()
	done := make(chan error, 1)
	go func() {
		_, _, err := p.runTurn(context.Background(), "conv", workerSpec("opus", "--resume", "S"), "a", nil)
		done <- err
	}()
	waitUntil(t, "first turn running", func() bool { return sp.count() == 1 && sp.proc(0).sentCount() == 1 })
	if _, _, err := p.runTurn(context.Background(), "conv", workerSpec("opus", "--resume", "S"), "b", nil); !errors.Is(err, errPersistentUnavailable) {
		t.Fatalf("concurrent turn err = %v, want errPersistentUnavailable", err)
	}
	if sp.proc(0).sentCount() != 1 {
		t.Fatal("second turn must not write to the busy process")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPersistentPool_DisabledRetiresOnRelease(t *testing.T) {
	sp := &fakeSpawner{respond: okTurn}
	var mu sync.Mutex
	enabled := true
	p := newPersistentPool(sp.spawn, func() (bool, time.Duration, int) {
		mu.Lock()
		defer mu.Unlock()
		return enabled, time.Minute, 3
	})
	defer p.Close()
	if _, _, err := p.runTurn(context.Background(), "a", workerSpec("opus", "--resume", "S"), "x", nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	enabled = false
	mu.Unlock()
	if _, _, err := p.runTurn(context.Background(), "b", workerSpec("opus", "--resume", "T"), "x", nil); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "process retired after disable", func() bool { return p.size() == 1 && procExited(sp.proc(1)) })
	p.retireIdle("disabled")
	waitUntil(t, "idle process retired", func() bool { return p.size() == 0 && procExited(sp.proc(0)) })
}

func TestPersistentPool_CloseRetiresAll(t *testing.T) {
	sp := &fakeSpawner{respond: okTurn}
	p := testPool(sp, time.Minute, 3)
	for _, k := range []string{"a", "b"} {
		if _, _, err := p.runTurn(context.Background(), k, workerSpec("opus", "--resume", k), "x", nil); err != nil {
			t.Fatal(err)
		}
	}
	p.Close()
	if !procExited(sp.proc(0)) || !procExited(sp.proc(1)) || p.size() != 0 {
		t.Fatal("Close must retire every process")
	}
	if _, _, err := p.runTurn(context.Background(), "a", workerSpec("opus", "--resume", "a"), "x", nil); !errors.Is(err, errPersistentUnavailable) {
		t.Fatalf("after Close err = %v, want errPersistentUnavailable", err)
	}
	p.Close() // idempotent
}

func TestPersistentPool_SpawnFailureFallsBack(t *testing.T) {
	sp := &fakeSpawner{fail: errors.New("exec: not found")}
	p := testPool(sp, time.Minute, 3)
	if _, _, err := p.runTurn(context.Background(), "a", workerSpec("opus", "--resume", "a"), "x", nil); !errors.Is(err, errPersistentUnavailable) {
		t.Fatalf("err = %v, want errPersistentUnavailable", err)
	}
}

// Through claudeRunner.Run: RunResult fields and OnProgress match the one-shot
// path, and the second turn reuses the process.
func TestClaudeRunner_PersistentRun(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	sp := &fakeSpawner{respond: okTurn}
	r := NewClaudeRunner("claude", NewConfigHolder(&Config{PersistentWorker: true, MaxWorkers: 3}))
	r.pool = newPersistentPool(sp.spawn, r.persistentLimits)
	defer r.Close()

	var mu sync.Mutex
	var progress []string
	onProgress := func(s string) { mu.Lock(); progress = append(progress, s); mu.Unlock() }
	req := RunRequest{Prompt: "hi", WorkDir: t.TempDir(), SessionID: "S", Model: "opus", OwnerLabel: "chat:1/conv:c", OnProgress: onProgress}
	res, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "reply 1: hi" || res.CacheReadTokens != 1000 || res.OutputTokens != 7 || res.CostUSD != 0.01 || res.ContextTokens != 1015 {
		t.Fatalf("res = %+v", res)
	}
	req.Resume = true
	req.Prompt = "again"
	res, err = r.Run(context.Background(), req)
	if err != nil || res.Text != "reply 2: again" {
		t.Fatalf("turn 2: %+v %v", res, err)
	}
	if sp.count() != 1 {
		t.Fatalf("spawns = %d, want 1", sp.count())
	}
	args := strings.Join(sp.specs[0].args, " ")
	if !strings.HasPrefix(args, "-p --input-format stream-json --output-format stream-json") || !strings.Contains(args, "--session-id S") {
		t.Errorf("args = %q", args)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(progress) != 2 || progress[0] != "🔧 Bash: ls" {
		t.Errorf("progress = %q", progress)
	}
}

// --- real process plumbing (helper process stands in for the CLI) ---

// TestHelperFakePersistentClaude reads stream-json user messages from stdin and
// answers each with a result line, until stdin EOF.
func TestHelperFakePersistentClaude(t *testing.T) {
	if os.Getenv("AGLINK_FAKE_PERSISTENT") == "" {
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	n := 0
	for sc.Scan() {
		n++
		fmt.Printf("{\"type\":\"system\",\"subtype\":\"init\"}\n")
		if strings.Contains(sc.Text(), "orphan") {
			c := exec.Command(os.Args[0], "-test.run=^TestHelperOrphanHolder$")
			c.Env = append(os.Environ(), "AGLINK_FAKE_ORPHAN=1")
			c.Stdout = os.Stdout
			_ = c.Start()
		}
		if strings.Contains(sc.Text(), "hang") {
			time.Sleep(time.Minute)
		}
		fmt.Printf("{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"turn %d\"}\n", n)
	}
	fmt.Fprintln(os.Stderr, "bye")
	os.Exit(0)
}

func helperSpec(t *testing.T) persistentSpec {
	return persistentSpec{
		path: os.Args[0],
		dir:  os.TempDir(), // not t.TempDir: an orphan helper may still hold it as cwd at cleanup
		args: []string{"-test.run=^TestHelperFakePersistentClaude$"},
		env:  append(os.Environ(), "AGLINK_FAKE_PERSISTENT=1"),
	}
}

func TestCLIProc_TwoTurnsThenGracefulExit(t *testing.T) {
	p := newPersistentPool(startCLIProc, func() (bool, time.Duration, int) { return true, time.Minute, 2 })
	spec := helperSpec(t)
	for i := 1; i <= 2; i++ {
		out, _, err := p.runTurn(context.Background(), "k", spec, "hello", nil)
		if err != nil || !strings.Contains(out, fmt.Sprintf(`"turn %d"`, i)) {
			t.Fatalf("turn %d: out=%q err=%v", i, out, err)
		}
	}
	p.Close() // closes stdin → helper exits 0
	if p.size() != 0 {
		t.Fatal("pool not empty after Close")
	}
}

func TestCLIProc_CancelKillsProcess(t *testing.T) {
	p := newPersistentPool(startCLIProc, func() (bool, time.Duration, int) { return true, time.Minute, 2 })
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_, _, err := p.runTurn(ctx, "k", helperSpec(t), "hang", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if el := time.Since(start); el > 15*time.Second {
		t.Fatalf("cancel took %v", el)
	}
	if p.size() != 0 {
		t.Fatal("cancelled process must leave the pool")
	}
}

// A process that is already gone when the message is written (e.g. --resume of a
// missing session failing at start-up) reports its own stdout, stderr and exit
// status — not a bare "stdin write" error — so the manager's session-recovery
// matching works as on the one-shot path.
func TestPersistentPool_StdinWriteFailureSurfacesProcessOutput(t *testing.T) {
	sp := &fakeSpawner{onSpawn: func(f *fakeProc) {
		f.stderr = "No conversation found with session ID: S\n"
		f.emit(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"S"}`)
		f.exit(errors.New("exit status 1"))
	}}
	p := testPool(sp, time.Minute, 3)
	defer p.Close()
	out, stderr, err := p.runTurn(context.Background(), "conv", workerSpec("opus", "--resume", "S"), "a", nil)
	if err == nil || err.Error() != "exit status 1" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "error_during_execution") || !isSessionNotFound(stderr) {
		t.Fatalf("out=%q stderr=%q", out, stderr)
	}
	if sp.count() != 1 || p.size() != 0 {
		t.Fatalf("spawns=%d size=%d", sp.count(), p.size())
	}
}

// TestHelperOrphanHolder sleeps briefly, holding the stdout it inherited.
func TestHelperOrphanHolder(t *testing.T) {
	if os.Getenv("AGLINK_FAKE_ORPHAN") == "" {
		return
	}
	time.Sleep(3 * time.Second)
	os.Exit(0)
}

// A process the CLI spawned that outlives it while holding stdout must not keep
// the resident process "alive": retiring it (idle eviction, host shutdown) has
// to finish once persistentPipeDelay has passed.
func TestCLIProc_OrphanHoldingStdoutDoesNotBlockRetire(t *testing.T) {
	if os.Getenv("AGLINK_FAKE_PERSISTENT") != "" {
		return
	}
	old := persistentPipeDelay
	persistentPipeDelay = 200 * time.Millisecond
	defer func() { persistentPipeDelay = old }()

	p := newPersistentPool(startCLIProc, func() (bool, time.Duration, int) { return true, time.Minute, 2 })
	spec := helperSpec(t)
	out, _, err := p.runTurn(context.Background(), "k", spec, "orphan", nil)
	if err != nil || !strings.Contains(out, `"turn 1"`) {
		t.Fatalf("out=%q err=%v", out, err)
	}
	start := time.Now()
	p.Close() // helper exits on stdin EOF; its orphan still holds stdout for 3s
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("Close blocked %v on the orphan's stdout", el)
	}
}
