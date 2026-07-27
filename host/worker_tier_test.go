package main

import "testing"

// classifyWorkerTier routes trivial conversational turns to the cheaper model and
// anything work-like to the premium one. It is quality-biased: any doubt → heavy,
// because there is no per-conversation override to escalate a mis-classified turn.

func TestClassifyWorkerTier_LightForChatter(t *testing.T) {
	for _, s := range []string{
		"", "고마워", "ㅇㅋ", "그거 다시 보자", "안녕", "응 좋아", "잘했어", "ok thanks",
	} {
		if got := classifyWorkerTier(s); got != "light" {
			t.Errorf("classifyWorkerTier(%q) = %q, want light", s, got)
		}
	}
}

func TestClassifyWorkerTier_HeavyForWork(t *testing.T) {
	for _, s := range []string{
		"이 버그 좀 고쳐줘",
		"implement the login flow",
		"main.go 열어서 확인해",
		"```go\nfunc x(){}\n```",
		"run go test ./...",
		"화면에서 저장 버튼 클릭해",
		"이 에러 원인 분석해줘",
		"C:\\Project\\x 경로 확인",
	} {
		if got := classifyWorkerTier(s); got != "heavy" {
			t.Errorf("classifyWorkerTier(%q) = %q, want heavy", s, got)
		}
	}
}

func TestClassifyWorkerTier_HeavyForLongMessage(t *testing.T) {
	long := make([]rune, 301)
	for i := range long {
		long[i] = '가'
	}
	if got := classifyWorkerTier(string(long)); got != "heavy" {
		t.Errorf("a >300-rune message should be heavy, got %q", got)
	}
}

// dynamicWorkerModel must be a no-op unless the claude backend AND WorkerModelLight
// are both in play — otherwise it returns exactly workerModelForBackendName.
func TestDynamicWorkerModel_DisabledPaths(t *testing.T) {
	m := &Manager{cfgh: NewConfigHolder(&Config{WorkerModel: "opus", WorkerModelLight: ""})}
	if got := m.dynamicWorkerModel("claude", "그거 다시 보자"); got != "opus" {
		t.Errorf("no WorkerModelLight → always WorkerModel, got %q", got)
	}

	m2 := &Manager{cfgh: NewConfigHolder(&Config{WorkerModel: "opus", WorkerModelLight: "sonnet"})}
	if got := m2.dynamicWorkerModel("claude", "고마워"); got != "sonnet" {
		t.Errorf("light turn should use WorkerModelLight, got %q", got)
	}
	if got := m2.dynamicWorkerModel("claude", "이 버그 고쳐줘"); got != "opus" {
		t.Errorf("heavy turn should use WorkerModel, got %q", got)
	}
	if got := m2.dynamicWorkerModel("codex", "고마워"); got == "sonnet" {
		t.Errorf("non-claude backend must not use the claude light model, got %q", got)
	}
}
