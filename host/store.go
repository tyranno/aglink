package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Design Ref: §4.1 StoreRepo, §3.4 store.json. Infrastructure layer.

// storeSchemaVersion is bumped whenever StoreData's shape changes in a way that
// isn't safely forward-compatible. On mismatch, Load backs up the old file and
// starts fresh rather than migrating — see Load for rationale.
const storeSchemaVersion = 3

// fileStore is a JSON-file backed StoreRepo (MVP). Safe for concurrent use.
type fileStore struct {
	path string
	mu   sync.Mutex
	data StoreData
}

// NewFileStore creates a store backed by the given JSON file path.
func NewFileStore(path string) *fileStore {
	return &fileStore{path: path, data: newEmptyStore()}
}

// Load reads store.json. A missing file is treated as an empty store.
func (s *fileStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.data = newEmptyStore()
			return nil
		}
		return err
	}
	var d StoreData
	if err := json.Unmarshal(b, &d); err != nil {
		return fmt.Errorf("store.json 파싱 실패: %w", err)
	}
	// Schema mismatch (or legacy file with no version) → back up once and reset.
	// Migration is intentionally not supported: old conversations are discarded.
	if d.SchemaVersion != storeSchemaVersion {
		if berr := os.Rename(s.path, s.path+".bak"); berr != nil {
			log.Printf("[store] legacy backup failed: %v (starting fresh anyway)", berr)
		} else {
			log.Printf("[store] legacy store.json (schema %d) backed up to %s.bak; starting fresh (schema %d)", d.SchemaVersion, s.path, storeSchemaVersion)
		}
		s.data = newEmptyStore()
		return s.saveLocked()
	}
	if d.Projects == nil {
		d.Projects = map[string]*Project{}
	}
	for _, p := range d.Projects {
		if p.Conversations == nil {
			p.Conversations = map[string]*Conversation{}
		}
	}
	if d.WebConvs == nil {
		d.WebConvs = map[string]*Conversation{}
	}
	s.data = d
	return nil
}

func newEmptyStore() StoreData {
	return StoreData{SchemaVersion: storeSchemaVersion, Projects: map[string]*Project{}, WebConvs: map[string]*Conversation{}}
}

// Save writes store.json atomically (temp file + rename).
func (s *fileStore) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *fileStore) saveLocked() error {
	s.data.SchemaVersion = storeSchemaVersion
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

// clone returns a copy a caller can read and mutate without touching the store.
// Only History is a reference; everything else is a value.
func (c *Conversation) clone() *Conversation {
	if c == nil {
		return nil
	}
	cp := *c
	if c.History != nil {
		cp.History = make([]ConversationTurn, len(c.History))
		copy(cp.History, c.History)
	}
	return &cp
}

// clone deep-copies a project, including every conversation in it.
func (p *Project) clone() *Project {
	if p == nil {
		return nil
	}
	cp := Project{Path: p.Path}
	if p.Conversations != nil {
		cp.Conversations = make(map[string]*Conversation, len(p.Conversations))
		for id, c := range p.Conversations {
			cp.Conversations[id] = c.clone()
		}
	}
	return &cp
}

// The read accessors below hand back copies. They used to return the store's own
// objects, so the lock they take protected nothing: a caller read (or appended
// to) a conversation's History, or ranged over a project's Conversations map,
// while a writer mutated the very same object under the lock. Ranging over a map
// that another goroutine writes is fatal to the process, not merely wrong.
//
// Mutating a copy and handing it to the matching Update* is the intended flow,
// and still works: Update* stores what it is given.

// ListProjects returns a deep copy of the project map.
func (s *fileStore) ListProjects() map[string]*Project {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]*Project, len(s.data.Projects))
	for name, p := range s.data.Projects {
		out[name] = p.clone()
	}
	return out
}

