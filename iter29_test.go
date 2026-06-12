package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- truncate ----

func TestTruncate_ShortString(t *testing.T) {
	got := truncate("hello", 10)
	if got != "hello" {
		t.Errorf("truncate(short) = %q, want %q", got, "hello")
	}
}

func TestTruncate_ExactLength(t *testing.T) {
	got := truncate("hello", 5)
	if got != "hello" {
		t.Errorf("truncate(exact) = %q, want %q", got, "hello")
	}
}

func TestTruncate_TooLong(t *testing.T) {
	got := truncate("hello world", 5)
	// 4 runes + ellipsis
	if len([]rune(got)) != 5 {
		t.Errorf("truncate length = %d, want 5", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncate should end with ellipsis, got %q", got)
	}
}

func TestTruncate_KoreanRunes(t *testing.T) {
	// Korean runes: each is 1 rune regardless of byte count
	got := truncate("안녕하세요 세계", 5)
	runes := []rune(got)
	if len(runes) != 5 {
		t.Errorf("truncate Korean rune count = %d, want 5", len(runes))
	}
}

func TestTruncate_NLessOrEqual1(t *testing.T) {
	got := truncate("hello", 1)
	if got != "h" {
		t.Errorf("truncate(n=1) = %q, want %q", got, "h")
	}
	got0 := truncate("hello", 0)
	if got0 != "" {
		t.Errorf("truncate(n=0) = %q, want empty", got0)
	}
}

// ---- estimateTokens ----

func TestEstimateTokens_Empty(t *testing.T) {
	if got := estimateTokens(""); got != 0 {
		t.Errorf("estimateTokens(\"\") = %d, want 0", got)
	}
}

func TestEstimateTokens_ASCIIWords(t *testing.T) {
	// "hello world" = 2 ASCII words × 1.4 = 2 (int(2.8))
	got := estimateTokens("hello world")
	if got < 2 || got > 4 {
		t.Errorf("estimateTokens(2 ASCII words) = %d, want ~2-4", got)
	}
}

func TestEstimateTokens_KoreanChars(t *testing.T) {
	// "안녕" = 2 runes × 1.4 = 2 (int(2.8))
	got := estimateTokens("안녕")
	if got < 2 || got > 4 {
		t.Errorf("estimateTokens(2 Korean chars) = %d, want ~2-4", got)
	}
}

func TestEstimateTokens_NonZeroForText(t *testing.T) {
	if got := estimateTokens("test text"); got == 0 {
		t.Error("estimateTokens should return nonzero for non-empty text")
	}
}

// ---- store.go edge cases ----

func TestAddProject_EmptyName(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStore(filepath.Join(dir, "store.json"))
	err := s.AddProject("", dir)
	if err == nil {
		t.Error("AddProject with empty name should return error")
	}
}

func TestAddProject_NonExistentPath(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStore(filepath.Join(dir, "store.json"))
	err := s.AddProject("myproj", dir+"/does-not-exist")
	if err == nil {
		t.Error("AddProject with non-existent path should return error")
	}
}

func TestAddProject_DuplicateName(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStore(filepath.Join(dir, "store.json"))
	if err := s.AddProject("p", dir); err != nil {
		t.Fatalf("first AddProject: %v", err)
	}
	err := s.AddProject("p", dir)
	if err == nil {
		t.Error("duplicate AddProject should return error")
	}
}

func TestNewConversation_EmptyTitle_GetsDefault(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStore(filepath.Join(dir, "store.json"))
	_ = s.AddProject("p", dir)
	c, err := s.NewConversation("p", "")
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	if c.Title == "" {
		t.Error("empty title should be filled with default")
	}
}

func TestGetConversation_NonExistentProject(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStore(filepath.Join(dir, "store.json"))
	_, ok := s.GetConversation("ghost", "1")
	if ok {
		t.Error("GetConversation on non-existent project should return false")
	}
}

func TestUpdateConversation_NonExistentProject(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStore(filepath.Join(dir, "store.json"))
	err := s.UpdateConversation("ghost", &Conversation{ID: "1"})
	if err == nil {
		t.Error("UpdateConversation on non-existent project should return error")
	}
}

func TestSortedConvIDsByActivity(t *testing.T) {
	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	convs := map[string]*Conversation{
		"1": {ID: "1", LastActivity: older},
		"2": {ID: "2", LastActivity: newer},
	}
	ids := sortedConvIDsByActivity(convs)
	if len(ids) != 2 || ids[0] != "2" {
		t.Errorf("sortedConvIDsByActivity = %v, want [2 1]", ids)
	}
}

func TestGetParent_WithChain(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStore(filepath.Join(dir, "store.json"))
	_ = s.AddProject("p", dir)

	parent, _ := s.NewConversation("p", "parent")
	child, _ := s.NewConversation("p", "child")
	child.ParentID = parent.ID
	_ = s.UpdateConversation("p", child)

	got, ok := s.GetParent("p", child.ID)
	if !ok {
		t.Fatal("GetParent should find parent")
	}
	if got.ID != parent.ID {
		t.Errorf("GetParent returned %s, want %s", got.ID, parent.ID)
	}
}

func TestGetParent_NoParent(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStore(filepath.Join(dir, "store.json"))
	_ = s.AddProject("p", dir)
	c, _ := s.NewConversation("p", "solo")
	_, ok := s.GetParent("p", c.ID)
	if ok {
		t.Error("GetParent on root conversation should return false")
	}
}

func TestGetSetStoredBackend(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStore(filepath.Join(dir, "store.json"))
	if got := s.GetStoredBackend(); got != "" {
		t.Errorf("GetStoredBackend initial = %q, want empty", got)
	}
	if err := s.SetStoredBackend("codex"); err != nil {
		t.Fatalf("SetStoredBackend: %v", err)
	}
	if got := s.GetStoredBackend(); got != "codex" {
		t.Errorf("GetStoredBackend after set = %q, want codex", got)
	}
}

// ---- config.go: parseBool ----

func TestParseBool_TrueValues(t *testing.T) {
	for _, v := range []string{"true", "1", "yes", "on", "TRUE", "Yes"} {
		if !parseBool(v, false) {
			t.Errorf("parseBool(%q) = false, want true", v)
		}
	}
}

func TestParseBool_FalseValues(t *testing.T) {
	for _, v := range []string{"false", "0", "no", "off", "FALSE", "No"} {
		if parseBool(v, true) {
			t.Errorf("parseBool(%q) = true, want false", v)
		}
	}
}

func TestParseBool_UnknownUsesDefault(t *testing.T) {
	if !parseBool("maybe", true) {
		t.Error("unknown value should use default=true")
	}
	if parseBool("maybe", false) {
		t.Error("unknown value should use default=false")
	}
}

// ---- config.go: IsAllowed/IsAllowedByUsername ----

func TestIsAllowed_KnownUserID(t *testing.T) {
	cfg := &Config{AllowedUserIDs: []int64{100, 200}}
	if !cfg.IsAllowed(100) {
		t.Error("known user ID should be allowed")
	}
	if cfg.IsAllowed(999) {
		t.Error("unknown user ID should not be allowed")
	}
}

func TestIsAllowedByUsername_Match(t *testing.T) {
	cfg := &Config{AllowedUsernames: []string{"alice", "bob"}}
	if !cfg.IsAllowedByUsername("alice") {
		t.Error("known username should be allowed")
	}
	if cfg.IsAllowedByUsername("charlie") {
		t.Error("unknown username should not be allowed")
	}
}

func TestIsAllowedByUsername_EmptyUsername(t *testing.T) {
	cfg := &Config{AllowedUsernames: []string{"alice"}}
	if cfg.IsAllowedByUsername("") {
		t.Error("empty username should never be allowed")
	}
}

// ---- config.go: validate ----

func TestConfigValidate_MissingToken(t *testing.T) {
	cfg := &Config{AllowedUserIDs: []int64{1}}
	if err := cfg.validate(); err == nil {
		t.Error("missing token should fail validation")
	}
}

func TestConfigValidate_MissingUserIDs(t *testing.T) {
	cfg := &Config{TelegramBotToken: "tok:123"}
	if err := cfg.validate(); err == nil {
		t.Error("missing user IDs should fail validation")
	}
}

func TestConfigValidate_InvalidDefaultBackend(t *testing.T) {
	cfg := &Config{TelegramBotToken: "tok:123", AllowedUserIDs: []int64{1}, DefaultBackend: "openai"}
	if err := cfg.validate(); err == nil {
		t.Error("invalid DEFAULT_BACKEND should fail validation")
	}
}

func TestConfigValidate_MaxWorkersExceeded(t *testing.T) {
	cfg := &Config{TelegramBotToken: "tok:123", AllowedUserIDs: []int64{1}, MaxWorkers: 51}
	if err := cfg.validate(); err == nil {
		t.Error("MaxWorkers > 50 should fail validation")
	}
}

func TestConfigValidate_ValidConfig(t *testing.T) {
	cfg := &Config{TelegramBotToken: "tok:123", AllowedUserIDs: []int64{1}, MaxWorkers: 3}
	if err := cfg.validate(); err != nil {
		t.Errorf("valid config should pass: %v", err)
	}
}

// ---- relay.go: routingHeader + chunkText ----

func TestRoutingHeader_NewConversation(t *testing.T) {
	got := routingHeader("myapp", "첫 대화", true)
	if !strings.Contains(got, "새 대화") {
		t.Errorf("new conversation header = %q, want '새 대화'", got)
	}
	if !strings.Contains(got, "myapp") {
		t.Error("header should contain project name")
	}
}

func TestRoutingHeader_Resume(t *testing.T) {
	got := routingHeader("proj", "이어가기 대화", false)
	if !strings.Contains(got, "이어가기") {
		t.Errorf("resume header = %q, want '이어가기'", got)
	}
}

func TestChunkText_ShortText(t *testing.T) {
	chunks := chunkText("short", 100)
	if len(chunks) != 1 || chunks[0] != "short" {
		t.Errorf("chunkText(short) = %v, want [short]", chunks)
	}
}

func TestChunkText_ExactLength(t *testing.T) {
	s := strings.Repeat("a", 10)
	chunks := chunkText(s, 10)
	if len(chunks) != 1 {
		t.Errorf("chunkText(exact) = %d chunks, want 1", len(chunks))
	}
}

func TestChunkText_SplitsLong(t *testing.T) {
	s := strings.Repeat("x", 15)
	chunks := chunkText(s, 10)
	if len(chunks) < 2 {
		t.Errorf("chunkText(15 chars, max=10) should produce ≥2 chunks, got %d", len(chunks))
	}
	total := 0
	for _, c := range chunks {
		total += len([]rune(c))
	}
	if total != 15 {
		t.Errorf("total runes after split = %d, want 15", total)
	}
}

func TestChunkText_PrefersNewlineSplit(t *testing.T) {
	// "aaaa\nbbb" with max=7; newline at index 4 (>7/2=3) → split there
	s := "aaaa\nbbb"
	chunks := chunkText(s, 7)
	if len(chunks) != 2 {
		t.Errorf("chunkText(newline) = %d chunks, want 2", len(chunks))
	}
	if !strings.HasSuffix(chunks[0], "\n") {
		t.Errorf("first chunk should end with newline, got %q", chunks[0])
	}
}

func TestChunkText_ZeroMax(t *testing.T) {
	// max ≤ 0 returns the whole string as one chunk
	chunks := chunkText("anything", 0)
	if len(chunks) != 1 || chunks[0] != "anything" {
		t.Errorf("chunkText(max=0) = %v", chunks)
	}
}
