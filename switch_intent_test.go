package main

import "testing"

func TestDetectProjectSwitchIntent(t *testing.T) {
	names := []string{"myapp", "voice", "voice-server"}
	cases := []struct {
		text string
		want string
		ok   bool
	}{
		{"이제 voice-server 하자", "voice-server", true}, // 가장 긴 매칭 우선
		{"myapp 로그인 버그 보자", "myapp", true},
		{"그냥 계속 진행하자", "", false},
		{"VOICE 쪽 확인", "voice", true}, // 대소문자 무시
	}
	for _, c := range cases {
		got, ok := detectProjectSwitchIntent(c.text, names)
		if got != c.want || ok != c.ok {
			t.Errorf("detectProjectSwitchIntent(%q) = (%q,%v), want (%q,%v)", c.text, got, ok, c.want, c.ok)
		}
	}
}
