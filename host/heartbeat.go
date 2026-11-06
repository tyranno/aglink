package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Heartbeat: an opt-in periodic check-in. On each tick the worker goes through
// the user's checklist (<data dir>/HEARTBEAT.md) and answers HEARTBEAT_OK when
// nothing needs attention — then nothing is sent. Anything else reaches the
// user like a scheduled task result.
//
// Timing is an ordinary scheduler task (Kind taskKindHeartbeat, persisted in
// tasks.json), the run goes through the shared dispatch queue (MaxWorkers,
// watchdog, !cancel), and every run reuses one dedicated CLI session. An empty
// or missing checklist, or quiet hours, skip the run without a model call.

const (
	taskKindHeartbeat     = "heartbeat"
	heartbeatSentinel     = "HEARTBEAT_OK"
	heartbeatLane         = "heartbeat"
	defaultHeartbeatEvery = 30 * time.Minute
	minHeartbeatEvery     = 5 * time.Minute
	defaultQuietHours     = "23:00-07:00"
	maxHeartbeatChecklist = 4000
	// heartbeatOKSlack: chatter beside the sentinel ("모두 정상입니다.") still
	// counts as nothing to report.
	heartbeatOKSlack = 200
	// heartbeatSessionRuns: runs on one CLI session before starting a fresh one,
	// so the resumed context does not grow without bound.
	heartbeatSessionRuns = 24
)

// heartbeatFilePath is the checklist the worker goes through.
func heartbeatFilePath(cfg *Config) string {
	if cfg != nil && cfg.HeartbeatFile != "" {
		return expandHome(cfg.HeartbeatFile)
	}
	dir, err := dataDir()
	if err != nil {
		return "HEARTBEAT.md"
	}
	return filepath.Join(dir, "HEARTBEAT.md")
}

// expandHome resolves a leading "~/" against the user's home directory.
func expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return p
}

var htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

// readHeartbeatChecklist returns the checklist, or "" when the file is missing
// or holds nothing but headings, blank lines and comments.
func readHeartbeatChecklist(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := htmlCommentRe.ReplaceAllString(string(b), "")
	if heartbeatItems(s) == 0 {
		return ""
	}
	return truncate(strings.TrimSpace(s), maxHeartbeatChecklist)
}

// heartbeatItems counts the checklist's non-heading, non-blank lines.
func heartbeatItems(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		n++
	}
	return n
}

// quietHoursSpec is the effective quiet-hours range: the default when unset,
// "" when turned off.
func quietHoursSpec(cfg *Config) string {
	s := ""
	if cfg != nil {
		s = strings.TrimSpace(cfg.HeartbeatQuietHours)
	}
	switch strings.ToLower(s) {
	case "":
		return defaultQuietHours
	case "off", "none", "없음":
		return ""
	}
	return s
}

// parseQuietHours parses "23:00-07:00" into minutes since midnight.
func parseQuietHours(spec string) (from, to int, ok bool) {
	a, b, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, false
	}
	from, okA := parseClock(a)
	to, okB := parseClock(b)
	return from, to, okA && okB
}

func parseClock(s string) (int, bool) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// inQuietHours reports whether now falls in spec's range; a range may wrap
// midnight. An empty or malformed spec has no quiet hours.
func inQuietHours(spec string, now time.Time) bool {
	from, to, ok := parseQuietHours(spec)
	if !ok || from == to {
		return false
	}
	m := now.Hour()*60 + now.Minute()
	if from < to {
		return m >= from && m < to
	}
	return m >= from || m < to
}

// parseHeartbeatEvery parses an interval ("30m", "2h", "매시간"); "" = default.
func parseHeartbeatEvery(s string) (time.Duration, error) {
	if strings.TrimSpace(s) == "" {
		return defaultHeartbeatEvery, nil
	}
	d, _, err := ParseSchedule(s)
	if err != nil {
		return 0, err
	}
	if d < minHeartbeatEvery {
		return 0, fmt.Errorf("%s보다 짧게는 정할 수 없어요", humanDelay(minHeartbeatEvery))
	}
	return d, nil
}

// everyExpr is the cron spec for a fixed interval ("@every 30m").
func everyExpr(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("@every %dh", int(d/time.Hour))
	}
	return fmt.Sprintf("@every %dm", int(d/time.Minute))
}

// everyOf reads the interval back out of an "@every" spec (0 if not one).
func everyOf(expr string) time.Duration {
	rest, ok := strings.CutPrefix(expr, "@every ")
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(strings.TrimSpace(rest))
	if err != nil {
		return 0
	}
	return d
}

