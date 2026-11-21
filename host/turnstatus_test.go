package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// statusRecSender is a MessageSender that also takes TurnStatus (like
// boundSender), recording everything.
type statusRecSender struct {
	mu       sync.Mutex
	sent     []string
	statuses []TurnStatus
	typings  int
}

func (s *statusRecSender) Send(_ int64, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, text)
	return nil
}
func (s *statusRecSender) Typing(int64) { s.mu.Lock(); s.typings++; s.mu.Unlock() }
func (s *statusRecSender) Done(int64)   {}
func (s *statusRecSender) Status(_ int64, st TurnStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append(s.statuses, st)
}
func (s *statusRecSender) snapshot() ([]string, []TurnStatus, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...), append([]TurnStatus(nil), s.statuses...), s.typings
}

// textOnlySender has no Status: the legacy per-tick message path.
type textOnlySender struct {
	mu   sync.Mutex
	sent []string
}

func (s *textOnlySender) Send(_ int64, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, text)
	return nil
}
func (s *textOnlySender) Typing(int64) {}
func (s *textOnlySender) Done(int64)   {}

func withShortHeartbeat(t *testing.T, beat, status time.Duration) {
	t.Helper()
	ob, os := heartbeatInterval, statusInterval
	heartbeatInterval, statusInterval = beat, status
	t.Cleanup(func() { heartbeatInterval, statusInterval = ob, os })
}

func TestRunHeartbeat_StatusInPlaceNotMessages(t *testing.T) {
	withShortHeartbeat(t, 40*time.Millisecond, 10*time.Millisecond)
	s := &statusRecSender{}
	start := time.Now()
	act := newTurnActivity(start, time.Hour, start.Add(time.Hour)) // far from any warning
	ctx := withTurnActivity(context.Background(), act)
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() { runHeartbeat(ctx, s, 1, "작업 진행 중", start, done, nil); close(finished) }()
	time.Sleep(130 * time.Millisecond)
	close(done)
	<-finished

	sent, sts, typings := s.snapshot()
	if len(sent) != 0 {
		t.Fatalf("routine heartbeat must not post messages when Status is supported, got %q", sent)
	}
	if len(sts) < 4 {
		t.Fatalf("want an initial status plus refreshes, got %d", len(sts))
	}
	if sts[0].Tick || sts[0].Label != "작업 진행 중" || !sts[0].Start.Equal(start) {
		t.Errorf("initial status = %+v", sts[0])
	}
	ticks := 0
	for _, st := range sts {
		if st.Tick {
			ticks++
			if !strings.HasPrefix(st.Text, "⏳ 작업 진행 중...") || st.SkipText {
				t.Errorf("tick status = %+v", st)
			}
		}
		if st.LastActivity.IsZero() {
			t.Errorf("status should carry the last activity time: %+v", st)
		}
	}
	if ticks == 0 || typings != ticks {
		t.Errorf("ticks=%d typings=%d: each heartbeat tick refreshes Typing", ticks, typings)
	}
	// Nothing after the turn ended.
	time.Sleep(30 * time.Millisecond)
	if _, after, _ := s.snapshot(); len(after) != len(sts) {
		t.Errorf("status emitted after done: %d → %d", len(sts), len(after))
	}
}

func TestRunHeartbeat_WarningStillSentAsMessage(t *testing.T) {
	withShortHeartbeat(t, 20*time.Millisecond, 20*time.Millisecond)
	s := &statusRecSender{}
	start := time.Now()
	act := newTurnActivity(start, time.Hour, start.Add(time.Minute)) // cap within 2 minutes → warning
	ctx := withTurnActivity(context.Background(), act)
	done := make(chan struct{})
	go runHeartbeat(ctx, s, 1, "작업 진행 중", start, done, nil)
	time.Sleep(50 * time.Millisecond)
	close(done)

	sent, sts, _ := s.snapshot()
	if len(sent) == 0 || !strings.Contains(sent[0], "최대 작업 시간") {
		t.Fatalf("cap warning must be a visible message, got %q", sent)
	}
	for _, st := range sts {
		if st.Tick && !st.SkipText {
			t.Errorf("a warning tick must not also create a routine status message: %+v", st)
		}
		if strings.Contains(st.Text, "최대 작업 시간") {
			t.Errorf("the in-place status shows the routine line, not the warning: %q", st.Text)
		}
	}
}

