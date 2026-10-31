package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFormatFlowReply(t *testing.T) {
	cases := []struct {
		out    string
		runErr error
		ctxErr error
		want   string
	}{
		{`{"text":"flow a: ok: 2/2 steps"}`, nil, nil, "✅ flow a: ok: 2/2 steps"},
		{`{"error":"no flow named \"x\""}`, errors.New("exit status 1"), nil, `⚠️ no flow named "x"`},
		{``, nil, context.DeadlineExceeded, "⚠️ flow 가 3m0s 안에 끝나지 않아 중단했습니다."},
		{`not json`, errors.New("boom"), nil, "⚠️ flow 실행 실패: boom"},
	}
	for _, c := range cases {
		if got := formatFlowReply([]byte(c.out), c.runErr, c.ctxErr); got != c.want {
			t.Errorf("formatFlowReply(%q) = %q, want %q", c.out, got, c.want)
		}
	}
	long := `{"text":"` + strings.Repeat("가", 5000) + `"}`
	if got := formatFlowReply([]byte(long), nil, nil); len([]rune(got)) > flowReplyMax+2 {
		t.Errorf("reply not capped: %d runes", len([]rune(got)))
	}
}
