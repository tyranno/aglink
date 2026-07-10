package main

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type sentBox struct {
	mu   sync.Mutex
	msgs []string
}

func (b *sentBox) add(_ int64, m string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.msgs = append(b.msgs, m)
}
func (b *sentBox) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.msgs)
}

func newFireScheduler(t *testing.T) (*Scheduler, *sentBox) {
	t.Helper()
	s := NewScheduler(filepath.Join(t.TempDir(), "tasks.json"))
	box := &sentBox{}
	s.SetSend(box.add)
	return s, box
}

// A one-shot reminder fires exactly once, then leaves the task list.
func TestScheduler_OneShotFiresOnceAndIsRemoved(t *testing.T) {
	s, box := newFireScheduler(t)

	if _, err := s.AddReminder(7, "스트레칭", time.Now().Add(30*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return box.count() == 1 })
	time.Sleep(80 * time.Millisecond) // it must not fire again

	if got := box.count(); got != 1 {
		t.Errorf("reminder fired %d times, want exactly 1", got)
	}
	if n := len(s.ListTasks("all")); n != 0 {
		t.Errorf("a fired one-shot must leave the list, %d task(s) remain", n)
	}
}

// A cancelled reminder must never fire, even though its timer is already armed.
func TestScheduler_CancelledOneShotNeverFires(t *testing.T) {
	s, box := newFireScheduler(t)

	task, err := s.AddReminder(7, "취소될 알림", time.Now().Add(60*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CancelTask(task.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)

	if got := box.count(); got != 0 {
		t.Errorf("a cancelled reminder fired %d time(s)", got)
	}
	tasks := s.ListTasks("all")
	if len(tasks) != 1 || tasks[0].Status != "cancelled" {
		t.Errorf("task should remain, marked cancelled: %+v", tasks)
	}
}

// A task whose dependency is still pending defers instead of firing, and never
// deadlocks the scheduler.
func TestScheduler_DependencyNotMetDefers(t *testing.T) {
	s, box := newFireScheduler(t)

	dep, err := s.AddReminder(7, "선행", time.Now().Add(time.Hour)) // stays pending
	if err != nil {
		t.Fatal(err)
	}
	blocked := &Task{
		ID: newTaskID(), ChatID: 7, Prompt: "후행", Status: "pending",
		FireAt: time.Now().Add(30 * time.Millisecond), DependsOn: []string{dep.ID},
	}
	if err := s.AddTask(blocked); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)

	if got := box.count(); got != 0 {
		t.Errorf("a task with an unmet dependency fired %d time(s)", got)
	}
	// It must still be around, rescheduled, not dropped.
	found := false
	for _, tk := range s.ListTasks("all") {
		if tk.ID == blocked.ID {
			found = true
		}
	}
	if !found {
		t.Error("a deferred task must stay in the list")
	}
}

// Every fired or cancelled one-shot must release its timer bookkeeping.
// removeByID only trimmed the task slice, so stopChs grew for the life of the
// process — one stale channel per reminder ever fired.
func TestScheduler_FiredOneShotReleasesTimerBookkeeping(t *testing.T) {
	s, box := newFireScheduler(t)

	for i := 0; i < 5; i++ {
		if _, err := s.AddReminder(7, "알림", time.Now().Add(20*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return box.count() == 5 })
	waitFor(t, func() bool { return len(s.ListTasks("all")) == 0 })
	time.Sleep(50 * time.Millisecond)

	s.mu.Lock()
	left := len(s.stopChs)
	s.mu.Unlock()
	if left != 0 {
		t.Errorf("stopChs holds %d stale entry(ies) after every one-shot fired", left)
	}
}