// heartbeatPrompt asks the worker to go through the checklist and use the
// sentinel when there is nothing to report.
func heartbeatPrompt(checklist string, now time.Time) string {
	return fmt.Sprintf(`[정기 점검] 사용자가 정해 둔 점검 목록을 지금 확인해 주세요. (지금: %s)
- 목록에 적힌 것만 확인하고, 요청받지 않은 작업은 시작하지 마세요.
- 사용자에게 알릴 것이 없으면 다른 말 없이 %s 한 줄만 답하세요.
- 알릴 것이 있으면 사용자에게 보낼 내용만 짧게 쓰고, 이때는 %s 를 쓰지 마세요.

점검 목록:
%s`, now.Format("2006-01-02 15:04 (Mon)"), heartbeatSentinel, heartbeatSentinel, checklist)
}

// heartbeatVerdict decides whether a reply is the all-clear. msg is the reply
// without the sentinel, to be sent when ok is false.
func heartbeatVerdict(text string) (ok bool, msg string) {
	found := false
	var rest []string
	for _, line := range strings.Split(text, "\n") {
		l := strings.Trim(strings.TrimSpace(line), "*`_")
		if after, has := strings.CutPrefix(l, heartbeatSentinel); has {
			found = true
			if after = strings.TrimSpace(strings.TrimLeft(after, ".!:-— ")); after != "" {
				rest = append(rest, after)
			}
			continue
		}
		rest = append(rest, line)
	}
	msg = strings.TrimSpace(strings.Join(rest, "\n"))
	if msg == "" {
		return true, ""
	}
	return found && len([]rune(msg)) <= heartbeatOKSlack, msg
}

// heartbeatState is the dedicated session and the last run's outcome
// (<data dir>/heartbeat.json).
type heartbeatState struct {
	Backend       string    `json:"backend,omitempty"`
	SessionID     string    `json:"sessionId,omitempty"`
	Started       bool      `json:"started,omitempty"`
	SessionRuns   int       `json:"sessionRuns,omitempty"`
	ContextTokens int       `json:"contextTokens,omitempty"`
	LastRun       time.Time `json:"lastRun,omitempty"`
	LastResult    string    `json:"lastResult,omitempty"` // ok | alert | error
	LastNote      string    `json:"lastNote,omitempty"`
}

func heartbeatStatePath() string {
	dir, err := dataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "heartbeat.json")
}

func loadHeartbeatState() heartbeatState {
	var st heartbeatState
	if p := heartbeatStatePath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &st)
		}
	}
	return st
}

func (st heartbeatState) save() {
	p := heartbeatStatePath()
	if p == "" {
		return
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		log.Printf("[heartbeat] save state: %v", err)
	}
}

// freshSession starts a new dedicated session for backend.
func (st *heartbeatState) freshSession(backend string) {
	st.Backend = backend
	st.SessionID = newUUID()
	st.Started = false
	st.SessionRuns = 0
	st.ContextTokens = 0
}

