package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestResolveWebOwner covers the three owner-resolution outcomes: explicit
// config wins, else fall back to the first allowed user, else disabled.
func TestResolveWebOwner(t *testing.T) {
	if got, ok := resolveWebOwner(42, []int64{1, 2}); got != 42 || !ok {
		t.Errorf("explicit owner: got (%d, %v), want (42, true)", got, ok)
	}
	if got, ok := resolveWebOwner(0, []int64{5, 6}); got != 5 || !ok {
		t.Errorf("fallback to allowed[0]: got (%d, %v), want (5, true)", got, ok)
	}
	if got, ok := resolveWebOwner(0, nil); got != 0 || ok {
		t.Errorf("no owner: got (%d, %v), want (0, false)", got, ok)
	}
}

func TestTokenOK(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ws?token=secret", nil)
	if !tokenOK(r, "secret") {
		t.Error("query token should match")
	}
	if tokenOK(r, "other") {
		t.Error("wrong token must fail")
	}
	r2 := httptest.NewRequest(http.MethodGet, "/api/upload", nil)
	r2.Header.Set("Authorization", "Bearer secret")
	if !tokenOK(r2, "secret") {
		t.Error("bearer token should match")
	}
	r3 := httptest.NewRequest(http.MethodGet, "/ws", nil)
	if tokenOK(r3, "secret") {
		t.Error("missing token must fail")
	}
}

func TestLoadOrCreateWebToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	tok1, err := loadOrCreateWebToken("")
	if err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}
	if tok1 == "" {
		t.Fatal("first call: expected non-empty token")
	}

	tok2, err := loadOrCreateWebToken("")
	if err != nil {
		t.Fatalf("second call: unexpected error: %v", err)
	}
	if tok2 != tok1 {
		t.Errorf("token not persisted across calls: first=%q second=%q", tok1, tok2)
	}

	if got, err := loadOrCreateWebToken("explicit"); err != nil || got != "explicit" {
		t.Errorf("loadOrCreateWebToken(%q) = (%q, %v), want (%q, nil)", "explicit", got, err, "explicit")
	}
}

