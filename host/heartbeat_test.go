package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHeartbeatVerdict(t *testing.T) {
	cases := []struct {
		in     string
		ok     bool
		wantIn string
	}{
		{"HEARTBEAT_OK", true, ""},
		{"  **HEARTBEAT_OK**\n", true, ""},
		{"모두 정상입니다.\nHEARTBEAT_OK", true, "모두 정상"},
		{"HEARTBEAT_OK — 별일 없어요", true, "별일 없어요"},
		{"", true, ""},
		{"서버 디스크가 95% 찼어요. 정리가 필요해요.", false, "디스크"},
		{"HEARTBEAT_OK\n" + strings.Repeat("긴 보고 ", 60), false, "긴 보고"},
	}
	for _, c := range cases {
		ok, msg := heartbeatVerdict(c.in)
		if ok != c.ok || !strings.Contains(msg, c.wantIn) {
			t.Errorf("heartbeatVerdict(%q) = %v, %q; want %v containing %q", c.in, ok, msg, c.ok, c.wantIn)
		}
		if strings.Contains(msg, heartbeatSentinel) {
			t.Errorf("heartbeatVerdict(%q) left the sentinel in %q", c.in, msg)
		}
	}
}

func TestReadHeartbeatChecklist(t *testing.T) {
	dir := t.TempDir()
	if got := readHeartbeatChecklist(filepath.Join(dir, "missing.md")); got != "" {
		t.Errorf("missing file = %q, want empty", got)
	}
	empty := filepath.Join(dir, "empty.md")
	_ = os.WriteFile(empty, []byte("# 점검 목록\n\n<!-- 여기에 확인할 것을 적으세요\n- 예시 -->\n\n## 매일\n"), 0o600)
	if got := readHeartbeatChecklist(empty); got != "" {
		t.Errorf("headings/comments only = %q, want empty", got)
	}
	full := filepath.Join(dir, "full.md")
	_ = os.WriteFile(full, []byte("# 점검\n- 받은 메일 중 급한 것\n- 오늘 일정\n"), 0o600)
	got := readHeartbeatChecklist(full)
	if !strings.Contains(got, "오늘 일정") || heartbeatItems(got) != 2 {
		t.Errorf("checklist = %q (%d items)", got, heartbeatItems(got))
	}
}

func TestInQuietHours(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 6, h, m, 0, 0, time.Local) }
	cases := []struct {
		spec string
		now  time.Time
		want bool
	}{
		{"23:00-07:00", at(23, 30), true},
		{"23:00-07:00", at(3, 0), true},
		{"23:00-07:00", at(7, 0), false},
		{"23:00-07:00", at(12, 0), false},
		{"12:00-13:00", at(12, 30), true},
		{"12:00-13:00", at(13, 30), false},
		{"", at(3, 0), false},
		{"엉터리", at(3, 0), false},
	}
	for _, c := range cases {
		if got := inQuietHours(c.spec, c.now); got != c.want {
			t.Errorf("inQuietHours(%q, %s) = %v, want %v", c.spec, c.now.Format("15:04"), got, c.want)
		}
	}
	if s := quietHoursSpec(&Config{}); s != defaultQuietHours {
		t.Errorf("unset quiet hours = %q, want default", s)
	}
	if s := quietHoursSpec(&Config{HeartbeatQuietHours: "off"}); s != "" {
		t.Errorf("off quiet hours = %q, want none", s)
	}
}

func TestParseHeartbeatEvery(t *testing.T) {
	if d, err := parseHeartbeatEvery(""); err != nil || d != defaultHeartbeatEvery {
		t.Errorf("default = %v, %v", d, err)
	}
	if d, err := parseHeartbeatEvery("2h"); err != nil || d != 2*time.Hour {
		t.Errorf("2h = %v, %v", d, err)
	}
	if _, err := parseHeartbeatEvery("2m"); err == nil {
		t.Error("2m should be rejected as too short")
	}
	for _, d := range []time.Duration{30 * time.Minute, 2 * time.Hour, 90 * time.Minute} {
		if got := everyOf(everyExpr(d)); got != d {
			t.Errorf("everyOf(everyExpr(%v)) = %v", d, got)
		}
	}
}

