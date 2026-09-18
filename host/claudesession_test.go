package main

import (
	"strings"
	"testing"
)

// 실제 원격에서 관측한 출력. 감싸는 LLM 이 코드펜스와 군말을 붙이므로 그대로
// 넣어 둔다 — 파서가 그것들을 버리는지가 이 시험의 핵심이다.
const sampleListAgents = "Raw output above, verbatim:\n" +
	"\n" +
	"```\n" +
	"Peer sessions (2):\n" +
	"  proj-a-cf [875607]  ·  interactive  ·  idle  ·  started 1h ago\n" +
	"  proj-b-4f [2265ec]  ·  interactive  ·  busy  ·  tmux sv:@0.%0  ·  started 1h ago\n" +
	"```\n"

func TestParsePeerSessions(t *testing.T) {
	got := parsePeerSessions(sampleListAgents)
	if len(got) != 2 {
		t.Fatalf("세션 2개를 기대했으나 %d개: %+v", len(got), got)
	}
	if got[0].Name != "proj-a-cf" || got[0].Ref != "875607" {
		t.Errorf("첫 세션 이름/참조가 어긋남: %+v", got[0])
	}
	if got[0].Kind != "interactive" || got[0].Busy {
		t.Errorf("첫 세션은 interactive·idle 이어야 함: %+v", got[0])
	}
	if got[0].Started != "1h ago" {
		t.Errorf("첫 세션 가동시간이 어긋남: %q", got[0].Started)
	}
	if !got[1].Busy {
		t.Errorf("둘째 세션은 busy 여야 함: %+v", got[1])
	}
}

func TestParsePeerSessions_Empty(t *testing.T) {
	for _, in := range []string{"", "Peer sessions (0):", "no sessions found", "오류가 났습니다"} {
		if got := parsePeerSessions(in); len(got) != 0 {
			t.Errorf("%q → 빈 목록이어야 하는데 %+v", in, got)
		}
	}
}

func TestParsePeerSessions_IgnoresJunkLines(t *testing.T) {
	in := "잡담 한 줄\n" +
		"  good-x [aaa111]  ·  interactive  ·  idle  ·  started 3m ago\n" +
		"  [빠진이름] 대괄호는 있지만 앞에 이름이 없다\n"
	got := parsePeerSessions(in)
	if len(got) != 1 || got[0].Name != "good-x" {
		t.Fatalf("알아볼 수 있는 한 줄만 남아야 함: %+v", got)
	}
}


func TestParseProbe(t *testing.T) {
	// 실제 원격 탐침 출력(경로는 일반화). 칸은 pid|cwd|기록파일|시작epoch.
	in := "10957|/home/u1/project/proj-a|/home/u1/.claude/projects/-home-u1-project-proj-a/aaa.jsonl|1789700022\n" +
		"11863|/home/u1/project/deep/proj-b|/home/u1/.claude/projects/-home-u1-project-deep-proj-b/bbb.jsonl|1789700105\n"
	got := parseProbe(in)
	if len(got) != 2 {
		t.Fatalf("2개를 기대했으나 %d개: %+v", len(got), got)
	}
	if got[0].PID != 10957 || got[0].Cwd != "/home/u1/project/proj-a" {
		t.Errorf("첫 항목이 어긋남: %+v", got[0])
	}
	if !strings.HasSuffix(got[0].Transcript, "aaa.jsonl") {
		t.Errorf("기록 파일 경로가 어긋남: %q", got[0].Transcript)
	}
	if got[0].StartedAt.Unix() != 1789700022 {
		t.Errorf("시작시각이 어긋남: %v", got[0].StartedAt)
	}
}

func TestParseProbe_SkipsBadLines(t *testing.T) {
	in := "\n" +
		"bash: 줄 1: 무슨 오류\n" +
		"notanumber|/a|/b|1\n" +
		"777|/home/u1/p|/home/u1/t.jsonl|\n" +
		"888|/home/u1/q\n"
	got := parseProbe(in)
	if len(got) != 1 || got[0].PID != 777 {
		t.Fatalf("성한 줄 하나만 남아야 함: %+v", got)
	}
	if !got[0].StartedAt.IsZero() {
		t.Errorf("시작시각이 비면 zero time 이어야 함: %v", got[0].StartedAt)
	}
}

func TestProbeCmd_NeverReadsTheTranscript(t *testing.T) {
	cmd := probeCmd()
	// 기록 파일은 여기서 읽지 않는다 — 경로만 찾는다. 30MB 파일을 건드리면 버그다.
	for _, forbidden := range []string{"cat ", "head -c", "grep "} {
		if strings.Contains(cmd, forbidden) {
			t.Errorf("탐침이 기록 파일을 읽으려 한다: %q 가 들어 있음", forbidden)
		}
	}
	if !strings.Contains(cmd, "cc-socks") {
		t.Error("탐침이 세션 소켓 디렉터리를 보지 않는다")
	}
}

