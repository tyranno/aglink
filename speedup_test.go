package main

import (
	"strings"
	"testing"
	"time"
)

// The manager LLM call is only worth its 7-20s when the message is plausibly a
// scheduling request. Everything else must skip it.
func TestMightBeScheduleRequest(t *testing.T) {
	schedule := []string{
		"매일 아침 9시에 빌드 상태 알려줘",
		"30분 뒤에 알림 보내줘",
		"내일 배포 리마인드 해줘",
		"이 작업 예약해줘",
		"cron 으로 매주 돌려줘",
		"remind me in 10 minutes",
		"schedule a daily report",
		"run this every 5 minutes",
		"ping me at 9am",
		"14:30 에 실행",
	}
	for _, s := range schedule {
		if !mightBeScheduleRequest(s) {
			t.Errorf("should consult the manager LLM: %q", s)
		}
	}

	ordinary := []string{
		"이 함수 리팩터링 해줘",
		"테스트 통과했어?",
		"git status 확인해봐",
		"웹 채팅이 안 뜨는데 왜 그러지",
		"fix the failing test",
		"explain this stack trace",
		"코드 리뷰 부탁해",
	}
	for _, s := range ordinary {
		if mightBeScheduleRequest(s) {
			t.Errorf("should skip the manager LLM: %q", s)
		}
	}
}

// A fresh conversation starts a thread; a started one resumes it. -C must come
// before the resume subcommand — codex rejects the other order.
func TestCodexRunBaseArgs(t *testing.T) {
	fresh := codexRunBaseArgs("/work", "", false, true)
	if got := strings.Join(fresh, " "); strings.Contains(got, "resume") {
		t.Errorf("a fresh turn must not resume: %v", fresh)
	}
	if fresh[0] != "exec" || fresh[1] != "-C" || fresh[2] != "/work" {
		t.Errorf("prefix = %v", fresh[:3])
	}

	resumed := codexRunBaseArgs("/work", "thread-1", true, true)
	want := []string{"exec", "-C", "/work", "resume", "thread-1", "--ignore-user-config",
		"--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "--json"}
	if len(resumed) != len(want) {
		t.Fatalf("resumed = %v, want %v", resumed, want)
	}
	for i := range want {
		if resumed[i] != want[i] {
			t.Fatalf("resumed[%d] = %q, want %q (full: %v)", i, resumed[i], want[i], resumed)
		}
	}

	// Resume without a thread id can't resume anything.
	if got := codexRunBaseArgs("/work", "", true, true); strings.Contains(strings.Join(got, " "), "resume") {
		t.Errorf("no session id → no resume: %v", got)
	}

	// --ephemeral must never appear: it stops codex recording the rollout that
	// `exec resume` needs.
	for _, args := range [][]string{fresh, resumed} {
		for _, a := range args {
			if a == "--ephemeral" {
				t.Errorf("worker turns must not be ephemeral: %v", args)
			}
		}
	}

	// A codex build without the flag simply omits it.
	if got := codexRunBaseArgs("/d", "", false, false); strings.Contains(strings.Join(got, " "), "--ignore-user-config") {
		t.Errorf("unsupported flag must be omitted: %v", got)
	}
}

// A pruned rollout must fall back to a fresh session, the same as a lost claude
// session, instead of dead-ending the turn.
func TestIsSessionNotFound_CodexRollout(t *testing.T) {
	if !isSessionNotFound("thread/resume failed: no rollout found for thread id 019f (code -32600)") {
		t.Error("codex 'no rollout found' must trigger fresh-session recovery")
	}
	if !isSessionNotFound("No conversation found with session ID: abc") {
		t.Error("claude's message must still match")
	}
	if isSessionNotFound("some other failure") {
		t.Error("unrelated errors must not trigger recovery")
	}
}

// A reminder fires once, so its confirmation must describe a delay, not a
// recurrence. ParseSchedule's label ("45분마다") was being suffixed with "후",
// producing "45분마다 후" for "remind me in 45 minutes".
func TestHumanDelay(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{45 * time.Minute, "45분"},
		{90 * time.Minute, "1시간 30분"},
		{2 * time.Hour, "2시간"},
		{24 * time.Hour, "1일"},
		{50 * time.Hour, "2일 2시간"},
		{30 * time.Second, "30초"},
		{time.Second, "1초"},
		{0, "1초"},
		{61 * time.Second, "1분"},
	}
	for _, c := range cases {
		if got := humanDelay(c.d); got != c.want {
			t.Errorf("humanDelay(%v) = %q, want %q", c.d, got, c.want)
		}
	}

	// The recurrence label is unchanged — cron still needs it.
	if _, label, err := ParseSchedule("45m"); err != nil || label != "45분마다" {
		t.Errorf("ParseSchedule(45m) label = %q (err %v), want 45분마다", label, err)
	}
}
