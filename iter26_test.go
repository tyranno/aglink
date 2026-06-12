package main

import (
	"testing"
	"time"
)

// ---- memoryWorkerStatusStore ----

func TestWorkerStatus_SetGet(t *testing.T) {
	s := NewMemoryWorkerStatusStore()
	ws := WorkerStatus{
		Project:        "myapp",
		ConversationID: "1",
		Title:          "Test",
		Status:         "running",
		StartTime:      time.Now(),
	}
	if err := s.SetStatus(ws); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	got, ok := s.GetStatus("myapp", "1")
	if !ok {
		t.Fatal("GetStatus should find the status we just set")
	}
	if got.Status != "running" {
		t.Errorf("Status = %q, want \"running\"", got.Status)
	}
}

func TestWorkerStatus_UpdateToCompleted_MovesToRecent(t *testing.T) {
	s := NewMemoryWorkerStatusStore()
	ws := WorkerStatus{Project: "myapp", ConversationID: "1", Title: "T", Status: "running", StartTime: time.Now()}
	_ = s.SetStatus(ws)
	if err := s.UpdateStatus("myapp", "1", "completed", ""); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	// Should NOT appear in ListActive.
	active := s.ListActive()
	for _, a := range active {
		if a.Project == "myapp" && a.ConversationID == "1" {
			t.Error("completed worker should not appear in ListActive")
		}
	}

	// Should appear in ListRecent.
	recent := s.ListRecent(10)
	if len(recent) == 0 {
		t.Fatal("completed worker should appear in ListRecent")
	}
	if recent[0].Status != "completed" {
		t.Errorf("Status = %q, want \"completed\"", recent[0].Status)
	}
}

func TestWorkerStatus_ListRecent_Capped(t *testing.T) {
	s := NewMemoryWorkerStatusStore()
	for i := range 60 {
		ws := WorkerStatus{Project: "p", ConversationID: string(rune('a' + i)), Title: "T", Status: "running", StartTime: time.Now()}
		_ = s.SetStatus(ws)
		_ = s.UpdateStatus("p", string(rune('a'+i)), "completed", "")
	}
	recent := s.ListRecent(100)
	if len(recent) > 50 {
		t.Errorf("recent list should be capped at 50, got %d", len(recent))
	}
}

func TestWorkerStatus_UpdateNotFound_ReturnsError(t *testing.T) {
	s := NewMemoryWorkerStatusStore()
	err := s.UpdateStatus("unknown", "99", "completed", "")
	if err == nil {
		t.Error("UpdateStatus on unknown worker should return error")
	}
}

func TestWorkerStatus_GetStatus_FallsBackToRecent(t *testing.T) {
	s := NewMemoryWorkerStatusStore()
	ws := WorkerStatus{Project: "p", ConversationID: "1", Title: "T", Status: "running", StartTime: time.Now()}
	_ = s.SetStatus(ws)
	_ = s.UpdateStatus("p", "1", "failed", "timeout")

	got, ok := s.GetStatus("p", "1")
	if !ok {
		t.Fatal("GetStatus should find completed worker in recent history")
	}
	if got.Status != "failed" {
		t.Errorf("Status = %q, want \"failed\"", got.Status)
	}
}

// ---- firstJSONObject edge cases ----

func TestFirstJSONObject_BracesInStringValues(t *testing.T) {
	// A JSON value containing braces inside a string shouldn't confuse the parser.
	s := `{"action":"new","newTitle":"fix {bug} in code"}`
	got := firstJSONObject(s)
	if got != s {
		t.Errorf("firstJSONObject returned %q, want original", got)
	}
}

func TestFirstJSONObject_PrefixText(t *testing.T) {
	s := `Some prose before. {"action":"resume"} and after.`
	got := firstJSONObject(s)
	if got != `{"action":"resume"}` {
		t.Errorf("firstJSONObject = %q, want {\"action\":\"resume\"}", got)
	}
}

func TestFirstJSONObject_NoObject(t *testing.T) {
	got := firstJSONObject("no braces here")
	if got != "" {
		t.Errorf("firstJSONObject(\"no braces\") = %q, want empty", got)
	}
}

func TestFirstJSONObject_Nested(t *testing.T) {
	s := `{"outer":{"inner":"val"}}`
	got := firstJSONObject(s)
	if got != s {
		t.Errorf("firstJSONObject(nested) = %q, want full object", got)
	}
}
