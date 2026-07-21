package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// A Playbook is a reusable work routine (업무): the recorded inspection/build
// steps a user repeats across similar projects, plus where the result is
// delivered (a shared folder, an email, …). Playbooks are grouped in a tree so
// similar routines cluster, and "running" one composes its steps into a single
// prompt the AI worker executes — automating the repeat.
//
// Unlike conversation groups (which live in browser localStorage), playbooks are
// persisted server-side in playbooks.json so a routine created in the desktop app
// is visible in the web/chat UI too and survives a cache clear. The storage
// contract mirrors Scheduler (tasks.json): atomic tmp+rename under a mutex.

// PlaybookStep is one recorded action in a routine.
type PlaybookStep struct {
	ID   string `json:"id"`
	Text string `json:"text"`           // what to check / do — becomes a numbered line in the run prompt
	Kind string `json:"kind,omitempty"` // "check" (default) | "prompt" | "command"; advisory label only
}

// DeliveryTarget records where a routine's output is delivered (배포 경로).
type DeliveryTarget struct {
	Kind string `json:"kind"`           // "folder" | "email" | "other"
	Dest string `json:"dest"`           // folder path, email address, …
	Note string `json:"note,omitempty"` // optional free-text (e.g. subject line, subfolder rule)
}

// PlaybookGroup is a node in the routine tree. ParentID == "" is a root node.
type PlaybookGroup struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ParentID  string `json:"parentId,omitempty"`
	Collapsed bool   `json:"collapsed,omitempty"`
}

// Playbook is a single reusable routine, modeled after a Claude "skill": a name,
// a Description that says when to use it (the trigger), and Instructions — a
// free-form natural-language body the AI reads and executes on its own, rather
// than a fixed list of fields. The legacy Steps/Delivery structured form is kept
// only so pre-existing routines still load and run; new routines use Instructions.
type Playbook struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	GroupID      string           `json:"groupId,omitempty"`      // "" = ungrouped (tree root)
	Description  string           `json:"description,omitempty"`  // when/what — the skill's trigger
	Instructions string           `json:"instructions,omitempty"` // natural-language body (the skill itself)
	Backend      string           `json:"backend,omitempty"`      // backend to run on ("" = default)
	WorkDir      string           `json:"workDir,omitempty"`      // working directory the routine targets
	Steps        []PlaybookStep   `json:"steps,omitempty"`        // legacy structured steps (fallback)
	Delivery     []DeliveryTarget `json:"delivery,omitempty"`     // legacy structured delivery (fallback)
	CreatedAt    time.Time        `json:"createdAt"`
	UpdatedAt    time.Time        `json:"updatedAt"`
	LastRunAt    time.Time        `json:"lastRunAt,omitempty"`
	RunCount     int              `json:"runCount,omitempty"`
}

// playbookData is the root persisted to playbooks.json.
type playbookData struct {
	SchemaVersion int              `json:"schemaVersion"`
	Groups        []*PlaybookGroup `json:"groups"`
	Playbooks     []*Playbook      `json:"playbooks"`
}

const playbookSchemaVersion = 1

// PlaybookStore persists routines to playbooks.json. Mirrors Scheduler's storage
// contract (atomic write, single mutex). Every exported mutator persists before
// returning, so the file is always consistent with in-memory state.
type PlaybookStore struct {
	mu   sync.Mutex
	path string
	data playbookData
}

func NewPlaybookStore(path string) *PlaybookStore {
	return &PlaybookStore{path: path, data: playbookData{SchemaVersion: playbookSchemaVersion}}
}

// Load reads playbooks.json. A missing file is not an error (empty store).
func (s *PlaybookStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var d playbookData
	if err := json.Unmarshal(b, &d); err != nil {
		return err
	}
	if d.Groups == nil {
		d.Groups = []*PlaybookGroup{}
	}
	if d.Playbooks == nil {
		d.Playbooks = []*Playbook{}
	}
	s.data = d
	return nil
}

