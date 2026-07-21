package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestPlaybookStore(t *testing.T) *PlaybookStore {
	t.Helper()
	s := NewPlaybookStore(filepath.Join(t.TempDir(), "playbooks.json"))
	if err := s.Load(); err != nil {
		t.Fatalf("load empty: %v", err)
	}
	return s
}

func TestPlaybookStore_MissingFileIsEmpty(t *testing.T) {
	s := newTestPlaybookStore(t)
	groups, books := s.Snapshot()
	if len(groups) != 0 || len(books) != 0 {
		t.Fatalf("expected empty store, got %d groups %d playbooks", len(groups), len(books))
	}
}

func TestPlaybookStore_UpsertAndPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "playbooks.json")
	s := NewPlaybookStore(path)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	saved, err := s.UpsertPlaybook(Playbook{
		Name: "배포 점검",
		Steps: []PlaybookStep{
			{Text: "빌드 통과 확인"},
			{Text: "   "}, // blank → dropped
			{Text: "테스트 실행"},
		},
		Delivery: []DeliveryTarget{{Kind: "folder", Dest: `\\share\deploy`}},
	}, now)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("expected minted ID")
	}
	if len(saved.Steps) != 2 {
		t.Fatalf("blank step not dropped: %d steps", len(saved.Steps))
	}
	if saved.Steps[0].ID == "" || saved.Steps[0].Kind != "check" {
		t.Fatalf("step not normalized: %+v", saved.Steps[0])
	}
	if !saved.CreatedAt.Equal(now) || !saved.UpdatedAt.Equal(now) {
		t.Fatal("timestamps not stamped")
	}

	// Reload from disk → durability.
	s2 := NewPlaybookStore(path)
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	_, books := s2.Snapshot()
	if len(books) != 1 || books[0].Name != "배포 점검" {
		t.Fatalf("reload mismatch: %+v", books)
	}
}

func TestPlaybookStore_UpsertPreservesCreatedAndRunStats(t *testing.T) {
	s := newTestPlaybookStore(t)
	t0 := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	saved, _ := s.UpsertPlaybook(Playbook{Name: "R", Steps: []PlaybookStep{{Text: "a"}}}, t0)

	// Run bumps stats.
	if _, _, _, _, err := s.Run(saved.ID, t0.Add(time.Hour)); err != nil {
		t.Fatalf("run: %v", err)
	}
	// Edit later → CreatedAt + run stats survive, UpdatedAt advances.
	t2 := t0.Add(2 * time.Hour)
	edited, err := s.UpsertPlaybook(Playbook{ID: saved.ID, Name: "R2", Steps: []PlaybookStep{{Text: "b"}}}, t2)
	if err != nil {
		t.Fatal(err)
	}
	if !edited.CreatedAt.Equal(t0) {
		t.Fatalf("CreatedAt not preserved: %v", edited.CreatedAt)
	}
	if !edited.UpdatedAt.Equal(t2) {
		t.Fatalf("UpdatedAt not advanced: %v", edited.UpdatedAt)
	}
	if edited.RunCount != 1 {
		t.Fatalf("RunCount lost on edit: %d", edited.RunCount)
	}
	if edited.Name != "R2" {
		t.Fatalf("edit not applied: %s", edited.Name)
	}
}

func TestPlaybookStore_NameRequired(t *testing.T) {
	s := newTestPlaybookStore(t)
	if _, err := s.UpsertPlaybook(Playbook{Name: "  "}, time.Now()); err == nil {
		t.Fatal("expected error for blank name")
	}
}

func TestPlaybookStore_UnknownGroupRejected(t *testing.T) {
	s := newTestPlaybookStore(t)
	if _, err := s.UpsertPlaybook(Playbook{Name: "x", GroupID: "nope"}, time.Now()); err == nil {
		t.Fatal("expected error for unknown group")
	}
}

