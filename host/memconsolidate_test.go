package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeMemSummarizer returns out (or err) and can run a hook mid-call.
type fakeMemSummarizer struct {
	out     string
	err     error
	during  func()
	prompts []string
}

func (f *fakeMemSummarizer) Summarize(_ context.Context, prompt string) (string, error) {
	f.prompts = append(f.prompts, prompt)
	if f.during != nil {
		f.during()
	}
	return f.out, f.err
}

// A realistic grown memory file: dated sections, duplicated notes, a decision
// later reversed, a TODO marked done.
func sampleFacts() string {
	var b strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "- 확인 %02d: voice.svelte.js %d줄 근처 음성 게이트 동작은 그대로 둔다.\n", i, 2300+i)
	}
	return b.String()
}

const sampleHead = "## 2026-08-24 — 빌드/배포\n\n- 호스트 exe 위치: `E:\\Project\\My\\aglink\\host\\aglink.exe`\n"

func sampleMemory() string {
	facts := sampleFacts()
	return sampleHead +
		"- 할 일: 데스크톱 아이콘 문제 확인 — 완료\n\n" +
		"## 2026-11-07 — 회의 모드\n\n" + facts +
		string([]rune(facts)[:1200]) + "\n" + // duplicated notes
		"- 결정: 호출어는 아무 목소리나 인식 → 번복: 호출어도 내 목소리만.\n"
}

func consolidatedSample() string {
	return sampleHead + "\n## 2026-11-07 — 회의 모드\n\n" + sampleFacts() +
		"- 결정: 호출어도 내 목소리만 인식.\n"
}

func writeMem(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".aglink", "memory", "5.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNeedsConsolidation(t *testing.T) {
	doc := convMemoryDoc // cap 4000 → threshold 2800, growth 500
	cases := []struct {
		chars, last int
		want        bool
	}{
		{1000, 0, false},
		{2799, 0, false},
		{2800, 0, true},
		{3200, 3000, false}, // grew too little since the last pass
		{3500, 3000, true},
		{22000, 0, true},
	}
	for _, c := range cases {
		if got := needsConsolidation(c.chars, c.last, doc); got != c.want {
			t.Errorf("needsConsolidation(%d, %d) = %v, want %v", c.chars, c.last, got, c.want)
		}
	}
}

func TestValidateConsolidated(t *testing.T) {
	in := sampleMemory()
	good := consolidatedSample()
	if out, err := validateConsolidated(in, "```markdown\n"+good+"```", convMemoryDoc); err != nil || out != strings.TrimSpace(good) {
		t.Errorf("fenced good result: %v\n%q", err, out)
	}
	bad := map[string]string{
		"empty":     "  \n",
		"too short": "## 요약\n- 회의 모드",
		"longer":    in + in,
		"english":   "## 2026-11-07\n" + strings.Repeat("- Meeting mode starts and stops only on my voice.\n", 20),
		"no heads":  strings.ReplaceAll(good, "## ", ""),
	}
	for name, out := range bad {
		if _, err := validateConsolidated(in, out, convMemoryDoc); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
	// A huge file may shrink below 30% as long as the result is substantial.
	huge := strings.Repeat("## 2026-11-07\n- 같은 메모가 계속 반복된다 반복된다 반복된다.\n", 400)
	if _, err := validateConsolidated(huge, strings.Repeat("## 정리\n- 반복된 메모를 하나로 합쳤다, 내용 유지.\n", 80), convMemoryDoc); err != nil {
		t.Errorf("floor should accept a substantial result: %v", err)
	}
	// The profile is stricter: half the input at least, no floor.
	prof := strings.Repeat("- 답변은 완전한 한국어 문장으로.\n- git remote는 SSH만 사용.\n", 30)
	if _, err := validateConsolidated(prof, strings.Repeat("- 답변은 완전한 한국어 문장으로.\n", 12), profileMemoryDoc); err == nil {
		t.Error("profile result under 50% must be rejected")
	}
}

func TestBuildMemoryConsolidatePrompt(t *testing.T) {
	in := sampleMemory()
	p := buildMemoryConsolidatePrompt(in, convMemoryDoc)
	for _, want := range []string{in, "모순", "지어내지", "한국어는 한국어로", "3500자"} {
		if !strings.Contains(p, want) {
			t.Errorf("conv prompt missing %q", want)
		}
	}
	pp := buildMemoryConsolidatePrompt("- x", profileMemoryDoc)
	if !strings.Contains(pp, "보수적") || strings.Contains(pp, "할 일은") {
		t.Errorf("profile prompt should be dedupe-only:\n%s", pp)
	}
}

func TestConsolidateMemoryFile_RewritesWithBackup(t *testing.T) {
	in := sampleMemory()
	path := writeMem(t, in)
	s := &fakeMemSummarizer{out: consolidatedSample()}
	before, after, err := consolidateMemoryFile(context.Background(), s, path, convMemoryDoc, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if before <= after || after == 0 {
		t.Errorf("sizes %d → %d", before, after)
	}
	got, _ := os.ReadFile(path)
	if strings.TrimSpace(string(got)) != strings.TrimSpace(consolidatedSample()) {
		t.Errorf("file not rewritten:\n%s", got)
	}
	baks, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".history", "5.md.*"))
	if len(baks) != 1 {
		t.Fatalf("backups: %v", baks)
	}
	if b, _ := os.ReadFile(baks[0]); string(b) != in {
		t.Error("backup must hold the original bytes")
	}
	if tmps, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".5.md.tmp*")); len(tmps) != 0 {
		t.Errorf("temp file left behind: %v", tmps)
	}
}

