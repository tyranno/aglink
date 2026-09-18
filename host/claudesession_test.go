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
