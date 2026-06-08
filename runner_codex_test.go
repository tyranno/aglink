package main

import (
	"encoding/json"
	"testing"
)

// TestExtractThreadID: JSONL 스트림에서 thread_id 추출
func TestExtractThreadID(t *testing.T) {
	jsonl := "{\"type\":\"thread.started\",\"thread_id\":\"abc-123\"}\n{\"type\":\"turn.started\"}\n{\"type\":\"agent_message\",\"content\":\"hello\"}"

	got := extractThreadID(jsonl)
	if got != "abc-123" {
		t.Errorf("extractThreadID = %q, want %q", got, "abc-123")
	}
}

func TestExtractThreadID_Missing(t *testing.T) {
	jsonl := "{\"type\":\"turn.started\"}\n{\"type\":\"agent_message\",\"content\":\"hello\"}"

	got := extractThreadID(jsonl)
	if got != "" {
		t.Errorf("extractThreadID = %q, want empty", got)
	}
}

func TestParseCodexOutput_Plain(t *testing.T) {
	content := "  hello world  \n"
	got := parseCodexOutput(content)
	if got != "hello world" {
		t.Errorf("parseCodexOutput = %q, want %q", got, "hello world")
	}
}

func TestParseCodexRouteDecision(t *testing.T) {
	raw := "{\"action\":\"new\",\"project\":\"myapp\",\"newTitle\":\"새 기능\"}"
	got, err := parseCodexRouteDecision(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "new" || got.Project != "myapp" || got.NewTitle != "새 기능" {
		t.Errorf("unexpected decision: %+v", got)
	}
}

func TestCodexDefaultModel(t *testing.T) {
	// Empty CodexModel → "" so codex uses its own built-in default.
	cfg := &Config{}
	if codexWorkerModel(cfg) != "" {
		t.Error("expected empty string (let codex choose default)")
	}
	cfg.CodexModel = "o3"
	if codexWorkerModel(cfg) != "o3" {
		t.Error("expected o3")
	}
	// Manager model falls back to worker model when not set.
	cfg.CodexModel = "gpt-5.4"
	cfg.CodexManagerModel = ""
	if codexManagerModel(cfg) != "gpt-5.4" {
		t.Error("expected fallback to worker model")
	}
	cfg.CodexManagerModel = "gpt-4o-mini"
	if codexManagerModel(cfg) != "gpt-4o-mini" {
		t.Error("expected manager model override")
	}
}

func TestRouteDecisionJSONRoundTrip(t *testing.T) {
	dec := RouteDecision{Action: "resume", Project: "p1", ConversationID: "c1"}
	b, _ := json.Marshal(dec)
	var got RouteDecision
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Action != dec.Action || got.Project != dec.Project {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}