// AddProject registers a directory under a name. The path must exist and be a directory.
func (s *fileStore) AddProject(name, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if name == "" {
		return fmt.Errorf("프로젝트 이름이 비어 있습니다")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("경로 변환 실패: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("디렉토리가 존재하지 않습니다: %s", abs)
	}
	if _, exists := s.data.Projects[name]; exists {
		return fmt.Errorf("이미 등록된 프로젝트입니다: %s", name)
	}
	s.data.Projects[name] = &Project{Path: abs, Conversations: map[string]*Conversation{}}
	return s.saveLocked()
}

// RemoveProject deletes a project (and its conversation metadata).
func (s *fileStore) RemoveProject(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Projects[name]; !ok {
		return fmt.Errorf("프로젝트를 찾을 수 없습니다: %s", name)
	}
	delete(s.data.Projects, name)
	if s.data.Active.Project == name {
		s.data.Active = ActiveRef{}
	}
	if s.data.TelegramActiveProject == name {
		s.data.TelegramActiveProject = ""
	}
	return s.saveLocked()
}

// GetProject returns a project by name.
func (s *fileStore) GetProject(name string) (*Project, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.data.Projects[name]
	return p.clone(), ok
}

// NewConversation creates a conversation in a project, assigning a numeric ID and a session UUID.
func (s *fileStore) NewConversation(project, title, origin string) (*Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.data.Projects[project]
	if !ok {
		return nil, fmt.Errorf("프로젝트를 찾을 수 없습니다: %s", project)
	}
	id := nextConvID(p.Conversations)
	if title == "" {
		title = "대화 " + id
	}
	c := &Conversation{
		ID:           id,
		Title:        title,
		SessionID:    newUUID(),
		Started:      false,
		LastActivity: time.Now().UTC(),
		Origin:       origin,
	}
	p.Conversations[id] = c
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return c, nil
}

// nextConvID returns the smallest unused positive integer ID as a string.
func nextConvID(convs map[string]*Conversation) string {
	max := 0
	for k := range convs {
		if n, err := strconv.Atoi(k); err == nil && n > max {
			max = n
		}
	}
	return strconv.Itoa(max + 1)
}

// GetConversation returns a conversation within a project.
func (s *fileStore) GetConversation(project, convID string) (*Conversation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.data.Projects[project]
	if !ok {
		return nil, false
	}
	c, ok := p.Conversations[convID]
	return c.clone(), ok
}

// UpdateConversation persists changes to a conversation.
func (s *fileStore) UpdateConversation(project string, c *Conversation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.data.Projects[project]
	if !ok {
		return fmt.Errorf("프로젝트를 찾을 수 없습니다: %s", project)
	}
	p.Conversations[c.ID] = c
	return s.saveLocked()
}

// SetActive records the active project/conversation pointer.
func (s *fileStore) SetActive(project, convID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Active = ActiveRef{Project: project, ConversationID: convID}
	return s.saveLocked()
}

// GetActive returns the active pointer.
func (s *fileStore) GetActive() ActiveRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Active
}

// sortedConvIDsByActivity returns IDs sorted by LastActivity descending (most recent first).
func sortedConvIDsByActivity(convs map[string]*Conversation) []string {
	ids := make([]string, 0, len(convs))
	for k := range convs {
		ids = append(ids, k)
	}
	sort.Slice(ids, func(i, j int) bool {
		return convs[ids[i]].LastActivity.After(convs[ids[j]].LastActivity)
	})
	return ids
}

// sortedConvIDs returns conversation IDs in numeric order (helper for listings).
func sortedConvIDs(convs map[string]*Conversation) []string {
	ids := make([]string, 0, len(convs))
	for k := range convs {
		ids = append(ids, k)
	}
	sort.Slice(ids, func(i, j int) bool {
		ni, _ := strconv.Atoi(ids[i])
		nj, _ := strconv.Atoi(ids[j])
		return ni < nj
	})
	return ids
}

// GetStoredBackend returns the persisted backend name ("claude"|"codex"; "" = claude).
func (s *fileStore) GetStoredBackend() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.ActiveBackend
}

// SetStoredBackend persists the active backend to store.json.
func (s *fileStore) SetStoredBackend(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.ActiveBackend = name
	return s.saveLocked()
}

// PruneOldConversations removes conversations whose LastActivity is older than
// ttlDays. It never prunes the active conversation OR any ancestor in its
// continuation chain (walking ParentID upward), so pruning can never sever the
// live conversation's context lineage. ttlDays <= 0 disables pruning
// (returns 0, nil). Returns the number of conversations removed.
func (s *fileStore) PruneOldConversations(ttlDays int) (int, error) {
	if ttlDays <= 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Protect the active conversation and its ancestor chain from pruning.
	protected := map[string]bool{}
	if ap := s.data.Projects[s.data.Active.Project]; ap != nil {
		for id := s.data.Active.ConversationID; id != ""; {
			if protected[id] {
				break // cycle guard
			}
			protected[id] = true
			c, ok := ap.Conversations[id]
			if !ok {
				break
			}
			id = c.ParentID
		}
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -ttlDays)
	removed := 0
	for projName, p := range s.data.Projects {
		for id, c := range p.Conversations {
			if projName == s.data.Active.Project && protected[id] {
				continue
			}
			if c.LastActivity.Before(cutoff) {
				delete(p.Conversations, id)
				removed++
			}
		}
	}
	if removed == 0 {
		return 0, nil
	}
	return removed, s.saveLocked()
}