func TestSchedulerHeartbeatLifecycle(t *testing.T) {
	s := newTestScheduler(t)
	if s.HeartbeatTask() != nil {
		t.Fatal("no heartbeat task expected at start")
	}
	hb, err := s.EnableHeartbeat(42, 0)
	if err != nil {
		t.Fatal(err)
	}
	if hb.Kind != taskKindHeartbeat || hb.Status != "pending" || hb.CronExpr != "@every 30m" || hb.ChatID != 42 {
		t.Fatalf("task = %+v", hb)
	}
	if s.NextFire(hb.ID).IsZero() {
		t.Error("heartbeat should be registered with cron")
	}
	if err := s.DisableHeartbeat(); err != nil {
		t.Fatal(err)
	}
	if got := s.HeartbeatTask(); got.Status != "paused" || !s.NextFire(hb.ID).IsZero() {
		t.Errorf("after off: %+v", got)
	}
	if ok, err := s.SetHeartbeatEvery(time.Hour); !ok || err != nil {
		t.Fatalf("SetHeartbeatEvery = %v, %v", ok, err)
	}
	again, err := s.EnableHeartbeat(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != hb.ID || again.CronExpr != "@every 1h" || again.ChatID != 42 {
		t.Errorf("re-enable should keep the one task, its interval and chat: %+v", again)
	}
	if n := len(s.ListTasks("all")); n != 1 {
		t.Errorf("tasks = %d, want 1", n)
	}
}

// A heartbeat tick goes to the heartbeat runner, never to dispatch (its Prompt
// is only a label), and gets no missed-run notice after downtime.
func TestHeartbeatFireUsesHook(t *testing.T) {
	s := newTestScheduler(t)
	got := make(chan int64, 1)
	s.SetHeartbeat(func(chatID int64) { got <- chatID })
	s.SetDispatch(func(int64, string, string) { t.Error("heartbeat must not be dispatched as a job") })
	s.SetSend(func(int64, string) { t.Error("heartbeat must not send a reminder") })
	hb, err := s.EnableHeartbeat(7, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !s.fire(hb.ID) {
		t.Fatal("fire returned false")
	}
	select {
	case id := <-got:
		if id != 7 {
			t.Errorf("chatID = %d", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat hook not called")
	}
	old := &Task{CronExpr: "@every 30m", Kind: taskKindHeartbeat, CreatedAt: time.Now().Add(-5 * time.Hour)}
	if _, missed := missedRepeat(old, time.Now()); missed {
		t.Error("a heartbeat missed while off should not produce a notice")
	}
}

func heartbeatFixture(t *testing.T, fc *fakeClaude) *Manager {
	t.Helper()
	t.Setenv(dataDirEnv, t.TempDir())
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	return NewManager(fc, nil, st, NewConfigHolder(&Config{HomeDir: t.TempDir()}))
}

func TestRunHeartbeat_OKIsSilentAndSessionIsReused(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "HEARTBEAT_OK"}}
	m := heartbeatFixture(t, fc)
	s := &fakeSender{}

	m.RunHeartbeat(t.Context(), 1, "- 메일 확인", false, s)
	if len(s.sent) != 0 {
		t.Fatalf("all-clear sent %v, want nothing", s.sent)
	}
	first := fc.lastRun
	if first.Resume || first.SessionID == "" || !strings.Contains(first.Prompt, "- 메일 확인") || !strings.Contains(first.Prompt, heartbeatSentinel) {
		t.Fatalf("first run = %+v", first)
	}

	m.RunHeartbeat(t.Context(), 1, "- 메일 확인", false, s)
	if !fc.lastRun.Resume || fc.lastRun.SessionID != first.SessionID {
		t.Errorf("second run should resume the dedicated session: %+v", fc.lastRun)
	}
	if st := loadHeartbeatState(); st.LastResult != "ok" || st.SessionRuns != 2 {
		t.Errorf("state = %+v", st)
	}

	// Manual runs confirm an all-clear.
	m.RunHeartbeat(t.Context(), 1, "- 메일 확인", true, s)
	if len(s.sent) != 1 || !strings.Contains(s.sent[0], "알릴 것이 없어요") {
		t.Errorf("manual all-clear = %v", s.sent)
	}
}

func TestRunHeartbeat_AlertIsSent(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "급한 메일이 2통 왔어요."}}
	m := heartbeatFixture(t, fc)
	s := &fakeSender{}
	m.RunHeartbeat(t.Context(), 1, "- 메일 확인", false, s)
	if len(s.sent) != 1 || !strings.Contains(s.sent[0], "급한 메일") || !strings.HasPrefix(s.sent[0], "💓") {
		t.Fatalf("sent = %v", s.sent)
	}
	if st := loadHeartbeatState(); st.LastResult != "alert" {
		t.Errorf("state = %+v", st)
	}
}

