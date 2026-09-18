package main

import (
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

