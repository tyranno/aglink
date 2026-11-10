package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// Scheduler manages Tasks (one-shot and recurring), persisting to tasks.json.
// Replaces the old Reminder + CronJob design.
type Scheduler struct {
	mu          sync.Mutex
	tasksPath   string
	tasks       []*Task
	cronRunner  *cron.Cron
	cronEntries map[string]cron.EntryID  // Task.ID → cron EntryID
	stopChs     map[string]chan struct{} // Task.ID → cancel channel (one-shot)
	done        chan struct{}            // closed by Stop() to unblock Run()
	send        func(chatID int64, text string)
	dispatch    func(chatID int64, text, project string)
	// For tasks with a web Target: a reminder posted into that conversation,
	// and a job sent as a turn of it.
	webSend     func(chatID int64, tgt Target, text string)
	webDispatch func(chatID int64, tgt Target, text string)
	runPlaybook func(chatID int64, playbookID string) error // tasks with a PlaybookID
}

// lateAfter: a one-shot reminder delivered later than this (the host was off,
// or asleep) says when it was meant for; a repeat whose run fell further back
// than this while the host was off gets one catch-up notice.
const lateAfter = 2 * time.Minute

func NewScheduler(tasksPath string) *Scheduler {
	c := cron.New(cron.WithLocation(time.Local))
	return &Scheduler{
		tasksPath:   tasksPath,
		cronRunner:  c,
		cronEntries: make(map[string]cron.EntryID),
		done:        make(chan struct{}),
		stopChs:     make(map[string]chan struct{}),
	}
}

func (s *Scheduler) SetSend(f func(int64, string)) {
	s.mu.Lock()
	s.send = f
	s.mu.Unlock()
}

func (s *Scheduler) SetDispatch(f func(int64, string, string)) {
	s.mu.Lock()
	s.dispatch = f
	s.mu.Unlock()
}

// SetWebDelivery wires how tasks addressed to a web conversation report.
func (s *Scheduler) SetWebDelivery(send, dispatch func(int64, Target, string)) {
	s.mu.Lock()
	s.webSend, s.webDispatch = send, dispatch
	s.mu.Unlock()
}

// SetPlaybookRun wires how a task that names an 업무 루틴 runs it. An error is
// reported to the task's target as a 정기 실행 실패 message.
func (s *Scheduler) SetPlaybookRun(f func(int64, string) error) {
	s.mu.Lock()
	s.runPlaybook = f
	s.mu.Unlock()
}

// Load reads tasks.json; migrates schedule.json if tasks.json is absent.
func (s *Scheduler) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := os.ReadFile(s.tasksPath)
	if os.IsNotExist(err) {
		schedPath := filepath.Join(filepath.Dir(s.tasksPath), "schedule.json")
		if _, serr := os.Stat(schedPath); serr == nil {
			return s.migrateLegacy(schedPath)
		}
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &s.tasks)
}

// save writes tasks atomically. Lock must be held by caller.
func (s *Scheduler) save() error {
	b, err := json.MarshalIndent(s.tasks, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.tasksPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.tasksPath)
}

// Run registers all pending tasks and starts the cron runner. Call in a goroutine.
// Returns when Stop() is called.
func (s *Scheduler) Run() {
	s.mu.Lock()
	var missed []missedRun
	now := time.Now()
	for _, t := range s.tasks {
		if t.Status == "pending" {
			if m, ok := missedRepeat(t, now); ok {
				missed = append(missed, m)
				// The notice stands in for the missed runs, so a second restart
				// does not repeat it.
				t.LastFired = now
			}
			if err := s.register(t); err != nil {
				log.Printf("[scheduler] startup register error for task %s: %v", t.ID, err)
			}
		}
	}
	if len(missed) > 0 {
		_ = s.save()
	}
	s.mu.Unlock()
	for _, m := range missed {
		log.Printf("[scheduler] task %s: run due %s was missed while the host was off — notice sent", m.taskID, m.due.Format(time.RFC3339))
		s.deliver(m.chatID, m.web, m.text)
	}
	s.cronRunner.Start()
	<-s.done
	s.cronRunner.Stop()
}

// Stop gracefully shuts down the scheduler.
func (s *Scheduler) Stop() {
	select {
	case <-s.done: // already closed
	default:
		close(s.done)
	}
}

