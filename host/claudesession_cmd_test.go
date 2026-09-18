package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestAttachState_AttachDetach(t *testing.T) {
	st := newAttachState()
	if _, ok := st.Current("telegram"); ok {
		t.Fatal("처음에는 붙은 것이 없어야 함")
	}
	st.Attach("telegram", SessionInfo{Host: "dev", Name: "proj-a-cf"})
	got, ok := st.Current("telegram")
	if !ok || got.Name != "proj-a-cf" {
		t.Fatalf("붙은 세션이 어긋남: %+v %v", got, ok)
	}
	if _, ok := st.Current("web:7"); ok {
		t.Error("다른 대화까지 붙으면 안 됨")
	}
	if !st.Detach("telegram") {
		t.Error("붙어 있었으면 Detach 가 true 여야 함")
	}
	if st.Detach("telegram") {
		t.Error("이미 풀린 것을 또 풀면 false 여야 함")
	}
}

func TestAttachState_RecallByNumber(t *testing.T) {
	st := newAttachState()
	st.Remember("telegram", []SessionInfo{
		{Name: "proj-a-cf", Addressable: true},
		{Name: "proj-b-4f", Addressable: true},
	})
	got, ok := st.Recall("telegram", 2)
	if !ok || got.Name != "proj-b-4f" {
		t.Fatalf("2번이 어긋남: %+v %v", got, ok)
	}
	if _, ok := st.Recall("telegram", 0); ok {
		t.Error("0번은 없어야 함")
	}
	if _, ok := st.Recall("telegram", 3); ok {
		t.Error("범위 밖은 없어야 함")
	}
	if _, ok := st.Recall("web:7", 1); ok {
		t.Error("목록을 본 적 없는 대화에서는 번호가 듣지 않아야 함")
	}
}

func TestAttachState_ConcurrentUse(t *testing.T) {
	st := newAttachState()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st.Attach("telegram", SessionInfo{Name: "x"})
			st.Remember("telegram", []SessionInfo{{Name: "y"}})
			st.Current("telegram")
			st.Recall("telegram", 1)
			st.Detach("telegram")
		}()
	}
	wg.Wait() // -race 에서 걸리지 않으면 통과
}

// fakeSSH swaps the one seam every remote call goes through.
func fakeSSH(t *testing.T, fn func(host, cmd string) (string, error)) {
	t.Helper()
	orig := runSSHFn
	runSSHFn = func(ctx context.Context, cfg *Config, host, cmd string) (string, error) {
		return fn(host, cmd)
	}
	t.Cleanup(func() { runSSHFn = orig })
}

func testCfgWithHost() *Config {
	return &Config{
		SSHEnabled: true,
		SSHHosts: []SSHHost{
			{Name: "dev", Host: "h", User: "u", Password: "p", ClaudeSessions: true},
			{Name: "other", Host: "h2", User: "u", Password: "p"}, // 옵트인 안 함
		},
	}
}

func TestCollectSessions(t *testing.T) {
	var sawOther bool
	fakeSSH(t, func(host, cmd string) (string, error) {
		if host != "dev" {
			sawOther = true
		}
		switch {
		case strings.Contains(cmd, "cc-socks"):
			return "10957|/home/u1/project/proj-a|/t/a.jsonl|1789700022\n", nil
		case strings.Contains(cmd, "ListAgents"):
			return "  proj-a-cf [875607]  ·  interactive  ·  idle  ·  started 1h ago\n", nil
		case strings.Contains(cmd, "tail -c"):
			return "버림\n" +
				`{"type":"assistant","timestamp":"2026-09-18T03:56:46.000Z","message":{"content":[{"type":"text","text":"담기를 지시했습니다."}]}}` + "\n", nil
		}
		return "", nil
	})

	list, errs := collectSessions(context.Background(), testCfgWithHost())
	if len(errs) != 0 {
		t.Fatalf("오류가 없어야 함: %v", errs)
	}
	if sawOther {
		t.Error("claude_sessions 를 켜지 않은 호스트를 건드렸다")
	}
	if len(list) != 1 {
		t.Fatalf("1개를 기대했으나 %d개: %+v", len(list), list)
	}
	if list[0].Name != "proj-a-cf" || !list[0].Addressable {
		t.Errorf("이름/붙기 가능 여부가 어긋남: %+v", list[0])
	}
	if list[0].Last != "담기를 지시했습니다." {
		t.Errorf("마지막 한 줄이 어긋남: %q", list[0].Last)
	}
}

func TestCollectSessions_HostFailureIsReportedNotFatal(t *testing.T) {
	fakeSSH(t, func(host, cmd string) (string, error) {
		return "", errors.New("연결 거부")
	})
	list, errs := collectSessions(context.Background(), testCfgWithHost())
	if len(list) != 0 {
		t.Errorf("목록이 비어야 함: %+v", list)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "dev") {
		t.Errorf("어느 호스트가 실패했는지 알려야 함: %v", errs)
	}
}

func TestCollectSessions_NoOptedInHost(t *testing.T) {
	cfg := &Config{SSHEnabled: true, SSHHosts: []SSHHost{{Name: "other"}}}
	list, errs := collectSessions(context.Background(), cfg)
	if len(list) != 0 || len(errs) != 0 {
		t.Errorf("켠 호스트가 없으면 조용해야 함: %+v %v", list, errs)
	}
}

