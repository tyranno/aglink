package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// A reminder delivered well after its time says when it was meant for, after
// the 🔔 the desktop recognises a reminder by.
func TestScheduler_LateOneShotSaysWhenItWasDue(t *testing.T) {
	s, box := newFireScheduler(t)
	due := time.Now().Add(-10 * time.Minute)
	if _, err := s.AddReminder(7, "", "회의", due); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return box.count() == 1 })
	waitFor(t, func() bool { return len(s.ListTasks("all")) == 0 }) // its save is done before TempDir cleanup
	want := "🔔 (" + due.Format("15:04") + " 예정이던 알림) 회의"
	if due.Day() != time.Now().Day() {
		want = "🔔 (" + due.Format("1/2 15:04") + " 예정이던 알림) 회의"
	}
	if got := box.msgs[0]; got != want {
		t.Errorf("late reminder = %q, want %q", got, want)
	}
}

// An on-time reminder keeps its plain form.
func TestScheduler_OnTimeOneShotHasNoPrefix(t *testing.T) {
	s, box := newFireScheduler(t)
	if _, err := s.AddReminder(7, "", "회의", time.Now().Add(20*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return box.count() == 1 })
	waitFor(t, func() bool { return len(s.ListTasks("all")) == 0 }) // its save is done before TempDir cleanup
	if got := box.msgs[0]; got != "🔔 회의" {
		t.Errorf("on-time reminder = %q", got)
	}
}

// A repeat whose run fell due while the host was off gets one notice at
// startup, and not again on the next start.
func TestMissedRepeat(t *testing.T) {
	now := time.Date(2026, 10, 31, 14, 0, 0, 0, time.Local)
	task := &Task{ID: "a", CronExpr: "0 9 * * *", Prompt: "약 먹을 시간이에요.", LastFired: now.Add(-30 * time.Hour)}
	m, ok := missedRepeat(task, now)
	if !ok || !strings.HasPrefix(m.text, "🔔 (09:00 예정이던 알림") || !strings.HasSuffix(m.text, "약 먹을 시간이에요.") {
		t.Fatalf("missed daily reminder: ok=%v text=%q", ok, m.text)
	}
	task.LastFired = now.Add(-2 * time.Hour) // 12:00 today; next 09:00 is tomorrow
	if _, ok := missedRepeat(task, now); ok {
		t.Error("nothing was missed since 12:00")
	}
	job := &Task{ID: "b", CronExpr: "@every 90m", IsTask: true, Label: "▶ 빌드 점검", LastFired: now.Add(-5 * time.Hour)}
	if m, ok := missedRepeat(job, now); !ok || !strings.Contains(m.text, "빌드 점검") {
		t.Errorf("missed @every job: ok=%v text=%q", ok, m.text)
	}
}

// A scheduled 업무 루틴 that fails says so where the task reports.
func TestScheduler_PlaybookFailureIsDelivered(t *testing.T) {
	s, box := newFireScheduler(t)
	s.SetPlaybookRun(func(int64, string) error { return errors.New("routine \"x\" not found") })
	if err := s.AddTask(&Task{ID: newTaskID(), ChatID: 7, Label: "▶ 주간 보고", PlaybookID: "x", IsTask: true, FireAt: time.Now().Add(20 * time.Millisecond), Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return box.count() == 1 })
	waitFor(t, func() bool { return len(s.ListTasks("all")) == 0 }) // its save is done before TempDir cleanup
	if got := box.msgs[0]; !strings.HasPrefix(got, "⚠️ 정기 실행 실패: 주간 보고 — ") {
		t.Errorf("failure notice = %q", got)
	}
}

// Deleting a routine takes its schedules with it.
func TestScheduler_DeleteTasksForPlaybook(t *testing.T) {
	s, _ := newFireScheduler(t)
	for _, pb := range []string{"x", "x", "y"} {
		if err := s.AddTask(&Task{ID: newTaskID(), PlaybookID: pb, IsTask: true, CronExpr: "0 9 * * *", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.DeleteTasksForPlaybook("x"); n != 2 {
		t.Errorf("removed %d, want 2", n)
	}
	if left := s.ListTasks("all"); len(left) != 1 || left[0].PlaybookID != "y" {
		t.Errorf("left: %+v", left)
	}
}