// register adds a task to cronRunner (recurring) or starts a timer (one-shot).
// Returns an error if the cron expression is invalid.
// Lock must be held by caller.
func (s *Scheduler) register(t *Task) error {
	if t.CronExpr != "" {
		entryID, err := s.cronRunner.AddFunc(t.CronExpr, func() { s.fire(t.ID) })
		if err != nil {
			return fmt.Errorf("cron 식 오류 (%q): %w", t.CronExpr, err)
		}
		s.cronEntries[t.ID] = entryID
		return nil
	}
	if !t.FireAt.IsZero() {
		stopCh := make(chan struct{})
		s.stopChs[t.ID] = stopCh
		taskID := t.ID
		delay := time.Until(t.FireAt)
		if delay < 0 {
			delay = 0
		}
		go func() {
			select {
			case <-time.After(delay):
				// fired=false means deps were not met and the task was rescheduled;
				// the task must NOT be removed from the list in that case.
				if s.fire(taskID) {
					s.mu.Lock()
					s.removeByID(taskID)
					_ = s.save()
					s.mu.Unlock()
				}
			case <-stopCh:
			}
		}()
	}
	return nil
}

// deregister removes a task from cronRunner or cancels its timer.
// Lock must be held by caller.
func (s *Scheduler) deregister(id string) {
	if entryID, ok := s.cronEntries[id]; ok {
		s.cronRunner.Remove(entryID)
		delete(s.cronEntries, id)
	}
	if ch, ok := s.stopChs[id]; ok {
		close(ch)
		delete(s.stopChs, id)
	}
}

// depsMetLocked reports whether all DependsOn tasks have completed.
// A dependency is "done" when it is absent from the task list (removed after
// one-shot fire) or has Status "cancelled". Lock must be held by caller.
func (s *Scheduler) depsMetLocked(t *Task) bool {
	for _, depID := range t.DependsOn {
		dep := s.findByID(depID)
		if dep != nil && (dep.Status == "pending" || dep.Status == "paused") {
			return false
		}
	}
	return true
}

// fire executes a task's action (called by cron tick or one-shot goroutine).
// Returns true if the task was actually dispatched; false if deferred (deps not met).
// One-shot goroutines must check the return value before removing the task.
func (s *Scheduler) fire(taskID string) bool {
	s.mu.Lock()
	t := s.findByID(taskID)
	if t == nil || t.Status != "pending" {
		s.mu.Unlock()
		return false
	}
	// DAG: skip if any dependency is still active.
	if len(t.DependsOn) > 0 && !s.depsMetLocked(t) {
		log.Printf("[scheduler] task %s: dependencies not met — deferring 1m", taskID)
		if t.CronExpr == "" && !t.FireAt.IsZero() {
			// One-shot: reschedule 1 minute later.
			s.deregister(taskID)
			t.FireAt = time.Now().Add(time.Minute)
			_ = s.register(t)
			_ = s.save()
		}
		s.mu.Unlock()
		return false
	}
	t.LastFired = time.Now()
	chatID, project, prompt, script, isTask := t.ChatID, t.Project, t.Prompt, t.Script, t.IsTask
	var web *Target
	if t.Target != nil && t.Target.IsWeb() && t.Target.ID != "" {
		tgt := *t.Target
		web = &tgt
	}
	playbookID := t.PlaybookID
	label := t.Label
	// A one-shot that comes late (the host was off when it was due) says so,
	// after the 🔔 the desktop recognises a reminder by.
	latePrefix := ""
	if t.CronExpr == "" && !t.FireAt.IsZero() && t.LastFired.Sub(t.FireAt) > lateAfter {
		latePrefix = "(" + dueLabel(t.FireAt, t.LastFired) + " 예정이던 알림) "
	}
	_ = s.save()
	sendFn, dispatchFn := s.send, s.dispatch
	webSendFn, webDispatchFn, playbookFn := s.webSend, s.webDispatch, s.runPlaybook
	s.mu.Unlock()

	// Script pre-check: skip turn if wakeAgent == false
	if script != "" {
		wake, data := runScriptPrecheck(script)
		if !wake {
			log.Printf("[scheduler] task %s: wakeAgent=false — skipping this turn", taskID)
			return true // consumed the turn; cron will fire again on next tick
		}
		if len(data) > 0 {
			if msg, ok := data["message"].(string); ok && msg != "" {
				// Pre-formatted human-readable text (e.g. from a script's own summarization)
				// reads far better in a 🔔-style literal notification than a raw JSON dump.
				prompt = prompt + "\n\n" + msg
			} else {
				b, _ := json.Marshal(data)
				prompt = prompt + "\n\n[Script data]: " + string(b)
			}
		}
	}

	if playbookID != "" && playbookFn != nil {
		go func() {
			if err := playbookFn(chatID, playbookID); err != nil {
				log.Printf("[scheduler] task %s: scheduled playbook %s: %v", taskID, playbookID, err)
				name := strings.TrimSpace(strings.TrimPrefix(label, "▶"))
				if name == "" {
					name = playbookID
				}
				s.deliver(chatID, web, fmt.Sprintf("⚠️ 정기 실행 실패: %s — %v", name, err))
			}
		}()
	} else if web != nil && isTask && webDispatchFn != nil {
		go webDispatchFn(chatID, *web, prompt)
	} else if web != nil && !isTask && webSendFn != nil {
		go webSendFn(chatID, *web, "🔔 "+latePrefix+prompt)
	} else if isTask && dispatchFn != nil {
		go dispatchFn(chatID, prompt, project)
	} else if sendFn != nil {
		go sendFn(chatID, "🔔 "+latePrefix+prompt)
	} else {
		// No channel wired (e.g. the bot is not up yet): say so instead of
		// dropping the run silently.
		log.Printf("[scheduler] task %s fired but there is no channel to run or send it on — dropped", taskID)
	}
	return true
}