func TestMergeSessions_JoinsByBasename(t *testing.T) {
	probe := []remoteSession{
		{PID: 1, Cwd: "/home/u1/project/proj-a", Transcript: "/t/a.jsonl"},
		{PID: 2, Cwd: "/home/u1/deep/proj-b", Transcript: "/t/b.jsonl"},
	}
	peers := []PeerSession{
		{Name: "proj-b-4f", Busy: true, Started: "1h ago"},
		{Name: "proj-a-cf", Busy: false, Started: "2h ago"},
	}
	got := mergeSessions("dev", probe, peers)
	if len(got) != 2 {
		t.Fatalf("2개를 기대했으나 %d개: %+v", len(got), got)
	}
	if got[0].Name != "proj-a-cf" || got[0].Transcript != "/t/a.jsonl" {
		t.Errorf("proj-a 가 제 이름/기록과 이어지지 않음: %+v", got[0])
	}
	if !got[0].Addressable {
		t.Error("이름이 있으면 붙을 수 있어야 함")
	}
	if !got[1].Busy || got[1].Name != "proj-b-4f" {
		t.Errorf("proj-b 가 어긋남: %+v", got[1])
	}
	if got[0].Host != "dev" {
		t.Errorf("등록 호스트 이름이 실려야 함: %q", got[0].Host)
	}
}

func TestMergeSessions_UnnamedStillListed(t *testing.T) {
	probe := []remoteSession{{PID: 9, Cwd: "/home/u1/project/lonely", Transcript: "/t/l.jsonl"}}
	got := mergeSessions("dev", probe, nil)
	if len(got) != 1 {
		t.Fatalf("이름이 없어도 목록에는 남아야 함: %+v", got)
	}
	if got[0].Addressable {
		t.Error("이름이 없으면 붙을 수 없다고 표시해야 함")
	}
	if got[0].Name != "lonely" {
		t.Errorf("이름이 없으면 작업디렉터리 이름을 보여야 함: %q", got[0].Name)
	}
}

func TestMergeSessions_PeerWithoutProbeIsKept(t *testing.T) {
	got := mergeSessions("dev", nil, []PeerSession{{Name: "ghost-11", Busy: true}})
	if len(got) != 1 || got[0].Transcript != "" || !got[0].Addressable {
		t.Fatalf("탐침에 없는 세션도 붙을 수 있게 남아야 함: %+v", got)
	}
}

func TestTruncRunes_CutsOnRuneBoundary(t *testing.T) {
	s := strings.Repeat("가", 200)
	got := truncRunes(s, 110)
	if n := len([]rune(got)); n != 111 { // 110 + 말줄임표
		t.Fatalf("110 룬 + … 이어야 하는데 %d 룬: %q", n, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("잘렸으면 말줄임표가 붙어야 함: %q", got)
	}
	for _, r := range got {
		if r == 0xFFFD {
			t.Fatal("한글이 깨졌다")
		}
	}
	if short := truncRunes("짧다", 110); short != "짧다" {
		t.Errorf("한도 안이면 그대로여야 함: %q", short)
	}
}

func TestTailSummary(t *testing.T) {
	chunk := []byte(
		`{"type":"assis` + "\n" +
			`{"type":"user","timestamp":"2026-09-18T03:55:17.000Z","message":{"content":"뭐 하는 중?"}}` + "\n" +
			`{"type":"assistant","timestamp":"2026-09-18T03:55:43.000Z","message":{"content":[{"type":"text","text":"2단계도\n초록입니다."}]}}` + "\n" +
			`{"type":"assistant","timestamp":"2026-09-18T03:56:46.000Z","message":{"content":[{"type":"thinking","thinking":"속내"},{"type":"text","text":"담기를 지시했습니다."}]}}` + "\n")
	line, last := tailSummary(chunk)
	if line != "담기를 지시했습니다." {
		t.Errorf("마지막 어시스턴트 글이 어긋남: %q", line)
	}
	if last.UTC().Format("15:04:05") != "03:56:46" {
		t.Errorf("마지막 기록 시각이 어긋남: %v", last)
	}
}

func TestTailSummary_CollapsesNewlines(t *testing.T) {
	chunk := []byte("버림\n" +
		`{"type":"assistant","timestamp":"2026-09-18T03:55:43.000Z","message":{"content":[{"type":"text","text":"첫 줄\n\n둘째 줄"}]}}` + "\n")
	line, _ := tailSummary(chunk)
	if strings.Contains(line, "\n") {
		t.Errorf("여러 줄이 한 줄로 접혀야 함: %q", line)
	}
	if line != "첫 줄 둘째 줄" {
		t.Errorf("접힌 결과가 어긋남: %q", line)
	}
}

func TestTailSummary_NoAssistant(t *testing.T) {
	line, last := tailSummary([]byte("버림\n" +
		`{"type":"user","timestamp":"2026-09-18T03:55:17.000Z","message":{"content":"안녕"}}` + "\n"))
	if line != "" {
		t.Errorf("어시스턴트 글이 없으면 빈 문자열이어야 함: %q", line)
	}
	if last.IsZero() {
		t.Error("어시스턴트 글이 없어도 마지막 기록 시각은 나와야 함")
	}
}

func TestTailSummary_Empty(t *testing.T) {
	line, last := tailSummary(nil)
	if line != "" || !last.IsZero() {
		t.Errorf("빈 입력은 빈 결과여야 함: %q %v", line, last)
	}
}

func TestTailCmd_UsesTailC(t *testing.T) {
	cmd := tailCmd("/t/a.jsonl", 200000)
	if !strings.Contains(cmd, "tail -c 200000") {
		t.Errorf("tail -c 로 읽어야 함: %q", cmd)
	}
	if strings.Contains(cmd, "cat ") {
		t.Errorf("전체를 읽으려 한다: %q", cmd)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Errorf("작은따옴표 탈출이 어긋남: %s", got)
	}
	if got := shellQuote("/t/a b.jsonl"); got != "'/t/a b.jsonl'" {
		t.Errorf("공백 있는 경로가 어긋남: %s", got)
	}
}
