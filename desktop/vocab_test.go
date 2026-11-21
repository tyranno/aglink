package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testVocab() *voiceVocab {
	v := voiceVocab{
		Words: []string{"Headscale", "두원"},
		Replace: []vocabReplace{
			{From: "에이지링크", To: "aglink"},
			{From: "아그링크", To: "aglink"},
			{From: "에이지", To: "AG"},
			{From: "head scale", To: "Headscale"},
			{From: "ai", To: "AI"},
		},
		Snippets: []vocabSnippet{
			{Trigger: "내 메일 주소", Text: "doowon@example.com"},
			{Trigger: "서명 넣어", Text: "감사합니다.\n두원 드림"},
		},
	}
	t := v.tidy()
	return t.compile()
}

func TestVocabReplace(t *testing.T) {
	v := testVocab()
	cases := map[string]string{
		"에이지링크 켜 줘":         "aglink 켜 줘",
		"에이지 링크를 설치해":       "aglink를 설치해", // spaces inside the word, a particle after it
		"아그링크, 에이지 버전":      "aglink, AG 버전", // the longer phrase wins where it matches
		"HEAD SCALE 서버":     "Headscale 서버",
		"ai 로 정리해 줘":        "AI 로 정리해 줘",
		"he said hi":        "he said hi", // "ai" inside a word is left alone
		"그냥 아무 말":           "그냥 아무 말",
		"":                  "",
		"에이지링크에이지링크":        "aglinkaglink",
		"aglink 은 그대로 둬 줘": "aglink 은 그대로 둬 줘",
	}
	for in, want := range cases {
		if got := v.apply(in); got != want {
			t.Errorf("apply(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVocabSnippets(t *testing.T) {
	v := testVocab()
	cases := map[string]string{
		"내 메일 주소.":          "doowon@example.com", // only the trigger: only the text
		"내메일주소":             "doowon@example.com",
		" 서명 넣어! ":          "감사합니다.\n두원 드림", // line breaks kept
		"내 메일 주소 알려 줘":      "doowon@example.com 알려 줘",
		"여기에 서명 넣어 줘":       "여기에 감사합니다.\n두원 드림 줘",
		"메일 주소는 따로 있어":      "메일 주소는 따로 있어",
		"에이지링크 내 메일 주소 보내": "aglink doowon@example.com 보내", // replacements first
	}
	for in, want := range cases {
		if got := v.apply(in); got != want {
			t.Errorf("apply(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVocabTidy(t *testing.T) {
	v := voiceVocab{
		Words:    []string{" 두원 ", "", "두 원", "Headscale", "headscale"},
		Replace:  []vocabReplace{{From: " ", To: "x"}, {From: "A", To: "1"}, {From: "a", To: "2"}},
		Snippets: []vocabSnippet{{Trigger: "서명.", Text: "a\r\nb"}, {Trigger: "빈 것", Text: " "}},
	}.tidy()
	if strings.Join(v.Words, "|") != "두원|Headscale" {
		t.Errorf("words = %q", v.Words)
	}
	if len(v.Replace) != 1 || v.Replace[0].To != "1" {
		t.Errorf("replace = %+v", v.Replace)
	}
	if len(v.Snippets) != 1 || v.Snippets[0].Trigger != "서명" || v.Snippets[0].Text != "a\nb" {
		t.Errorf("snippets = %+v", v.Snippets)
	}
}

func TestVoicePrompt(t *testing.T) {
	if got := strings.Join(promptWords(nil), ", ") + "."; got != whisperPrompt {
		t.Fatalf("no user words: %q, want whisperPrompt", got)
	}
	got := promptWords([]string{"Headscale", "AGLINK", "두원"})
	if strings.Join(got[len(got)-3:], ",") != "Headscale,AGLINK,두원" || strings.Count(strings.ToLower(strings.Join(got, ",")), "aglink") != 1 {
		t.Errorf("user words last, no repeats: %q", got)
	}
	long := make([]string, 40)
	for i := range long {
		long[i] = strings.Repeat("가", 5) + string(rune('A'+i))
	}
	got = promptWords(long)
	if n := len([]rune(strings.Join(got, ", "))); n > maxPromptRunes {
		t.Errorf("prompt is %d runes, over %d", n, maxPromptRunes)
	}
	if got[0] != long[0] {
		t.Errorf("an overflowing list drops the built-in words first: %q", got)
	}
}

func TestPromptEchoWithUserWords(t *testing.T) {
	vocab := promptWords([]string{"Headscale", "두원", "나스"})
	if !isPromptEcho([]string{"Headscale", " 두원", "나스"}, vocab) {
		t.Error("the user's words read back are an echo")
	}
	if isPromptEcho([]string{"Headscale", "두원 서버 켜 줘", "나스"}, vocab) {
		t.Error("a real request is not an echo")
	}
}

func TestLoadVocabReloadsOnChange(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGLINK_HOME", dir)
	if v := loadVocab(); len(v.Words) != 0 || v.apply("에이지링크") != "에이지링크" {
		t.Fatal("no file: nothing changes")
	}
	if err := saveVocab(voiceVocab{Words: []string{"두원"}, Replace: []vocabReplace{{From: "에이지링크", To: "aglink"}}}); err != nil {
		t.Fatal(err)
	}
	if got := loadVocab().apply("에이지링크 열어"); got != "aglink 열어" {
		t.Errorf("after save: %q", got)
	}
	if !strings.HasSuffix(voicePrompt(), ", 두원.") {
		t.Errorf("prompt = %q", voicePrompt())
	}
	p := filepath.Join(dir, vocabFileName)
	if err := os.WriteFile(p, []byte(`{"replace":[{"from":"아그링크","to":"AG"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(p, later, later)
	if got := loadVocab().apply("아그링크 에이지링크"); got != "AG 에이지링크" {
		t.Errorf("after edit: %q", got)
	}
	if err := os.WriteFile(p, []byte(`{broken`), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(p, later.Add(time.Second), later.Add(time.Second))
	if got := loadVocab().apply("아그링크"); got != "아그링크" {
		t.Errorf("broken file: %q", got)
	}
	info, _ := (&VoiceService{}).VocabInfo()
	if !strings.Contains(info, `"error":"`) || strings.Contains(info, `"error":""`) {
		t.Errorf("broken file is reported: %s", info)
	}
	os.Remove(p)
	if got := loadVocab().apply("아그링크"); got != "아그링크" {
		t.Errorf("file removed: %q", got)
	}
}

func TestSaveVocabFromUI(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir())
	s := &VoiceService{}
	if err := s.SaveVocab(`{"words":["두원"],"snippets":[{"trigger":"서명","text":"a\nb"}]}`); err != nil {
		t.Fatal(err)
	}
	info, _ := s.VocabInfo()
	for _, want := range []string{`"words":["두원"]`, `"replace":[]`, `"text":"a\nb"`, `"builtin":["aglink"`} {
		if !strings.Contains(info, want) {
			t.Errorf("VocabInfo lacks %s: %s", want, info)
		}
	}
	if s.SaveVocab("not json") == nil {
		t.Error("bad JSON must be refused")
	}
}

func TestFixLeavesSnippetsForSending(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir())
	v := testVocab()
	if got := v.fix("에이지링크 내 메일 주소"); got != "aglink 내 메일 주소" {
		t.Errorf("fix = %q", got)
	}
	if err := saveVocab(voiceVocab{Snippets: v.Snippets}); err != nil {
		t.Fatal(err)
	}
	if got := (&VoiceService{}).ExpandSnippets("서명 넣어."); got != "감사합니다.\n두원 드림" {
		t.Errorf("ExpandSnippets = %q", got)
	}
}