// deliver posts a plain message (no AI turn) where a task reports: its web
// conversation when it has one, else the telegram stream.
func (s *Scheduler) deliver(chatID int64, web *Target, text string) {
	s.mu.Lock()
	sendFn, webSendFn := s.send, s.webSend
	s.mu.Unlock()
	switch {
	case web != nil && webSendFn != nil:
		webSendFn(chatID, *web, text)
	case sendFn != nil:
		sendFn(chatID, text)
	default:
		log.Printf("[scheduler] no channel to deliver %q — dropped", truncate(text, 60))
	}
}

// dueLabel renders when a late task was due: "15:00", or "10/30 15:00" when
// that was not on the day it is delivered.
func dueLabel(due, now time.Time) string {
	due = due.In(time.Local)
	now = now.In(time.Local)
	if y, m, d := due.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return due.Format("15:04")
	}
	return due.Format("1/2 15:04")
}

// missedRun is a repeat whose time came and went while the host was off.
type missedRun struct {
	taskID string
	chatID int64
	web    *Target
	due    time.Time
	text   string
}

// missedRepeat reports whether a recurring task had a run fall due (by more
// than lateAfter) between its last run — or its creation — and now, i.e.
// while the host was not running. Missed runs are not replayed; the caller
// sends a single notice instead.
func missedRepeat(t *Task, now time.Time) (missedRun, bool) {
	if t.CronExpr == "" {
		return missedRun{}, false
	}
	from := t.LastFired
	if from.IsZero() {
		from = t.CreatedAt
	}
	if from.IsZero() {
		return missedRun{}, false
	}
	sched, err := cron.ParseStandard(t.CronExpr)
	if err != nil {
		return missedRun{}, false
	}
	due := sched.Next(from)
	if due.IsZero() || now.Sub(due) <= lateAfter {
		return missedRun{}, false
	}
	// Name the latest run that was missed, not the first (bounded, for an
	// every-minute repeat after a long time off).
	for i := 0; i < 100000; i++ {
		next := sched.Next(due)
		if next.IsZero() || now.Sub(next) <= lateAfter {
			break
		}
		due = next
	}
	m := missedRun{taskID: t.ID, chatID: t.ChatID, due: due}
	if t.Target != nil && t.Target.IsWeb() && t.Target.ID != "" {
		tgt := *t.Target
		m.web = &tgt
	}
	when := dueLabel(due, now)
	switch {
	case !t.IsTask && t.PlaybookID == "":
		m.text = "🔔 (" + when + " 예정이던 알림 — 꺼져 있는 동안 놓쳤어요) " + t.Prompt
	default:
		name := strings.TrimSpace(strings.TrimPrefix(t.Label, "▶"))
		if name == "" {
			name = truncate(t.Prompt, 40)
		}
		m.text = fmt.Sprintf("⚠️ 꺼져 있는 동안 정기 실행을 놓쳤어요: %s (%s 예정) — 다음 예정 시각에 다시 실행해요.", name, when)
	}
	return m, true
}