// RunHeartbeat runs one check-in over checklist and reports only what needs
// attention. manual (!heartbeat run) also confirms an all-clear, and reports
// every failure rather than only the first of a streak.
func (m *Manager) RunHeartbeat(ctx context.Context, chatID int64, checklist string, manual bool, s MessageSender) {
	backend := m.effectiveBackend("")
	client := m.clientForBackend(backend)
	if client == nil {
		log.Printf("[heartbeat] backend %s unavailable — skipped", backend)
		if manual {
			_ = s.Send(chatID, "⚠️ 백엔드를 사용할 수 없어 점검하지 못했어요.")
		}
		return
	}

	st := loadHeartbeatState()
	limit := claudeContextResetTokens
	if backend == "codex" {
		limit = codexContextResetTokens
	}
	if st.Backend != backend || st.SessionID == "" || st.SessionRuns >= heartbeatSessionRuns || st.ContextTokens >= limit {
		st.freshSession(backend)
	}
	req := RunRequest{
		Prompt:    heartbeatPrompt(checklist, time.Now()),
		WorkDir:   m.validWorkDirOrHome(resolveHomeDir(m.cfg())),
		SessionID: st.SessionID,
		Resume:    st.Started,
		Model:     m.workerModelForBackendName(backend),
	}
	start := time.Now()
	res, err := client.Run(ctx, req)
	if req.Resume && ctx.Err() == nil && (err != nil && isSessionNotFound(err.Error()) || err == nil && res.IsError && isSessionNotFound(res.Text)) {
		// The session is gone (restart, CLI update): once more on a fresh one.
		st.freshSession(backend)
		req.SessionID, req.Resume = st.SessionID, false
		res, err = client.Run(ctx, req)
	}
	if ctx.Err() != nil {
		return // Bot.runTurn reports a cancel or timeout
	}
	st.LastRun = time.Now()
	if err != nil || res.IsError {
		reason := res.Text
		if err != nil {
			reason = err.Error()
		}
		streak := st.LastResult == "error"
		st.LastResult, st.LastNote = "error", truncate(strings.TrimSpace(reason), 120)
		st.save()
		log.Printf("[heartbeat] run failed after %s: %s", time.Since(start).Round(time.Second), truncate(reason, 200))
		if manual || !streak {
			_ = s.Send(chatID, "⚠️ 정기 점검 실패: "+truncate(strings.TrimSpace(reason), 300))
		}
		return
	}
	if res.SessionID != "" && !req.Resume {
		st.SessionID = res.SessionID
	}
	st.Started = true
	st.SessionRuns++
	st.ContextTokens = res.ContextTokens
	if backend == "codex" {
		st.ContextTokens = res.InputTokens
	}

	ok, msg := heartbeatVerdict(res.Text)
	if ok {
		st.LastResult, st.LastNote = "ok", ""
		st.save()
		log.Printf("[heartbeat] all clear (%s)", time.Since(start).Round(time.Second))
		if manual {
			_ = s.Send(chatID, "💓 점검 완료 — 지금은 알릴 것이 없어요.")
		}
		return
	}
	st.LastResult, st.LastNote = "alert", truncate(msg, 120)
	st.save()
	log.Printf("[heartbeat] reporting %d chars (%s)", len(msg), time.Since(start).Round(time.Second))
	if herr := WriteHistory(heartbeatLane, "💓 정기 점검", "정기 점검", msg); herr != nil {
		log.Printf("[heartbeat] history write: %v", herr)
	}
	_ = sendChunked(s, chatID, "💓 정기 점검\n\n"+msg)
}

// --- scheduler side ---

// heartbeatLocked finds the live (non-cancelled) heartbeat task. Lock held.
func (s *Scheduler) heartbeatLocked() *Task {
	for _, t := range s.tasks {
		if t.Kind == taskKindHeartbeat && t.Status != "cancelled" {
			return t
		}
	}
	return nil
}

// HeartbeatTask returns a copy of the heartbeat task, nil when there is none.
func (s *Scheduler) HeartbeatTask() *Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.heartbeatLocked(); t != nil {
		cp := *t
		return &cp
	}
	return nil
}

// EnableHeartbeat turns the check-in on, creating its task if needed. every
// <= 0 keeps the current interval (or the default for a new task); chatID 0
// keeps the current recipient.
func (s *Scheduler) EnableHeartbeat(chatID int64, every time.Duration) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.heartbeatLocked()
	if t == nil {
		if every <= 0 {
			every = defaultHeartbeatEvery
		}
		t = &Task{
			ID:        newTaskID(),
			ChatID:    chatID,
			Prompt:    "정기 점검 (HEARTBEAT.md)",
			CronExpr:  everyExpr(every),
			Status:    "pending",
			IsTask:    true,
			Label:     "💓 정기 점검",
			Kind:      taskKindHeartbeat,
			CreatedAt: time.Now(),
		}
		if err := s.register(t); err != nil {
			return nil, err
		}
		s.tasks = append(s.tasks, t)
	} else {
		if t.Status == "pending" {
			s.deregister(t.ID)
		}
		if every > 0 {
			t.CronExpr = everyExpr(every)
		}
		if chatID != 0 {
			t.ChatID = chatID
		}
		t.Status = "pending"
		if err := s.register(t); err != nil {
			t.Status = "paused"
			_ = s.save()
			return nil, err
		}
	}
	cp := *t
	return &cp, s.save()
}

// DisableHeartbeat pauses the check-in; a no-op when there is none.
func (s *Scheduler) DisableHeartbeat() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.heartbeatLocked()
	if t == nil {
		return nil
	}
	s.deregister(t.ID)
	t.Status = "paused"
	return s.save()
}

// SetHeartbeatEvery changes the interval, on or off. ok is false when there is
// no heartbeat task yet.
func (s *Scheduler) SetHeartbeatEvery(every time.Duration) (ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.heartbeatLocked()
	if t == nil {
		return false, nil
	}
	if t.Status != "pending" {
		t.CronExpr = everyExpr(every)
		return true, s.save()
	}
	s.deregister(t.ID)
	old := t.CronExpr
	t.CronExpr = everyExpr(every)
	if err := s.register(t); err != nil {
		t.CronExpr = old
		_ = s.register(t)
		return true, err
	}
	return true, s.save()
}