func TestOriginOK(t *testing.T) {
	cases := []struct {
		origin string
		want   bool
	}{
		{"", true},
		{"http://127.0.0.1:27271", true},
		{"http://localhost:27271", true},
		{"http://localhost", true},
		{"http://evil.com", false},
		{"https://example.org:27271", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := originOK(r); got != c.want {
			t.Errorf("originOK(%q)=%v, want %v", c.origin, got, c.want)
		}
	}
}

// TestOriginOK_Rejects checks originOK against spoofed/hostile Origin headers
// (subdomain confusables, the sandboxed "null" origin, a bare foreign host).
func TestOriginOK_Rejects(t *testing.T) {
	cases := []struct {
		origin string
		want   bool
	}{
		{"null", false},
		{"http://127.0.0.1.evil.com", false},
		{"http://localhost.evil.com", false},
		{"http://evil.com", false},
		{"http://127.0.0.1:27271", true},
		{"http://localhost:27271", true},
		{"", true},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := originOK(r); got != c.want {
			t.Errorf("originOK(%q) = %v, want %v", c.origin, got, c.want)
		}
	}
}

// buildConversationsResponse (shared by the control-API list_conversations) puts
// the active conversation first within its project and tags each with a channel.
func TestBuildConversationsResponse_ListsActiveTopics(t *testing.T) {
	dir := t.TempDir()
	st := NewFileStore(filepath.Join(dir, "store.json"))
	if err := st.Load(); err != nil {
		t.Fatalf("load store: %v", err)
	}
	projectDir := filepath.Join(dir, "alpha")
	if err := os.Mkdir(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := st.AddProject("alpha", projectDir); err != nil {
		t.Fatalf("add project: %v", err)
	}

	oldConv, err := st.NewConversation("alpha", "오래된 주제", "")
	if err != nil {
		t.Fatalf("new old conversation: %v", err)
	}
	oldConv.Summary = "예전 작업 요약"
	oldConv.LastActivity = time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	oldConv.Started = true
	if err := st.UpdateConversation("alpha", oldConv); err != nil {
		t.Fatalf("update old conversation: %v", err)
	}

	activeConv, err := st.NewConversation("alpha", "현재 주제", "")
	if err != nil {
		t.Fatalf("new active conversation: %v", err)
	}
	activeConv.Summary = "현재 작업 요약"
	activeConv.LastActivity = time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	activeConv.Started = true
	activeConv.Backend = "codex"
	if err := st.UpdateConversation("alpha", activeConv); err != nil {
		t.Fatalf("update active conversation: %v", err)
	}
	if err := st.SetActive("alpha", activeConv.ID); err != nil {
		t.Fatalf("set active: %v", err)
	}

	resp := buildConversationsResponse(st)
	if resp.Active.Project != "alpha" || resp.Active.ConversationID != activeConv.ID {
		t.Fatalf("active = %+v, want alpha/%s", resp.Active, activeConv.ID)
	}
	if len(resp.Projects) != 1 {
		t.Fatalf("projects len = %d, want 1", len(resp.Projects))
	}
	if resp.Projects[0].Name != "alpha" || resp.Projects[0].Path != projectDir {
		t.Fatalf("project = %+v, want alpha/%s", resp.Projects[0], projectDir)
	}
	convs := resp.Projects[0].Conversations
	if len(convs) != 2 {
		t.Fatalf("conversation len = %d, want 2", len(convs))
	}
	first := convs[0]
	if first.ID != activeConv.ID || first.Title != "현재 주제" || !first.Active || first.Summary != "현재 작업 요약" || first.Backend != "codex" {
		t.Fatalf("first conversation = %+v, want active conversation first", first)
	}
	if first.LastActivity == "" {
		t.Fatal("lastActivity should be populated")
	}
}

// Continuation chains collapse into a single grouped topic under the root title.
func TestBuildConversationsResponse_GroupsContinuationChains(t *testing.T) {
	dir := t.TempDir()
	st := NewFileStore(filepath.Join(dir, "store.json"))
	if err := st.Load(); err != nil {
		t.Fatalf("load store: %v", err)
	}
	projectDir := filepath.Join(dir, "alpha")
	if err := os.Mkdir(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := st.AddProject("alpha", projectDir); err != nil {
		t.Fatalf("add project: %v", err)
	}

	root, err := st.NewConversation("alpha", "긴 작업", "")
	if err != nil {
		t.Fatalf("new root conversation: %v", err)
	}
	root.Summary = "첫 구간"
	root.LastActivity = time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	root.Started = true

	cont, err := st.NewConversation("alpha", "긴 작업 (시리즈 2)", "")
	if err != nil {
		t.Fatalf("new continuation conversation: %v", err)
	}
	cont.ParentID = root.ID
	cont.IsContinuation = true
	cont.Summary = "이어진 구간"
	cont.LastActivity = time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	cont.Started = true
	cont.Backend = "claude"
	root.ChildID = cont.ID
	if err := st.UpdateConversation("alpha", root); err != nil {
		t.Fatalf("update root conversation: %v", err)
	}
	if err := st.UpdateConversation("alpha", cont); err != nil {
		t.Fatalf("update continuation conversation: %v", err)
	}

	other, err := st.NewConversation("alpha", "다른 작업", "")
	if err != nil {
		t.Fatalf("new other conversation: %v", err)
	}
	other.Summary = "별도 주제"
	other.LastActivity = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	other.Started = true
	if err := st.UpdateConversation("alpha", other); err != nil {
		t.Fatalf("update other conversation: %v", err)
	}
	if err := st.SetActive("alpha", cont.ID); err != nil {
		t.Fatalf("set active: %v", err)
	}

	resp := buildConversationsResponse(st)
	if len(resp.Projects) != 1 {
		t.Fatalf("projects len = %d, want 1", len(resp.Projects))
	}
	convs := resp.Projects[0].Conversations
	if len(convs) != 2 {
		t.Fatalf("conversation len = %d, want 2 grouped topics", len(convs))
	}
	first := convs[0]
	if first.ID != cont.ID || first.Title != "긴 작업" || first.Summary != "이어진 구간" || first.Backend != "claude" || !first.Active {
		t.Fatalf("grouped continuation topic = %+v, want active latest child under root title", first)
	}
	for _, conv := range convs {
		if conv.Title == "긴 작업 (시리즈 2)" {
			t.Fatalf("continuation child should not be listed as a separate topic: %+v", convs)
		}
	}
}

// TestWSFrameJSONTags locks the wire protocol between aglink and app.js:
// field names and omitempty behavior must not drift silently. wsFrame is still
// used by the control-API channel that forwards frames to aglink-chat.
func TestWSFrameJSONTags(t *testing.T) {
	imgB, err := json.Marshal(wsFrame{Type: "image", Caption: "c", Data: "ZGF0YQ=="})
	if err != nil {
		t.Fatal(err)
	}
	var imgKeys map[string]json.RawMessage
	if err := json.Unmarshal(imgB, &imgKeys); err != nil {
		t.Fatal(err)
	}
	wantImgKeys := map[string]bool{"type": true, "caption": true, "data": true}
	if len(imgKeys) != len(wantImgKeys) {
		t.Fatalf("image frame keys = %v, want exactly %v", imgKeys, wantImgKeys)
	}
	for k := range imgKeys {
		if !wantImgKeys[k] {
			t.Errorf("unexpected key %q in image frame JSON: %s", k, imgB)
		}
	}
	if _, ok := imgKeys["text"]; ok {
		t.Errorf("empty text must be omitted via omitempty, got: %s", imgB)
	}

	textB, err := json.Marshal(wsFrame{Type: "text", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	var textKeys map[string]json.RawMessage
	if err := json.Unmarshal(textB, &textKeys); err != nil {
		t.Fatal(err)
	}
	wantTextKeys := map[string]bool{"type": true, "text": true}
	if len(textKeys) != len(wantTextKeys) {
		t.Fatalf("text frame keys = %v, want exactly %v", textKeys, wantTextKeys)
	}
}