// DeleteTasksForPlaybook removes every task that runs the given 업무 루틴, so
// deleting the routine does not leave a schedule that can only fail. Returns
// how many were removed.
func (s *Scheduler) DeleteTasksForPlaybook(playbookID string) int {
	if playbookID == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, t := range s.tasks {
		if t.PlaybookID == playbookID {
			ids = append(ids, t.ID)
		}
	}
	for _, id := range ids {
		s.deregister(id)
		s.removeByID(id)
	}
	if len(ids) > 0 {
		_ = s.save()
	}
	return len(ids)
}

// AddTask adds a new Task and registers it with the scheduler.
// Returns an error (and does not persist) if cron expression parsing fails.
func (s *Scheduler) AddTask(t *Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.Status == "pending" {
		if err := s.register(t); err != nil {
			return err
		}
	}
	s.tasks = append(s.tasks, t)
	return s.save()
}

// PauseTask pauses a task (deregisters, sets status="paused").
func (s *Scheduler) PauseTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.findByID(id)
	if t == nil {
		return fmt.Errorf("작업 %q 없음", id)
	}
	s.deregister(id)
	t.Status = "paused"
	return s.save()
}

// ResumeTask re-activates a paused task.
// Idempotent: returns nil if the task is already pending (prevents ghost cron entries
// from leaking when register() would overwrite cronEntries without removing the old entry).
func (s *Scheduler) ResumeTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.findByID(id)
	if t == nil {
		return fmt.Errorf("작업 %q 없음", id)
	}
	if t.Status == "pending" {
		return nil // already active — idempotent
	}
	t.Status = "pending"
	if err := s.register(t); err != nil {
		t.Status = "paused"
		return err
	}
	return s.save()
}

// CancelTask permanently cancels a task (deregisters, sets status="cancelled").
func (s *Scheduler) CancelTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.findByID(id)
	if t == nil {
		return fmt.Errorf("작업 %q 없음", id)
	}
	s.deregister(id)
	t.Status = "cancelled"
	return s.save()
}

// UpdateTask updates mutable fields on an active or paused task.
// Pass empty string / nil to leave a field unchanged.
func (s *Scheduler) UpdateTask(id, cronExpr, prompt, script string, dependsOn []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.findByID(id)
	if t == nil {
		return fmt.Errorf("작업 %q 없음", id)
	}
	wasActive := t.Status == "pending"
	// Snapshot old values for rollback on register failure.
	oldCronExpr, oldPrompt, oldScript, oldDeps := t.CronExpr, t.Prompt, t.Script, t.DependsOn
	if wasActive {
		s.deregister(id)
	}
	if cronExpr != "" {
		t.CronExpr = cronExpr
	}
	if prompt != "" {
		t.Prompt = prompt
	}
	switch script {
	case "":
		// no change
	case "\x00":
		t.Script = "" // clear sentinel
	default:
		t.Script = script
	}
	if dependsOn != nil {
		t.DependsOn = dependsOn
	}
	if wasActive {
		if err := s.register(t); err != nil {
			t.CronExpr, t.Prompt, t.Script, t.DependsOn = oldCronExpr, oldPrompt, oldScript, oldDeps
			t.Status = "paused"
			_ = s.save()
			return err
		}
	}
	return s.save()
}