// save writes playbooks.json atomically. Caller must hold the lock.
func (s *PlaybookStore) save() error {
	s.data.SchemaVersion = playbookSchemaVersion
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Snapshot returns a deep copy of the tree for read-only serialization, so a
// caller marshaling it can't race a concurrent mutator.
func (s *PlaybookStore) Snapshot() ([]*PlaybookGroup, []*Playbook) {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := make([]*PlaybookGroup, len(s.data.Groups))
	for i, g := range s.data.Groups {
		cp := *g
		groups[i] = &cp
	}
	books := make([]*Playbook, len(s.data.Playbooks))
	for i, p := range s.data.Playbooks {
		books[i] = clonePlaybook(p)
	}
	return groups, books
}

func clonePlaybook(p *Playbook) *Playbook {
	cp := *p
	cp.Steps = append([]PlaybookStep(nil), p.Steps...)
	cp.Delivery = append([]DeliveryTarget(nil), p.Delivery...)
	return &cp
}

// UpsertGroup creates or updates a tree group. A blank ID mints a new one.
// A group may not be its own parent, and its parent must already exist (or be
// root) to avoid orphaned/cyclic nodes.
func (s *PlaybookStore) UpsertGroup(g PlaybookGroup) (*PlaybookGroup, error) {
	g.Name = strings.TrimSpace(g.Name)
	if g.Name == "" {
		return nil, fmt.Errorf("group name required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if g.ID != "" && g.ParentID == g.ID {
		return nil, fmt.Errorf("group cannot be its own parent")
	}
	if g.ParentID != "" && s.findGroup(g.ParentID) == nil {
		return nil, fmt.Errorf("parent group %q not found", g.ParentID)
	}
	if g.ID != "" && s.createsGroupCycle(g.ID, g.ParentID) {
		return nil, fmt.Errorf("move would create a cycle")
	}
	var out *PlaybookGroup
	if g.ID == "" {
		g.ID = newPlaybookID()
		ng := g
		s.data.Groups = append(s.data.Groups, &ng)
		out = &ng
	} else if existing := s.findGroup(g.ID); existing != nil {
		existing.Name = g.Name
		existing.ParentID = g.ParentID
		existing.Collapsed = g.Collapsed
		out = existing
	} else {
		ng := g
		s.data.Groups = append(s.data.Groups, &ng)
		out = &ng
	}
	if err := s.save(); err != nil {
		return nil, err
	}
	cp := *out
	return &cp, nil
}

// DeleteGroup removes a group and reparents its child groups and playbooks to the
// deleted node's parent, so content is preserved rather than silently dropped.
func (s *PlaybookStore) DeleteGroup(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := s.findGroup(id)
	if target == nil {
		return fmt.Errorf("group %q not found", id)
	}
	parent := target.ParentID
	kept := s.data.Groups[:0]
	for _, g := range s.data.Groups {
		if g.ID == id {
			continue
		}
		if g.ParentID == id {
			g.ParentID = parent
		}
		kept = append(kept, g)
	}
	s.data.Groups = kept
	for _, p := range s.data.Playbooks {
		if p.GroupID == id {
			p.GroupID = parent
		}
	}
	return s.save()
}

// UpsertPlaybook creates or updates a routine. A blank ID mints a new one and
// stamps CreatedAt; an existing ID preserves CreatedAt/run stats and refreshes
// UpdatedAt. now is injected so tests are deterministic.
func (s *PlaybookStore) UpsertPlaybook(p Playbook, now time.Time) (*Playbook, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return nil, fmt.Errorf("routine name required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.GroupID != "" && s.findGroup(p.GroupID) == nil {
		return nil, fmt.Errorf("group %q not found", p.GroupID)
	}
	p.Steps = normalizeSteps(p.Steps)
	p.Delivery = normalizeDelivery(p.Delivery)
	var out *Playbook
	if p.ID == "" {
		p.ID = newPlaybookID()
		p.CreatedAt = now
		p.UpdatedAt = now
		np := clonePlaybook(&p)
		s.data.Playbooks = append(s.data.Playbooks, np)
		out = np
	} else if existing := s.findPlaybook(p.ID); existing != nil {
		p.CreatedAt = existing.CreatedAt
		p.LastRunAt = existing.LastRunAt
		p.RunCount = existing.RunCount
		p.UpdatedAt = now
		*existing = *clonePlaybook(&p)
		out = existing
	} else {
		p.CreatedAt = now
		p.UpdatedAt = now
		np := clonePlaybook(&p)
		s.data.Playbooks = append(s.data.Playbooks, np)
		out = np
	}
	if err := s.save(); err != nil {
		return nil, err
	}
	return clonePlaybook(out), nil
}

// DeletePlaybook removes a routine by ID.
func (s *PlaybookStore) DeletePlaybook(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.data.Playbooks[:0]
	found := false
	for _, p := range s.data.Playbooks {
		if p.ID == id {
			found = true
			continue
		}
		kept = append(kept, p)
	}
	if !found {
		return fmt.Errorf("routine %q not found", id)
	}
	s.data.Playbooks = kept
	return s.save()
}

// Run composes a routine into an execution prompt, records the run (LastRunAt +
// RunCount), and returns the prompt along with the backend/workDir to run it on.
// The caller creates a conversation seeded with the prompt.
func (s *PlaybookStore) Run(id string, now time.Time) (name, prompt, backend, workDir string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.findPlaybook(id)
	if p == nil {
		return "", "", "", "", fmt.Errorf("routine %q not found", id)
	}
	name = p.Name
	prompt = buildRunPrompt(p)
	backend = p.Backend
	workDir = p.WorkDir
	p.LastRunAt = now
	p.RunCount++
	if err := s.save(); err != nil {
		return "", "", "", "", err
	}
	return name, prompt, backend, workDir, nil
}

// --- helpers (lock held by caller) ---

func (s *PlaybookStore) findGroup(id string) *PlaybookGroup {
	for _, g := range s.data.Groups {
		if g.ID == id {
			return g
		}
	}
	return nil
}

func (s *PlaybookStore) findPlaybook(id string) *Playbook {
	for _, p := range s.data.Playbooks {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// createsGroupCycle reports whether reparenting group id under newParent would
// make id an ancestor of itself (walking up from newParent must not reach id).
func (s *PlaybookStore) createsGroupCycle(id, newParent string) bool {
	seen := map[string]bool{}
	for cur := newParent; cur != ""; {
		if cur == id {
			return true
		}
		if seen[cur] {
			return true // pre-existing cycle guard
		}
		seen[cur] = true
		g := s.findGroup(cur)
		if g == nil {
			return false
		}
		cur = g.ParentID
	}
	return false
}

func normalizeSteps(steps []PlaybookStep) []PlaybookStep {
	out := make([]PlaybookStep, 0, len(steps))
	for _, st := range steps {
		st.Text = strings.TrimSpace(st.Text)
		if st.Text == "" {
			continue // drop blank steps
		}
		if st.ID == "" {
			st.ID = newPlaybookID()
		}
		if st.Kind == "" {
			st.Kind = "check"
		}
		out = append(out, st)
	}
	return out
}

func normalizeDelivery(targets []DeliveryTarget) []DeliveryTarget {
	out := make([]DeliveryTarget, 0, len(targets))
	for _, d := range targets {
		d.Dest = strings.TrimSpace(d.Dest)
		d.Kind = strings.TrimSpace(d.Kind)
		if d.Dest == "" {
			continue
		}
		if d.Kind == "" {
			d.Kind = "other"
		}
		out = append(out, d)
	}
	return out
}

// buildRunPrompt turns a routine into the Korean instruction prompt the worker
// executes. Kept pure (no store state) so it is directly unit-testable. A routine
// with a natural-language Instructions body is treated as a skill: the body is
// handed to the AI verbatim to interpret and carry out. Routines that predate the
// skill model (only Steps/Delivery) fall back to the legacy composed prompt.
func buildRunPrompt(p *Playbook) string {
	if instr := strings.TrimSpace(p.Instructions); instr != "" {
		return buildSkillPrompt(p, instr)
	}
	return buildLegacyPrompt(p)
}

// buildSkillPrompt frames the natural-language body as a skill for the worker to
// interpret and execute on its own, rather than a rigid numbered checklist.
func buildSkillPrompt(p *Playbook, instr string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "저장된 업무 루틴 \"%s\"을(를) 실행합니다. 아래 [업무 내용]은 자연어로 기술된 지침입니다. 내용을 이해하고 필요한 단계로 스스로 나누어 수행한 뒤, 진행 상황과 최종 결과를 간단히 보고해 주세요.\n", p.Name)
	if d := strings.TrimSpace(p.Description); d != "" {
		fmt.Fprintf(&b, "\n[언제/무엇] %s\n", d)
	}
	if wd := strings.TrimSpace(p.WorkDir); wd != "" {
		fmt.Fprintf(&b, "[작업 위치] %s\n", wd)
	}
	b.WriteString("\n── 업무 내용 ──\n")
	b.WriteString(instr)
	b.WriteString("\n\n지침에 파일 배포·메일 발송 등 외부에 영향을 주는 작업이 포함되어 있으면, 실제로 실행하기 전에 무엇을 할지 먼저 요약해 알려주세요.")
	return strings.TrimRight(b.String(), "\n")
}

// buildLegacyPrompt composes the pre-skill structured steps/delivery form.
func buildLegacyPrompt(p *Playbook) string {
	var b strings.Builder
	fmt.Fprintf(&b, "저장된 업무 루틴 \"%s\"을(를) 실행합니다. 아래 단계를 순서대로 수행하고, 각 단계의 결과를 간단히 보고해 주세요.\n", p.Name)
	if strings.TrimSpace(p.Description) != "" {
		fmt.Fprintf(&b, "\n[설명] %s\n", strings.TrimSpace(p.Description))
	}
	if strings.TrimSpace(p.WorkDir) != "" {
		fmt.Fprintf(&b, "[작업 위치] %s\n", strings.TrimSpace(p.WorkDir))
	}
	b.WriteString("\n■ 점검·작업 단계\n")
	if len(p.Steps) == 0 {
		b.WriteString("(등록된 단계 없음)\n")
	}
	for i, st := range p.Steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, st.Text)
	}
	if len(p.Delivery) > 0 {
		b.WriteString("\n■ 완료 후 배포·전달\n")
		for _, d := range p.Delivery {
			label := deliveryLabel(d.Kind)
			line := fmt.Sprintf("- %s: %s", label, d.Dest)
			if strings.TrimSpace(d.Note) != "" {
				line += " (" + strings.TrimSpace(d.Note) + ")"
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n각 배포 대상은 실제로 실행하기 전에 어떤 작업을 할지 먼저 알려주세요.\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func deliveryLabel(kind string) string {
	switch kind {
	case "folder":
		return "공유 폴더"
	case "email":
		return "메일"
	default:
		return "전달"
	}
}

// newPlaybookID returns a short random hex id. crypto/rand mirrors the
// scheduler's id source; a failure is astronomically unlikely but falls back to
// a time-based id rather than panicking.
func newPlaybookID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("pb%d", time.Now().UnixNano())
	}
	return "pb" + hex.EncodeToString(buf[:])
}

// sortGroupsByName is a small deterministic ordering used by callers that want
// stable display order regardless of insertion order.
func sortGroupsByName(groups []*PlaybookGroup) {
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
}