func TestRunHeartbeat_FailureReportedOncePerStreak(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "인증 오류", IsError: true}}
	m := heartbeatFixture(t, fc)
	s := &fakeSender{}
	m.RunHeartbeat(t.Context(), 1, "- x", false, s)
	m.RunHeartbeat(t.Context(), 1, "- x", false, s)
	if len(s.sent) != 1 || !strings.Contains(s.sent[0], "정기 점검 실패") {
		t.Errorf("sent = %v, want one failure notice", s.sent)
	}
}

func heartbeatBot(t *testing.T, fc *fakeClaude, cfg *Config) (*Bot, *recCh) {
	t.Helper()
	m := heartbeatFixture(t, fc)
	cfg.HomeDir = m.cfg().HomeDir
	cfg.MaxWorkers = 2
	holder := NewConfigHolder(cfg)
	m.cfgh = holder
	b := NewBot(nil, holder, m.store, m, newTestScheduler(t), nil)
	rec := &recCh{}
	b.out.RegisterGlobal(rec)
	return b, rec
}

// An empty checklist must not cost a model call.
func TestStartHeartbeat_EmptyChecklistSpendsNothing(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "HEARTBEAT_OK"}}
	path := filepath.Join(t.TempDir(), "HEARTBEAT.md")
	_ = os.WriteFile(path, []byte("# 점검 목록\n"), 0o600)
	b, _ := heartbeatBot(t, fc, &Config{HeartbeatFile: path})
	if why := b.startHeartbeat(1, false); why != "empty" {
		t.Fatalf("startHeartbeat = %q, want empty", why)
	}
	if fc.runCalls != 0 {
		t.Errorf("model called %d times", fc.runCalls)
	}
}

func TestFireHeartbeat_QuietHoursSkip(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "HEARTBEAT_OK"}}
	path := filepath.Join(t.TempDir(), "HEARTBEAT.md")
	_ = os.WriteFile(path, []byte("- 메일 확인\n"), 0o600)
	now := time.Now()
	quiet := now.Add(-time.Minute).Format("15:04") + "-" + now.Add(2*time.Minute).Format("15:04")
	b, _ := heartbeatBot(t, fc, &Config{HeartbeatFile: path, HeartbeatQuietHours: quiet})
	b.fireHeartbeat(1)
	b.mu.Lock()
	queued := b.lanes[heartbeatLane] != nil
	b.mu.Unlock()
	if queued {
		t.Error("a tick in quiet hours must not queue a run")
	}
}

func TestHeartbeatCommand_RunDeliversAlert(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "오늘 3시 회의가 있어요."}}
	path := filepath.Join(t.TempDir(), "HEARTBEAT.md")
	_ = os.WriteFile(path, []byte("- 오늘 일정\n"), 0o600)
	b, rec := heartbeatBot(t, fc, &Config{HeartbeatFile: path})

	b.handleHeartbeat(b.ReplyTo(TelegramTarget()), 1, strings.Fields("!heartbeat run"))
	deadline := time.Now().Add(3 * time.Second)
	for {
		rec.mu.Lock()
		joined := strings.Join(rec.texts, "\n")
		rec.mu.Unlock()
		if strings.Contains(joined, "3시 회의") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("alert not delivered; sent: %s", joined)
		}
		time.Sleep(20 * time.Millisecond)
	}

	b.handleHeartbeat(b.ReplyTo(TelegramTarget()), 1, strings.Fields("!heartbeat on 1h"))
	hb := b.scheduler.HeartbeatTask()
	if hb == nil || hb.Status != "pending" || hb.CronExpr != "@every 1h" {
		t.Fatalf("after on: %+v", hb)
	}
	if st := b.heartbeatStatus(); !strings.Contains(st, "켜짐") || !strings.Contains(st, path) {
		t.Errorf("status = %q", st)
	}
}