// UpsertTask creates a new task (in.ID == "") or replaces the editable fields of
// an existing one, re-registering it with the cron runner if it was active.
// Status/CreatedAt/LastFired of an existing task are preserved; a new task
// always starts "pending". Returns the saved task (with its assigned ID).
func (s *Scheduler) UpsertTask(in *Task) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if in.ID == "" {
		t := &Task{
			ID:         newTaskID(),
			ChatID:     in.ChatID,
			Project:    in.Project,
			Prompt:     in.Prompt,
			Script:     in.Script,
			CronExpr:   in.CronExpr,
			FireAt:     in.FireAt,
			Status:     "pending",
			IsTask:     in.IsTask,
			Label:      in.Label,
			DependsOn:  in.DependsOn,
			Target:     in.Target,
			PlaybookID: in.PlaybookID,
			CreatedAt:  time.Now(),
		}
		if err := s.register(t); err != nil {
			return nil, err
		}
		s.tasks = append(s.tasks, t)
		if err := s.save(); err != nil {
			return nil, err
		}
		return t, nil
	}

	t := s.findByID(in.ID)
	if t == nil {
		return nil, fmt.Errorf("작업 %q 없음", in.ID)
	}
	wasActive := t.Status == "pending"
	old := *t
	if wasActive {
		s.deregister(in.ID)
	}
	// ChatID/Project/DependsOn fall back to the existing value when the caller
	// doesn't specify one — editors (desktop/web) that don't expose these fields
	// must not silently wipe them on an otherwise-unrelated edit.
	if in.ChatID != 0 {
		t.ChatID = in.ChatID
	}
	if in.Project != "" {
		t.Project = in.Project
	}
	if in.DependsOn != nil {
		t.DependsOn = in.DependsOn
	}
	if in.Target != nil {
		t.Target = in.Target
	}
	t.PlaybookID = in.PlaybookID
	t.Prompt = in.Prompt
	t.Script = in.Script
	t.CronExpr = in.CronExpr
	t.FireAt = in.FireAt
	t.IsTask = in.IsTask
	t.Label = in.Label
	if wasActive {
		if err := s.register(t); err != nil {
			*t = old
			_ = s.register(t)
			return nil, err
		}
	}
	if err := s.save(); err != nil {
		return nil, err
	}
	return t, nil
}

// DeleteTask permanently removes a task (any status) from tasks.json, unlike
// CancelTask which keeps a "cancelled" record around for history.
func (s *Scheduler) DeleteTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findByID(id) == nil {
		return fmt.Errorf("작업 %q 없음", id)
	}
	s.removeByID(id)
	return s.save()
}