func TestRunHeartbeat_LegacySenderGetsMessages(t *testing.T) {
	withShortHeartbeat(t, 20*time.Millisecond, 5*time.Millisecond)
	s := &textOnlySender{}
	done := make(chan struct{})
	go runHeartbeat(context.Background(), s, 1, "작업 진행 중", time.Now(), done, nil)
	time.Sleep(50 * time.Millisecond)
	close(done)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 || !strings.HasPrefix(s.sent[0], "⏳ 작업 진행 중...") {
		t.Fatalf("a sender without Status keeps the per-tick message, got %q", s.sent)
	}
}

// statusRecCh is a ChannelSender that implements statusChannel.
type statusRecCh struct {
	recCh
	statuses []TurnStatus
}

func (r *statusRecCh) Status(_ Target, _ int64, st TurnStatus) { r.statuses = append(r.statuses, st) }

func TestHubStatus_FallbackForChannelsWithoutStatus(t *testing.T) {
	h := NewHub()
	legacy := &recCh{}
	modern := &statusRecCh{}
	h.Register(1, legacy)
	h.Register(1, modern)

	h.Status(WebTarget("a"), 1, TurnStatus{Label: "작업 진행 중", Text: "⏳ x"})                             // light refresh
	h.Status(WebTarget("a"), 1, TurnStatus{Label: "작업 진행 중", Text: "⏳ y", Tick: true, SkipText: true}) // narrated
	h.Status(WebTarget("a"), 1, TurnStatus{Label: "작업 진행 중", Text: "⏳ z", Tick: true})

	if len(legacy.texts) != 1 || legacy.texts[0] != "⏳ z" {
		t.Errorf("legacy channel should get only the un-skipped tick as text, got %q", legacy.texts)
	}
	if len(modern.statuses) != 3 || len(modern.texts) != 0 {
		t.Errorf("status channel: statuses=%d texts=%q", len(modern.statuses), modern.texts)
	}
}

func TestRemoteChatChannelStatusFrame(t *testing.T) {
	ch := &remoteChatChannel{send: make(chan controlOut, 4), cancel: func() {}}
	start := time.UnixMilli(1_700_000_000_000)
	last := start.Add(90 * time.Second)
	ch.Status(WebTarget("c1"), 1, TurnStatus{Label: "세션 복구 진행 중", Start: start, LastActivity: last, Text: "⏳ …", Tick: true})
	o := <-ch.send
	f := o.Frame
	if o.Kind != "frame" || f == nil || f.Type != "status" {
		t.Fatalf("want a status frame, got %+v", o)
	}
	if f.Label != "세션 복구 진행 중" || f.StartedAt != start.UnixMilli() || f.LastActivityAt != last.UnixMilli() || f.At == 0 {
		t.Errorf("frame = %+v", f)
	}
	if f.Text != "" {
		t.Errorf("status frame must not carry message text (older clients could render it): %q", f.Text)
	}
	if f.Target == nil || f.Target.ID != "c1" {
		t.Errorf("frame target = %+v", f.Target)
	}
}

// fakeTG records the Telegram calls statusMessages makes.
type fakeTG struct {
	nextID  int
	sends   []string
	edits   []string
	deletes []int
	editErr error
}