func TestPlaybookStore_GroupTreeAndDeleteReparents(t *testing.T) {
	s := newTestPlaybookStore(t)
	root, err := s.UpsertGroup(PlaybookGroup{Name: "프로젝트A"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.UpsertGroup(PlaybookGroup{Name: "배포", ParentID: root.ID})
	if err != nil {
		t.Fatal(err)
	}
	// A routine under the child group.
	pb, err := s.UpsertPlaybook(Playbook{Name: "루틴", GroupID: child.ID, Steps: []PlaybookStep{{Text: "s"}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Delete the child → its playbook reparents to child's parent (root), not dropped.
	if err := s.DeleteGroup(child.ID); err != nil {
		t.Fatal(err)
	}
	groups, books := s.Snapshot()
	if len(groups) != 1 || groups[0].ID != root.ID {
		t.Fatalf("child not removed / root missing: %+v", groups)
	}
	if len(books) != 1 || books[0].GroupID != root.ID {
		t.Fatalf("playbook not reparented to root: %+v", books[0])
	}
	_ = pb
}

func TestPlaybookStore_GroupCycleRejected(t *testing.T) {
	s := newTestPlaybookStore(t)
	a, _ := s.UpsertGroup(PlaybookGroup{Name: "A"})
	b, _ := s.UpsertGroup(PlaybookGroup{Name: "B", ParentID: a.ID})
	// Reparent A under B → cycle.
	if _, err := s.UpsertGroup(PlaybookGroup{ID: a.ID, Name: "A", ParentID: b.ID}); err == nil {
		t.Fatal("expected cycle rejection")
	}
	// Self-parent.
	if _, err := s.UpsertGroup(PlaybookGroup{ID: a.ID, Name: "A", ParentID: a.ID}); err == nil {
		t.Fatal("expected self-parent rejection")
	}
}

func TestPlaybookStore_DeletePlaybook(t *testing.T) {
	s := newTestPlaybookStore(t)
	pb, _ := s.UpsertPlaybook(Playbook{Name: "x", Steps: []PlaybookStep{{Text: "s"}}}, time.Now())
	if err := s.DeletePlaybook(pb.ID); err != nil {
		t.Fatal(err)
	}
	if _, books := s.Snapshot(); len(books) != 0 {
		t.Fatalf("not deleted: %d", len(books))
	}
	if err := s.DeletePlaybook("missing"); err == nil {
		t.Fatal("expected error deleting missing")
	}
}

func TestPlaybookStore_RunComposesPromptAndRecords(t *testing.T) {
	s := newTestPlaybookStore(t)
	pb, _ := s.UpsertPlaybook(Playbook{
		Name:        "출시 점검",
		Description: "정기 릴리스",
		WorkDir:     `C:\proj`,
		Backend:     "codex",
		Steps: []PlaybookStep{
			{Text: "린트"},
			{Text: "빌드"},
		},
		Delivery: []DeliveryTarget{
			{Kind: "folder", Dest: `\\share\rel`, Note: "버전 하위폴더"},
			{Kind: "email", Dest: "team@x.com"},
		},
	}, time.Now())

	runAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	name, prompt, backend, workDir, err := s.Run(pb.ID, runAt)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if name != "출시 점검" || backend != "codex" || workDir != `C:\proj` {
		t.Fatalf("run returned wrong meta: name=%q backend=%q workDir=%q", name, backend, workDir)
	}
	for _, want := range []string{"출시 점검", "1. 린트", "2. 빌드", "공유 폴더", `\\share\rel`, "메일", "team@x.com", "버전 하위폴더"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	// Stats recorded.
	_, books := s.Snapshot()
	if books[0].RunCount != 1 || !books[0].LastRunAt.Equal(runAt) {
		t.Fatalf("run stats not recorded: count=%d last=%v", books[0].RunCount, books[0].LastRunAt)
	}
}

func TestPlaybookStore_RunSkillBodyPrompt(t *testing.T) {
	s := newTestPlaybookStore(t)
	body := "1. main 브랜치 최신화\n2. 유닛 테스트 실행\n3. 결과를 \\\\share\\rel 에 복사하고 팀에 메일 발송"
	pb, err := s.UpsertPlaybook(Playbook{
		Name:         "릴리스 배포",
		Description:  "정기 릴리스를 낼 때 사용",
		WorkDir:      `C:\proj`,
		Instructions: body,
	}, time.Now())
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if pb.Instructions != body {
		t.Fatalf("instructions not persisted: %q", pb.Instructions)
	}
	_, prompt, _, _, err := s.Run(pb.ID, time.Now())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Skill-style prompt carries the body verbatim and the trigger/context, and
	// does NOT fall back to the numbered legacy composer.
	for _, want := range []string{"릴리스 배포", "정기 릴리스를 낼 때 사용", `C:\proj`, "업무 내용", body} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("skill prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "점검·작업 단계") {
		t.Fatalf("skill routine used legacy composer:\n%s", prompt)
	}
}

func TestPlaybooksResponse_MarshalsEmptyArrays(t *testing.T) {
	// A nil store must still marshal to arrays (not null) so the UI's .map works.
	resp := buildPlaybooksResponse(nil)
	b, _ := json.Marshal(resp)
	got := string(b)
	if !strings.Contains(got, `"groups":[]`) || !strings.Contains(got, `"playbooks":[]`) {
		t.Fatalf("expected empty arrays, got %s", got)
	}
}

func TestPlaybookStore_LoadCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "playbooks.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewPlaybookStore(path)
	if err := s.Load(); err == nil {
		t.Fatal("expected load error on corrupt file")
	}
}