// ListTasks returns tasks matching status filter ("pending"|"paused"|"cancelled"|"all"|"").
// "" and "all" return everything.
func (s *Scheduler) ListTasks(filter string) []*Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Task
	for _, t := range s.tasks {
		if filter == "" || filter == "all" || t.Status == filter {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out
}

// NextFire returns the next scheduled fire time for a recurring task.
// Returns zero time if not found or not active.
func (s *Scheduler) NextFire(id string) time.Time {
	s.mu.Lock()
	entryID, ok := s.cronEntries[id]
	s.mu.Unlock()
	if !ok {
		return time.Time{}
	}
	entry := s.cronRunner.Entry(entryID)
	return entry.Next
}

// Remove cancels a task by ID. Returns false if not found.
// Kept for backward compatibility with !remind / !cron commands.
func (s *Scheduler) Remove(id string) bool {
	err := s.CancelTask(id)
	return err == nil
}

// AddReminder creates a one-shot notification Task, pinned to project (the
// caller's active project at creation time — see Task.Project).
// Kept for backward compatibility with !remind.
func (s *Scheduler) AddReminder(chatID int64, project, msg string, fireAt time.Time) (*Task, error) {
	t := &Task{
		ID:        newTaskID(),
		ChatID:    chatID,
		Project:   project,
		Prompt:    msg,
		FireAt:    fireAt,
		Status:    "pending",
		IsTask:    false,
		Label:     "알림: " + msg,
		CreatedAt: time.Now(),
	}
	return t, s.AddTask(t)
}

// AddCron creates a recurring Task from a duration, pinned to project (the
// caller's active project at creation time — see Task.Project).
// Kept for backward compatibility with !cron.
func (s *Scheduler) AddCron(chatID int64, project, label string, interval time.Duration, task string, isTask bool) (*Task, error) {
	t := &Task{
		ID:        newTaskID(),
		ChatID:    chatID,
		Project:   project,
		Prompt:    task,
		CronExpr:  durationToCron(interval),
		Status:    "pending",
		IsTask:    isTask,
		Label:     label,
		CreatedAt: time.Now(),
	}
	return t, s.AddTask(t)
}

// ListReminders returns active one-shot tasks (CronExpr == "").
// Kept for backward compatibility with !remind list.
func (s *Scheduler) ListReminders() []*Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Task
	for _, t := range s.tasks {
		if t.CronExpr == "" && t.Status == "pending" {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out
}

// ListCrons returns active recurring tasks (CronExpr != "").
// Kept for backward compatibility with !cron list.
func (s *Scheduler) ListCrons() []*Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Task
	for _, t := range s.tasks {
		if t.CronExpr != "" && t.Status == "pending" {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out
}

// --- Legacy migration ---

type legacyScheduleData struct {
	Reminders []struct {
		ID      string    `json:"id"`
		ChatID  int64     `json:"chatId"`
		Message string    `json:"message"`
		FireAt  time.Time `json:"fireAt"`
	} `json:"reminders"`
	CronJobs []struct {
		ID       string        `json:"id"`
		ChatID   int64         `json:"chatId"`
		Label    string        `json:"label"`
		Interval time.Duration `json:"interval"`
		Task     string        `json:"task"`
		IsTask   bool          `json:"isTask"`
		Enabled  bool          `json:"enabled"`
	} `json:"cronJobs"`
}

// migrateLegacy converts schedule.json → tasks.json. Lock must be held by caller.
func (s *Scheduler) migrateLegacy(schedPath string) error {
	b, err := os.ReadFile(schedPath)
	if err != nil {
		return err
	}
	var old legacyScheduleData
	if err := json.Unmarshal(b, &old); err != nil {
		return fmt.Errorf("schedule.json 마이그레이션 파싱 실패: %w", err)
	}
	for _, r := range old.Reminders {
		s.tasks = append(s.tasks, &Task{
			ID:        "r-" + r.ID,
			ChatID:    r.ChatID,
			Prompt:    r.Message,
			FireAt:    r.FireAt,
			Status:    "pending",
			Label:     "알림: " + r.Message,
			CreatedAt: time.Now(),
		})
	}
	for _, c := range old.CronJobs {
		status := "pending"
		if !c.Enabled {
			status = "paused"
		}
		label := c.Label
		if label == "" {
			label = c.Task
		}
		s.tasks = append(s.tasks, &Task{
			ID:        "c-" + c.ID,
			ChatID:    c.ChatID,
			Prompt:    c.Task,
			CronExpr:  durationToCron(c.Interval),
			Status:    status,
			IsTask:    c.IsTask,
			Label:     label,
			CreatedAt: time.Now(),
		})
	}
	if err := s.save(); err != nil {
		return err
	}
	_ = os.Rename(schedPath, schedPath+".bak")
	log.Printf("[scheduler] migrated %d reminders + %d cron jobs from schedule.json", len(old.Reminders), len(old.CronJobs))
	return nil
}

// durationToCron converts a duration to a 5-field cron expression.
func durationToCron(d time.Duration) string {
	switch {
	case d <= time.Minute:
		return "* * * * *"
	case d < time.Hour:
		return fmt.Sprintf("*/%d * * * *", int(d.Minutes()))
	case d == time.Hour:
		return "0 * * * *"
	case d < 24*time.Hour:
		return fmt.Sprintf("0 */%d * * *", int(d.Hours()))
	case d == 24*time.Hour:
		return "0 0 * * *"
	case d == 7*24*time.Hour:
		return "0 0 * * 0"
	default:
		return fmt.Sprintf("@every %dm", int(d.Minutes()))
	}
}

// --- Script pre-check ---

type scriptResult struct {
	WakeAgent bool           `json:"wakeAgent"`
	Data      map[string]any `json:"data"`
}

var (
	bashPathOnce sync.Once
	bashPathVal  string
)

// bashPath resolves a single, deterministic bash interpreter for script prechecks.
//
// The machine can have several unrelated bash.exe on PATH (Git Bash, the WSL
// launcher stub in System32, Cygwin, the WindowsApps WSL alias). A bare "bash"
// lookup picks whichever wins PATH order for the *current* process — which drifted
// under the scheduler's launch context and resolved to a bash that couldn't find
// curl/sed/awk/perl, failing every run with exit status 127. Scripts under
// .aglink/scripts/ are authored for Git Bash (its /c/... path style, its
// curl/sed/awk/perl in usr/bin), so pin that explicitly instead of trusting PATH.
func bashPath() string {
	bashPathOnce.Do(func() {
		if p := os.Getenv("AGLINK_BASH_PATH"); p != "" {
			bashPathVal = p
			return
		}
		for _, p := range []string{
			`C:\Program Files\Git\bin\bash.exe`,
			`C:\Program Files\Git\usr\bin\bash.exe`,
		} {
			if _, err := os.Stat(p); err == nil {
				bashPathVal = p
				return
			}
		}
		log.Printf("[scheduler] Git Bash not found at its usual install path — falling back to PATH lookup for \"bash\"")
		bashPathVal = "bash"
	})
	return bashPathVal
}

// runScriptPrecheck runs a bash script and parses {"wakeAgent": bool, "data": {...}}.
// Returns wakeAgent=true on any failure (safe fallback: always run when uncertain).
func runScriptPrecheck(script string) (wakeAgent bool, data map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bashPath(), "-c", script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		log.Printf("[scheduler] script precheck exec error: %v (stderr: %q) — defaulting wakeAgent=true", err, strings.TrimSpace(stderr.String()))
		return true, nil
	}
	var res scriptResult
	if jerr := json.Unmarshal(out, &res); jerr != nil {
		log.Printf("[scheduler] script precheck: invalid JSON %q — defaulting wakeAgent=true", strings.TrimSpace(string(out)))
		return true, nil
	}
	return res.WakeAgent, res.Data
}

// --- ParseSchedule (backward compat for !remind / !cron duration parsing) ---

// humanDelay renders a one-shot delay, e.g. "45분", "1시간 30분", "2일".
//
// ParseSchedule's label describes a *recurrence* ("45분마다"), which is right for
// a cron but wrong for a reminder that fires once: "remind me in 45 minutes"
// used to be confirmed as "45분마다 후". Callers append 후/뒤 themselves.
func humanDelay(d time.Duration) string {
	if d < time.Minute {
		secs := int(d.Round(time.Second).Seconds())
		if secs < 1 {
			secs = 1
		}
		return fmt.Sprintf("%d초", secs)
	}
	d = d.Round(time.Minute)
	days := int(d / (24 * time.Hour))
	hours := int(d % (24 * time.Hour) / time.Hour)
	mins := int(d % time.Hour / time.Minute)

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d일", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%d시간", hours))
	}
	// Minutes are noise next to a multi-day delay.
	if mins > 0 && days == 0 {
		parts = append(parts, fmt.Sprintf("%d분", mins))
	}
	if len(parts) == 0 {
		return "1분"
	}
	return strings.Join(parts, " ")
}

