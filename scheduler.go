package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Reminder fires once after a delay and sends a notification message.
type Reminder struct {
	ID      string    `json:"id"`
	ChatID  int64     `json:"chatId"`
	Message string    `json:"message"`
	FireAt  time.Time `json:"fireAt"`
}

// CronJob fires repeatedly on a fixed interval.
// IsTask=true routes the task through Manager/Worker like a Telegram message.
type CronJob struct {
	ID       string        `json:"id"`
	ChatID   int64         `json:"chatId"`
	Label    string        `json:"label"`    // human-readable schedule description
	Interval time.Duration `json:"interval"` // how often to fire
	NextFire time.Time     `json:"nextFire"`
	Task     string        `json:"task"`   // notification text or Claude prompt
	IsTask   bool          `json:"isTask"` // true → dispatch through Manager
	Enabled  bool          `json:"enabled"`
}

type scheduleData struct {
	Reminders []*Reminder `json:"reminders"`
	CronJobs  []*CronJob  `json:"cronJobs"`
}

// Scheduler manages reminders and cron jobs, persisting to a JSON file.
type Scheduler struct {
	mu       sync.Mutex
	path     string
	data     scheduleData
	nextID   int
	send     func(chatID int64, text string)
	dispatch func(chatID int64, text string)
}

func NewScheduler(path string) *Scheduler {
	return &Scheduler{path: path}
}

func (s *Scheduler) SetSend(f func(int64, string)) {
	s.mu.Lock()
	s.send = f
	s.mu.Unlock()
}

func (s *Scheduler) SetDispatch(f func(int64, string)) {
	s.mu.Lock()
	s.dispatch = f
	s.mu.Unlock()
}

func (s *Scheduler) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return err
	}
	for _, r := range s.data.Reminders {
		if n, _ := strconv.Atoi(r.ID); n > s.nextID {
			s.nextID = n
		}
	}
	for _, c := range s.data.CronJobs {
		if n, _ := strconv.Atoi(c.ID); n > s.nextID {
			s.nextID = n
		}
	}
	return nil
}

func (s *Scheduler) save() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Scheduler) newID() string {
	s.nextID++
	return strconv.Itoa(s.nextID)
}

func (s *Scheduler) AddReminder(chatID int64, msg string, fireAt time.Time) (*Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &Reminder{ID: s.newID(), ChatID: chatID, Message: msg, FireAt: fireAt}
	s.data.Reminders = append(s.data.Reminders, r)
	return r, s.save()
}

func (s *Scheduler) AddCron(chatID int64, label string, interval time.Duration, task string, isTask bool) (*CronJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &CronJob{
		ID:       s.newID(),
		ChatID:   chatID,
		Label:    label,
		Interval: interval,
		NextFire: time.Now().Add(interval),
		Task:     task,
		IsTask:   isTask,
		Enabled:  true,
	}
	s.data.CronJobs = append(s.data.CronJobs, c)
	return c, s.save()
}

func (s *Scheduler) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.data.Reminders {
		if r.ID == id {
			s.data.Reminders = append(s.data.Reminders[:i], s.data.Reminders[i+1:]...)
			_ = s.save()
			return true
		}
	}
	for i, c := range s.data.CronJobs {
		if c.ID == id {
			s.data.CronJobs = append(s.data.CronJobs[:i], s.data.CronJobs[i+1:]...)
			_ = s.save()
			return true
		}
	}
	return false
}

func (s *Scheduler) ListReminders() []*Reminder {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Reminder, len(s.data.Reminders))
	copy(out, s.data.Reminders)
	return out
}

func (s *Scheduler) ListCrons() []*CronJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*CronJob, len(s.data.CronJobs))
	copy(out, s.data.CronJobs)
	return out
}

// Run is the background scheduler loop. Call in a goroutine.
func (s *Scheduler) Run() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.tick()
	}
}

func (s *Scheduler) tick() {
	now := time.Now()
	s.mu.Lock()

	var fired bool

	// Fire pending reminders
	kept := s.data.Reminders[:0]
	for _, r := range s.data.Reminders {
		if now.After(r.FireAt) {
			chatID, msg := r.ChatID, r.Message
			go s.send(chatID, "⏰ 알림: "+msg)
			fired = true
		} else {
			kept = append(kept, r)
		}
	}
	s.data.Reminders = kept

	// Fire due cron jobs
	for _, c := range s.data.CronJobs {
		if !c.Enabled || now.Before(c.NextFire) {
			continue
		}
		c.NextFire = now.Add(c.Interval)
		chatID, task, isTask := c.ChatID, c.Task, c.IsTask
		go func() {
			if isTask {
				s.dispatch(chatID, task)
			} else {
				s.send(chatID, "🔔 "+task)
			}
		}()
		fired = true
	}

	s.mu.Unlock()

	if fired {
		if err := s.save(); err != nil {
			log.Printf("[scheduler] save error: %v", err)
		}
	}
}

// ParseSchedule parses "30m", "2h", "1d", "hourly", "daily", "weekly" into a duration and label.
func ParseSchedule(raw string) (time.Duration, string, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	switch raw {
	case "hourly":
		return time.Hour, "매시간", nil
	case "daily":
		return 24 * time.Hour, "매일", nil
	case "weekly":
		return 7 * 24 * time.Hour, "매주", nil
	}
	if len(raw) < 2 {
		return 0, "", fmt.Errorf("알 수 없는 형식: %q", raw)
	}
	unit := raw[len(raw)-1]
	n, err := strconv.Atoi(raw[:len(raw)-1])
	if err != nil || n <= 0 {
		return 0, "", fmt.Errorf("잘못된 값: %q", raw)
	}
	switch unit {
	case 'm':
		return time.Duration(n) * time.Minute, fmt.Sprintf("%d분마다", n), nil
	case 'h':
		return time.Duration(n) * time.Hour, fmt.Sprintf("%d시간마다", n), nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, fmt.Sprintf("%d일마다", n), nil
	}
	return 0, "", fmt.Errorf("알 수 없는 단위 '%c' — m/h/d/hourly/daily/weekly 사용", unit)
}