func TestConsolidateMemoryFile_LeavesFileOnRejectOrRace(t *testing.T) {
	in := sampleMemory()
	cases := map[string]struct {
		s       *fakeMemSummarizer
		busy    bool
		wantErr error
	}{
		"rejected":   {s: &fakeMemSummarizer{out: "- 짧음"}},
		"call error": {s: &fakeMemSummarizer{err: errors.New("boom")}},
		"busy":       {s: &fakeMemSummarizer{out: consolidatedSample()}, busy: true, wantErr: errMemoryChanged},
	}
	for name, c := range cases {
		path := writeMem(t, in)
		_, _, err := consolidateMemoryFile(context.Background(), c.s, path, convMemoryDoc, func() bool { return c.busy })
		if err == nil || (c.wantErr != nil && !errors.Is(err, c.wantErr)) {
			t.Errorf("%s: err = %v", name, err)
		}
		if got, _ := os.ReadFile(path); string(got) != in {
			t.Errorf("%s: file must be untouched", name)
		}
	}

	// A worker appends while the summarizer runs: its note must survive.
	path := writeMem(t, in)
	appended := in + "- 새 메모: 워커가 방금 추가.\n"
	s := &fakeMemSummarizer{out: consolidatedSample(), during: func() { _ = os.WriteFile(path, []byte(appended), 0o600) }}
	if _, _, err := consolidateMemoryFile(context.Background(), s, path, convMemoryDoc, nil); !errors.Is(err, errMemoryChanged) {
		t.Errorf("concurrent write: err = %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != appended {
		t.Error("concurrent worker write was clobbered")
	}
}

func TestBackupMemoryFile_KeepsLastThree(t *testing.T) {
	path := writeMem(t, "x")
	other := filepath.Join(filepath.Dir(path), ".history", "15.md.20260101-000000.000")
	_ = os.MkdirAll(filepath.Dir(other), 0o700)
	_ = os.WriteFile(other, []byte("other"), 0o600)
	t0 := time.Date(2026, 11, 7, 10, 0, 0, 0, time.Local)
	for i := 0; i < 5; i++ {
		if err := backupMemoryFile(path, []byte{byte('a' + i)}, t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	baks, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".history", "5.md.*"))
	if len(baks) != memoryHistoryKeep {
		t.Fatalf("kept %d backups: %v", len(baks), baks)
	}
	if b, _ := os.ReadFile(baks[0]); string(b) != "c" {
		t.Errorf("oldest kept = %q, want c", b)
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("another file's backup must not be pruned")
	}
}

// End to end through the Manager: a finished turn arms the idle timer, which
// consolidates the conversation file and leaves a small profile alone.
func TestScheduleMemoryConsolidation(t *testing.T) {
	old := memoryConsolidateIdle
	memoryConsolidateIdle = 10 * time.Millisecond
	defer func() { memoryConsolidateIdle = old }()

	fc := &scriptedClient{summary: consolidatedSample(), summDone: make(chan struct{}, 4)}
	m, _ := tokenFixture(t, fc, &Config{MemoryConsolidate: true})
	t.Setenv(dataDirEnv, t.TempDir())
	_ = os.MkdirAll(filepath.Dir(userProfilePath()), 0o700)
	_ = os.WriteFile(userProfilePath(), []byte("# 사용자 프로필\n- 답변은 한국어로.\n"), 0o600)

	path := writeMem(t, sampleMemory())
	root := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	m.scheduleMemoryConsolidation("", "5", root)
	select {
	case <-fc.summDone:
	case <-time.After(5 * time.Second):
		t.Fatal("consolidation did not run")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := os.ReadFile(path)
		if strings.TrimSpace(string(got)) == strings.TrimSpace(consolidatedSample()) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("file not consolidated:\n%s", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	fc.mu.Lock()
	calls := len(fc.summCalls)
	fc.mu.Unlock()
	if calls != 1 {
		t.Errorf("summarizer calls = %d, want 1 (small profile skipped)", calls)
	}

	// Off → nothing armed.
	m2, _ := tokenFixture(t, fc, &Config{MemoryConsolidate: false})
	m2.scheduleMemoryConsolidation("", "5", root)
	if len(m2.memFiles) != 0 {
		t.Error("disabled feature must not arm timers")
	}
}

func TestConfig_MemoryConsolidateKey(t *testing.T) {
	got, err := unmarshalConfigYAML([]byte("telegram:\n  bot_token: t\n  allowed_user_ids: [1]\n"))
	if err != nil || !got.MemoryConsolidate {
		t.Fatalf("default must be on: %v", err)
	}
	if err := applySettings(got, map[string]any{"context.memory_consolidate": false}); err != nil {
		t.Fatal(err)
	}
	y, err := marshalConfigYAML(got)
	if err != nil {
		t.Fatal(err)
	}
	back, err := unmarshalConfigYAML(y)
	if err != nil || back.MemoryConsolidate {
		t.Errorf("off must round-trip: %v\n%s", err, y)
	}
}