// ParseSchedule parses duration strings: English ("30m", "2h", "1d", "1w", "hourly", "daily", "weekly")
// and Korean aliases ("매시간", "매일", "매주"). The returned label describes a
// recurrence; for a one-shot delay use humanDelay.
func ParseSchedule(raw string) (time.Duration, string, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	switch raw {
	case "hourly", "매시간":
		return time.Hour, "매시간", nil
	case "daily", "매일":
		return 24 * time.Hour, "매일", nil
	case "weekly", "매주":
		return 7 * 24 * time.Hour, "매주", nil
	}
	if len(raw) < 2 {
		return 0, "", fmt.Errorf("알 수 없는 형식: %q", raw)
	}
	unit := raw[len(raw)-1]
	var n int
	if _, err := fmt.Sscanf(raw[:len(raw)-1], "%d", &n); err != nil || n <= 0 {
		return 0, "", fmt.Errorf("잘못된 값: %q", raw)
	}
	switch unit {
	case 'm':
		return time.Duration(n) * time.Minute, fmt.Sprintf("%d분마다", n), nil
	case 'h':
		return time.Duration(n) * time.Hour, fmt.Sprintf("%d시간마다", n), nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, fmt.Sprintf("%d일마다", n), nil
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, fmt.Sprintf("%d주마다", n), nil
	}
	return 0, "", fmt.Errorf("알 수 없는 단위 '%c' — m/h/d/w/hourly/daily/weekly/매시간/매일/매주 사용", unit)
}

// --- Helpers ---

func (s *Scheduler) findByID(id string) *Task {
	for _, t := range s.tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// removeByID drops a task and the timer bookkeeping that belongs to it.
//
// It used to trim only the task slice, so every one-shot that fired left its
// entry behind in stopChs — one dead channel per reminder, for the life of the
// process. Lock must be held by caller.
func (s *Scheduler) removeByID(id string) {
	for i, t := range s.tasks {
		if t.ID == id {
			s.tasks = append(s.tasks[:i], s.tasks[i+1:]...)
			break
		}
	}
	// The one-shot's goroutine has already returned, so its channel is only
	// waiting to be forgotten; deregister would close it, which is harmless but
	// pointless. Drop the entries directly.
	if entryID, ok := s.cronEntries[id]; ok {
		s.cronRunner.Remove(entryID)
		delete(s.cronEntries, id)
	}
	delete(s.stopChs, id)
}

func newTaskID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%08x", b)
}