// --- bot side ---

// fireHeartbeat is the scheduler tick: outside quiet hours, queue a run.
func (b *Bot) fireHeartbeat(chatID int64) {
	if spec := quietHoursSpec(b.cfg()); inQuietHours(spec, time.Now()) {
		log.Printf("[heartbeat] quiet hours (%s) — skipped", spec)
		return
	}
	if why := b.startHeartbeat(chatID, false); why != "" {
		log.Printf("[heartbeat] skipped: %s", why)
	}
}

// startHeartbeat queues one run. Returns why it did not: "empty" (no
// checklist — no model call), "busy" (a run is still going), or "" (queued).
func (b *Bot) startHeartbeat(chatID int64, manual bool) string {
	checklist := readHeartbeatChecklist(heartbeatFilePath(b.cfg()))
	if checklist == "" {
		return "empty"
	}
	b.mu.Lock()
	busy := b.lanes[heartbeatLane] != nil
	b.mu.Unlock()
	if busy {
		return "busy"
	}
	b.dispatch(queuedMsg{
		chatID:    chatID,
		text:      checklist,
		isTask:    true,
		heartbeat: true,
		hbManual:  manual,
		origin:    OriginTelegram,
		laneKey:   heartbeatLane,
		noMerge:   true,
	})
	return ""
}

// ensureHeartbeatFromConfig creates the heartbeat task on startup when the
// config turns it on and there is none yet. Later on/off/interval changes made
// with !heartbeat live in tasks.json and are not overridden here.
func ensureHeartbeatFromConfig(cfg *Config, sched *Scheduler) {
	if cfg == nil || sched == nil || !cfg.HeartbeatEnabled || sched.HeartbeatTask() != nil || len(cfg.AllowedUserIDs) == 0 {
		return
	}
	every, err := parseHeartbeatEvery(cfg.HeartbeatEvery)
	if err != nil {
		log.Printf("[heartbeat] heartbeat.every %q: %v — using %s", cfg.HeartbeatEvery, err, defaultHeartbeatEvery)
		every = defaultHeartbeatEvery
	}
	if _, err := sched.EnableHeartbeat(cfg.AllowedUserIDs[0], every); err != nil {
		log.Printf("[heartbeat] enable from config: %v", err)
		return
	}
	log.Printf("[heartbeat] enabled from config, every %s", every)
}

func heartbeatHelpText() string {
	return strings.TrimSpace(`
사용법: !heartbeat [status] | on [주기] | off | interval <주기> | run
  on [주기]        정기 점검 켜기 (기본 30분마다)
  off              끄기
  interval <주기>  주기 바꾸기 — 예: 30m, 1h, 2h
  run              지금 한 번 점검
점검 목록 파일에 확인할 것을 적어 두면, 알릴 것이 있을 때만 말씀드려요.`)
}