// GetParent returns the parent conversation in a chain (used for continuation context).
func (s *fileStore) GetParent(project, convID string) (*Conversation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.data.Projects[project]
	if !ok {
		return nil, false
	}
	c, ok := p.Conversations[convID]
	if !ok || c.ParentID == "" {
		return nil, false
	}
	parent, ok := p.Conversations[c.ParentID]
	return parent, ok
}

// TelegramConversation returns the single global telegram conversation, creating
// it on first access. It is project-independent; the working directory for a
// telegram turn comes from TelegramActiveProject.
func (s *fileStore) TelegramConversation() *Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.TelegramConv == nil {
		s.data.TelegramConv = &Conversation{
			ID:           "telegram",
			Title:        "텔레그램 대화",
			SessionID:    newUUID(),
			Started:      false,
			LastActivity: time.Now().UTC(),
			Origin:       OriginTelegram,
		}
		_ = s.saveLocked()
	}
	return s.data.TelegramConv.clone()
}

// UpdateTelegramConversation persists changes to the global telegram conversation.
func (s *fileStore) UpdateTelegramConversation(c *Conversation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.TelegramConv = c
	return s.saveLocked()
}

// TelegramActiveProject returns the project currently targeted by telegram turns.
func (s *fileStore) TelegramActiveProject() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.TelegramActiveProject
}

// SetTelegramActiveProject records the project telegram turns should run against.
func (s *fileStore) SetTelegramActiveProject(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.TelegramActiveProject = name
	return s.saveLocked()
}

// HistorySnapshot returns a copy of the target conversation's turns taken
// under the store lock, so callers can read history without racing a worker
// that is appending to the live conversation.
func (s *fileStore) HistorySnapshot(tgt Target) []ConversationTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	var conv *Conversation
	switch {
	case tgt.Kind == "telegram":
		conv = s.data.TelegramConv
	case tgt.Kind == "web" && tgt.Project == "":
		conv = s.data.WebConvs[tgt.ID]
	default: // legacy project-scoped topic (kept for safety)
		if p, ok := s.data.Projects[tgt.Project]; ok {
			conv = p.Conversations[tgt.ID]
		}
	}
	if conv == nil {
		return nil
	}
	out := make([]ConversationTurn, len(conv.History))
	copy(out, conv.History)
	return out
}

// NewWebConv creates a top-level, project-independent web conversation.
func (s *fileStore) NewWebConv(title string) (*Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.WebConvs == nil {
		s.data.WebConvs = map[string]*Conversation{}
	}
	id := nextConvID(s.data.WebConvs)
	if title == "" {
		title = "웹 대화 " + id
	}
	c := &Conversation{
		ID:           id,
		Title:        title,
		SessionID:    newUUID(),
		Started:      false,
		LastActivity: time.Now().UTC(),
		Origin:       OriginWeb,
	}
	s.data.WebConvs[id] = c
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return c, nil
}

// GetWebConv returns a top-level web conversation by ID.
func (s *fileStore) GetWebConv(id string) (*Conversation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.data.WebConvs[id]
	return c.clone(), ok
}

// UpdateWebConv persists changes to a top-level web conversation.
func (s *fileStore) UpdateWebConv(c *Conversation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.WebConvs == nil {
		s.data.WebConvs = map[string]*Conversation{}
	}
	s.data.WebConvs[c.ID] = c
	return s.saveLocked()
}

// ListWebConvs returns a deep copy of the top-level web conversation map.
func (s *fileStore) ListWebConvs() map[string]*Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]*Conversation, len(s.data.WebConvs))
	for id, c := range s.data.WebConvs {
		out[id] = c.clone()
	}
	return out
}

// DeleteWebConv removes a top-level web conversation, clearing Active if it
// pointed at the deleted conversation.
func (s *fileStore) DeleteWebConv(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.WebConvs, id)
	if s.data.Active.ConversationID == id && s.data.Active.Project == "" {
		s.data.Active = ActiveRef{}
	}
	return s.saveLocked()
}