func (f *fakeTG) messages() *statusMessages {
	return newStatusMessages(
		func(_ int64, text string) (int, error) {
			f.nextID++
			f.sends = append(f.sends, text)
			return f.nextID, nil
		},
		func(_ int64, id int, text string) error {
			if f.editErr != nil {
				return f.editErr
			}
			f.edits = append(f.edits, text)
			return nil
		},
		func(_ int64, id int) error { f.deletes = append(f.deletes, id); return nil },
	)
}

func TestStatusMessages_SendOnceThenEditThenDelete(t *testing.T) {
	tg := &fakeTG{}
	m := tg.messages()
	start := time.Now()
	m.update(5, TurnStatus{Start: start, Text: "⏳ a"}) // not a tick: ignored
	m.update(5, TurnStatus{Start: start, Text: "⏳ 2분", Tick: true})
	m.update(5, TurnStatus{Start: start, Text: "⏳ 4분", Tick: true})
	m.update(5, TurnStatus{Start: start, Text: "⏳ 6분", Tick: true, SkipText: true}) // edit is still fine
	if len(tg.sends) != 1 || len(tg.edits) != 2 || tg.edits[1] != "⏳ 6분" {
		t.Fatalf("sends=%q edits=%q", tg.sends, tg.edits)
	}
	m.clear(5)
	if len(tg.deletes) != 1 || tg.deletes[0] != 1 {
		t.Fatalf("deletes=%v", tg.deletes)
	}
	// A tick of the ended turn racing with Done must not re-create the message.
	m.update(5, TurnStatus{Start: start, Text: "⏳ 8분", Tick: true})
	if len(tg.sends) != 1 {
		t.Errorf("late tick re-posted: %q", tg.sends)
	}
	// The next turn gets its own message.
	m.update(5, TurnStatus{Start: start.Add(time.Hour), Text: "⏳ 2분", Tick: true})
	if len(tg.sends) != 2 {
		t.Errorf("next turn should post a new status, sends=%q", tg.sends)
	}
}

func TestStatusMessages_SkipTextAndEditFailure(t *testing.T) {
	tg := &fakeTG{}
	m := tg.messages()
	start := time.Now()
	m.update(1, TurnStatus{Start: start, Text: "⏳ 2분", Tick: true, SkipText: true})
	if len(tg.sends) != 0 {
		t.Fatalf("SkipText must not create a status message: %q", tg.sends)
	}
	m.update(1, TurnStatus{Start: start, Text: "⏳ 4분", Tick: true})
	tg.editErr = errors.New("Bad Request: message is not modified")
	m.update(1, TurnStatus{Start: start, Text: "⏳ 6분", Tick: true})
	if len(tg.sends) != 1 {
		t.Errorf("'not modified' is not a failure, sends=%q", tg.sends)
	}
	tg.editErr = errors.New("Bad Request: message to edit not found")
	m.update(1, TurnStatus{Start: start, Text: "⏳ 8분", Tick: true})
	if len(tg.sends) != 2 || tg.sends[1] != "⏳ 8분" {
		t.Errorf("a failed edit should post anew, sends=%q", tg.sends)
	}
	m.clear(1)
	if len(tg.deletes) != 1 || tg.deletes[0] != 2 {
		t.Errorf("clear should delete the current message, deletes=%v", tg.deletes)
	}
}

func TestHeartbeatRoutineTextNeverWarns(t *testing.T) {
	start := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	act := newTurnActivity(start, 30*time.Minute, start.Add(90*time.Minute))
	act.last.Store(start.Add(84 * time.Minute).UnixNano())
	now := start.Add(89 * time.Minute)
	if msg, warn := heartbeatText("작업 진행 중", now, start, act); !warn || !strings.Contains(msg, "최대 작업 시간") {
		t.Fatalf("heartbeatText should warn near the cap: %q", msg)
	}
	if m := heartbeatRoutineText("작업 진행 중", now, start, act); m != "⏳ 작업 진행 중... (89분 0초 경과) · 마지막 활동 5분 전" {
		t.Errorf("routine = %q", m)
	}
}