// handleHeartbeat processes !heartbeat.
//
//	!heartbeat [status]          상태
//	!heartbeat on [주기]          켜기
//	!heartbeat off               끄기
//	!heartbeat interval <주기>    주기 변경
//	!heartbeat run               지금 한 번
func (b *Bot) handleHeartbeat(reply replySender, chatID int64, fields []string) {
	if b.scheduler == nil {
		_ = reply.Send(chatID, "⚠️ 스케줄러가 초기화되지 않았습니다.")
		return
	}
	sub := "status"
	if len(fields) > 1 {
		sub = strings.ToLower(fields[1])
	}
	path := heartbeatFilePath(b.cfg())
	emptyNote := fmt.Sprintf("\n점검 목록이 아직 비어 있어요. %s 에 확인할 것을 적어 두면 그때부터 점검해요 (비어 있는 동안은 모델을 부르지 않아요).", path)
	switch sub {
	case "status":
		_ = reply.Send(chatID, b.heartbeatStatus())
	case "on":
		every := time.Duration(0)
		if len(fields) > 2 {
			d, err := parseHeartbeatEvery(fields[2])
			if err != nil {
				_ = reply.Send(chatID, "⚠️ 주기 형식 오류: "+err.Error())
				return
			}
			every = d
		} else if b.scheduler.HeartbeatTask() == nil {
			if d, err := parseHeartbeatEvery(b.cfg().HeartbeatEvery); err == nil {
				every = d
			}
		}
		t, err := b.scheduler.EnableHeartbeat(chatID, every)
		if err != nil {
			_ = reply.Send(chatID, "⚠️ 정기 점검을 켜지 못했어요: "+err.Error())
			return
		}
		msg := fmt.Sprintf("💓 정기 점검을 켰어요 — %s 점검 목록을 확인하고, 알릴 것이 있을 때만 말씀드려요.", everyLabel(everyOf(t.CronExpr)))
		if readHeartbeatChecklist(path) == "" {
			msg += emptyNote
		}
		_ = reply.Send(chatID, msg)
	case "off":
		if err := b.scheduler.DisableHeartbeat(); err != nil {
			_ = reply.Send(chatID, "⚠️ 정기 점검을 끄지 못했어요: "+err.Error())
			return
		}
		_ = reply.Send(chatID, "💓 정기 점검을 껐어요.")
	case "interval", "every":
		if len(fields) < 3 {
			_ = reply.Send(chatID, "사용법: !heartbeat interval <주기> — 예: 30m, 1h")
			return
		}
		d, err := parseHeartbeatEvery(fields[2])
		if err != nil {
			_ = reply.Send(chatID, "⚠️ 주기 형식 오류: "+err.Error())
			return
		}
		ok, err := b.scheduler.SetHeartbeatEvery(d)
		switch {
		case err != nil:
			_ = reply.Send(chatID, "⚠️ 주기를 바꾸지 못했어요: "+err.Error())
		case !ok:
			_ = reply.Send(chatID, "정기 점검이 아직 없어요. !heartbeat on "+fields[2]+" 으로 켜 주세요.")
		default:
			_ = reply.Send(chatID, fmt.Sprintf("💓 이제 %s 점검해요.", everyLabel(d)))
		}
	case "run", "now":
		switch b.startHeartbeat(chatID, true) {
		case "empty":
			_ = reply.Send(chatID, "점검 목록이 비어 있어서 할 게 없어요."+emptyNote)
		case "busy":
			_ = reply.Send(chatID, "💓 이미 점검 중이에요.")
		default:
			_ = reply.Send(chatID, "💓 지금 점검할게요.")
		}
	default:
		_ = reply.Send(chatID, heartbeatHelpText())
	}
}

// everyLabel renders an interval as "30분마다" / "2시간마다".
func everyLabel(d time.Duration) string {
	switch {
	case d <= 0:
		return "정해진 주기로"
	case d%time.Hour == 0:
		return fmt.Sprintf("%d시간마다", int(d/time.Hour))
	default:
		return fmt.Sprintf("%d분마다", int(d/time.Minute))
	}
}

// heartbeatStatus renders !heartbeat status.
func (b *Bot) heartbeatStatus() string {
	cfg := b.cfg()
	var sb strings.Builder
	t := b.scheduler.HeartbeatTask()
	switch {
	case t == nil || t.Status != "pending":
		sb.WriteString("💓 정기 점검: 꺼짐")
		if t != nil {
			fmt.Fprintf(&sb, " (켜면 %s)", everyLabel(everyOf(t.CronExpr)))
		}
	default:
		fmt.Fprintf(&sb, "💓 정기 점검: 켜짐 — %s", everyLabel(everyOf(t.CronExpr)))
		if next := b.scheduler.NextFire(t.ID); !next.IsZero() {
			fmt.Fprintf(&sb, " (다음: %s)", dueLabel(next, time.Now()))
		}
	}
	spec := quietHoursSpec(cfg)
	if _, _, ok := parseQuietHours(spec); ok {
		fmt.Fprintf(&sb, "\n조용한 시간: %s (이때는 건너뛰어요)", spec)
	} else if spec != "" {
		fmt.Fprintf(&sb, "\n조용한 시간: %q 형식을 알 수 없어 적용하지 않아요 (예: 23:00-07:00)", spec)
	} else {
		sb.WriteString("\n조용한 시간: 없음")
	}
	path := heartbeatFilePath(cfg)
	if c := readHeartbeatChecklist(path); c == "" {
		fmt.Fprintf(&sb, "\n점검 목록: %s — 비어 있음 (모델을 부르지 않아요)", path)
	} else {
		fmt.Fprintf(&sb, "\n점검 목록: %s — %d줄", path, heartbeatItems(c))
	}
	if st := loadHeartbeatState(); !st.LastRun.IsZero() {
		fmt.Fprintf(&sb, "\n마지막 점검: %s — ", dueLabel(st.LastRun, time.Now()))
		switch st.LastResult {
		case "ok":
			sb.WriteString("알릴 것 없음")
		case "alert":
			sb.WriteString("알림 보냄: " + st.LastNote)
		default:
			sb.WriteString("실패: " + st.LastNote)
		}
	}
	sb.WriteString("\n\n" + heartbeatHelpText())
	return sb.String()
}