func TestFormatSessionList_TruncatesAndNumbers(t *testing.T) {
	long := strings.Repeat("가", 300)
	out := formatSessionList([]SessionInfo{
		{Name: "proj-a-cf", Host: "dev", Started: "1시간째", Last: truncRunes(long, maxLastLine), Addressable: true},
		{Name: "proj-b-4f", Host: "dev", Started: "2시간째", Busy: true, Addressable: true},
		{Name: "lonely", Host: "dev", Addressable: false},
	})
	if !strings.Contains(out, "1. proj-a-cf") || !strings.Contains(out, "2. proj-b-4f") {
		t.Errorf("번호가 붙어야 함:\n%s", out)
	}
	if !strings.Contains(out, "일하는 중") || !strings.Contains(out, "쉬는 중") {
		t.Errorf("busy/idle 이 보여야 함:\n%s", out)
	}
	if !strings.Contains(out, "붙을 수 없음") {
		t.Errorf("이름을 못 읽은 세션은 그렇게 표시해야 함:\n%s", out)
	}
	if strings.Contains(out, strings.Repeat("가", 120)) {
		t.Errorf("마지막 줄이 잘리지 않았다:\n%s", out)
	}
}

func TestFormatSessionList_Empty(t *testing.T) {
	if out := formatSessionList(nil); !strings.Contains(out, "세션이 없습니다") {
		t.Errorf("빈 목록 문구가 어긋남: %q", out)
	}
}

// recordingReply captures what a handler would have sent.
type recordingReply struct{ sent []string }

func (r *recordingReply) Send(chatID int64, text string) error {
	r.sent = append(r.sent, text)
	return nil
}
func (r *recordingReply) Typing(int64) {}
func (r *recordingReply) Done(int64)   {}
func (r *recordingReply) SendPhoto(int64, []byte, string) error { return nil }

// newTestBotForAttach builds the smallest Bot the session handlers need.
func newTestBotForAttach(cfg *Config) *Bot {
	return &Bot{cfgh: NewConfigHolder(cfg), attach: newAttachState()}
}

func TestResolveTarget(t *testing.T) {
	st := newAttachState()
	list := []SessionInfo{
		{Name: "proj-a-cf", Addressable: true},
		{Name: "proj-b-4f", Addressable: true},
		{Name: "lonely", Addressable: false},
	}
	st.Remember("telegram", list)

	if got, why := resolveTarget(st, "telegram", "2", list); why != "" || got.Name != "proj-b-4f" {
		t.Errorf("번호로 고르기 실패: %+v %q", got, why)
	}
	if got, why := resolveTarget(st, "telegram", "proj-a-cf", list); why != "" || got.Name != "proj-a-cf" {
		t.Errorf("이름으로 고르기 실패: %+v %q", got, why)
	}
	if got, why := resolveTarget(st, "telegram", "proj-a", list); why != "" || got.Name != "proj-a-cf" {
		t.Errorf("앞부분만 대도 골라야 함: %+v %q", got, why)
	}
	if _, why := resolveTarget(st, "telegram", "proj", list); why == "" {
		t.Error("여러 개에 걸리면 되물어야 함")
	}
	if _, why := resolveTarget(st, "telegram", "없는이름", list); why == "" {
		t.Error("없는 이름은 거절해야 함")
	}
	if _, why := resolveTarget(st, "telegram", "9", list); why == "" {
		t.Error("범위 밖 번호는 거절해야 함")
	}
	if _, why := resolveTarget(st, "telegram", "lonely", list); why == "" {
		t.Error("이름을 못 읽은 세션에는 붙을 수 없어야 함")
	}
}

func TestHandleAttachDetach(t *testing.T) {
	fakeSSH(t, func(host, cmd string) (string, error) {
		switch {
		case strings.Contains(cmd, "cc-socks"):
			return "1|/home/u1/project/proj-a|/t/a.jsonl|1789700022\n", nil
		case strings.Contains(cmd, "ListAgents"):
			return "  proj-a-cf [aaa]  ·  interactive  ·  idle  ·  started 1h ago\n", nil
		}
		return "", nil
	})
	b := newTestBotForAttach(testCfgWithHost())
	rec := &recordingReply{}

	b.handleAttach(rec, 7, "telegram", []string{"!attach", "proj-a-cf"})
	if _, ok := b.attach.Current("telegram"); !ok {
		t.Fatalf("붙지 않았다: %v", rec.sent)
	}
	if !strings.Contains(strings.Join(rec.sent, "\n"), "proj-a-cf") {
		t.Errorf("무엇에 붙었는지 알려야 함: %v", rec.sent)
	}

	rec.sent = nil
	b.handleDetach(rec, 7, "telegram")
	if _, ok := b.attach.Current("telegram"); ok {
		t.Error("풀리지 않았다")
	}
	if len(rec.sent) == 0 {
		t.Error("풀렸다고 알려야 함")
	}
}

func TestHandleAttach_UnknownName(t *testing.T) {
	fakeSSH(t, func(host, cmd string) (string, error) { return "", nil })
	b := newTestBotForAttach(testCfgWithHost())
	rec := &recordingReply{}
	b.handleAttach(rec, 7, "telegram", []string{"!attach", "없는것"})
	if _, ok := b.attach.Current("telegram"); ok {
		t.Error("없는 세션에 붙으면 안 됨")
	}
	if len(rec.sent) == 0 {
		t.Error("왜 못 붙는지 알려야 함")
	}
}
