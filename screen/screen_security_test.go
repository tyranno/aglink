package main

import "testing"

func TestParseUserPrompts(t *testing.T) {
	in := `
# 사내 인증 모듈
INISAFE | 허용 + 예

  AhnLab Safe Transaction |확인
bad line without a bar
| no title
NoButtons |
TrailingBtns | a +  + b
`
	got := parseUserPrompts(in)
	if len(got) != 3 {
		t.Fatalf("want 3 specs, got %d: %+v", len(got), got)
	}
	if got[0].Titles[0] != "INISAFE" || len(got[0].Buttons) != 2 ||
		got[0].Buttons[0] != "허용" || got[0].Buttons[1] != "예" {
		t.Errorf("INISAFE spec wrong: %+v", got[0])
	}
	if got[1].Titles[0] != "AhnLab Safe Transaction" || len(got[1].Buttons) != 1 || got[1].Buttons[0] != "확인" {
		t.Errorf("AhnLab spec wrong: %+v", got[1])
	}
	if len(got[2].Buttons) != 2 || got[2].Buttons[0] != "a" || got[2].Buttons[1] != "b" {
		t.Errorf("empty '+' segment should be dropped: %+v", got[2])
	}
	if got[0].Name != "user:INISAFE" {
		t.Errorf("name should be prefixed for reporting: %q", got[0].Name)
	}
}

func TestMatchSecurityPrompt(t *testing.T) {
	specs := builtinSecurityPrompts
	cases := []struct {
		title string
		want  string // spec name, or "" for no match
	}{
		{"파일 열기 - 보안 경고", "open-file-security-warning"},
		{"Open File - Security Warning", "open-file-security-warning"},
		{"Windows의 PC 보호", "smartscreen"},
		{"Windows protected your PC", "smartscreen"},
		{"그냥 메모장", ""},
		{"", ""},
	}
	for _, c := range cases {
		got, ok := matchSecurityPrompt(c.title, specs)
		if c.want == "" {
			if ok {
				t.Errorf("%q: expected no match, got %q", c.title, got.Name)
			}
			continue
		}
		if !ok || got.Name != c.want {
			t.Errorf("%q: want %q, got %q (ok=%v)", c.title, c.want, got.Name, ok)
		}
	}
}

func TestMatchUserPromptBeforeBuiltin(t *testing.T) {
	user := parseUserPrompts("보안 경고 | 커스텀")
	specs := append(user, builtinSecurityPrompts...)
	got, ok := matchSecurityPrompt("파일 열기 - 보안 경고", specs)
	if !ok || got.Buttons[0] != "커스텀" {
		t.Errorf("a user spec listed first should win: %+v (ok=%v)", got, ok)
	}
}

func TestIsUACTitle(t *testing.T) {
	for _, s := range []string{"사용자 계정 컨트롤", "User Account Control", "user account control"} {
		if !isUACTitle(s) {
			t.Errorf("%q should be detected as UAC", s)
		}
	}
	for _, s := range []string{"파일 열기 - 보안 경고", "메모장", ""} {
		if isUACTitle(s) {
			t.Errorf("%q should NOT be detected as UAC", s)
		}
	}
}
