package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// Wire types + control-API glue for the playbook (업무 관리) subsystem. The store
// itself lives in playbook.go; this file adapts it to the chatControlServer verb
// switch and builds the JSON the UIs render.

// webPlaybooksResponse is the payload for the playbook_list verb. The tree is
// flat (groups carry parentId; the client nests them), matching how the desktop
// already reconstructs its conversation groups client-side.
type webPlaybooksResponse struct {
	Groups    []*PlaybookGroup `json:"groups"`
	Playbooks []*Playbook      `json:"playbooks"`
}

func buildPlaybooksResponse(store *PlaybookStore) webPlaybooksResponse {
	if store == nil {
		return webPlaybooksResponse{Groups: []*PlaybookGroup{}, Playbooks: []*Playbook{}}
	}
	groups, books := store.Snapshot()
	sortGroupsByName(groups)
	return webPlaybooksResponse{Groups: groups, Playbooks: books}
}

// mutateResult marshals the standard {ok,error} reply shared by the delete verbs.
func mutateResult(err error) json.RawMessage {
	out := map[string]any{"ok": err == nil}
	if err != nil {
		out["error"] = err.Error()
	}
	b, _ := json.Marshal(out)
	return b
}

func deletePlaybook(store *PlaybookStore, id string) error {
	if store == nil {
		return fmt.Errorf("playbooks unavailable")
	}
	return store.DeletePlaybook(id)
}

func deletePlaybookGroup(store *PlaybookStore, id string) error {
	if store == nil {
		return fmt.Errorf("playbooks unavailable")
	}
	return store.DeleteGroup(id)
}

// savePlaybook upserts a routine from the raw JSON payload and returns the saved
// entity (with its assigned id) so the client can reconcile without a refetch.
func (s *chatControlServer) savePlaybook(payload json.RawMessage) json.RawMessage {
	if s.bot == nil || s.bot.playbooks == nil {
		return mutateResult(fmt.Errorf("playbooks unavailable"))
	}
	var p Playbook
	if err := json.Unmarshal(payload, &p); err != nil {
		return mutateResult(fmt.Errorf("invalid playbook payload: %w", err))
	}
	saved, err := s.bot.playbooks.UpsertPlaybook(p, time.Now())
	if err != nil {
		return mutateResult(err)
	}
	b, _ := json.Marshal(map[string]any{"ok": true, "playbook": saved})
	return b
}

func (s *chatControlServer) savePlaybookGroup(payload json.RawMessage) json.RawMessage {
	if s.bot == nil || s.bot.playbooks == nil {
		return mutateResult(fmt.Errorf("playbooks unavailable"))
	}
	var g PlaybookGroup
	if err := json.Unmarshal(payload, &g); err != nil {
		return mutateResult(fmt.Errorf("invalid group payload: %w", err))
	}
	saved, err := s.bot.playbooks.UpsertGroup(g)
	if err != nil {
		return mutateResult(err)
	}
	b, _ := json.Marshal(map[string]any{"ok": true, "group": saved})
	return b
}

// runPlaybook composes a routine into a prompt, creates a fresh web conversation
// (named after the routine), applies its backend/workDir when set, and dispatches
// the prompt so the worker actually performs the routine. Returns the new
// conversation id for the client to switch to.
func (b *Bot) runPlaybook(chatID int64, id string) (string, error) {
	if b.playbooks == nil {
		return "", fmt.Errorf("playbooks unavailable")
	}
	name, prompt, backend, workDir, err := b.playbooks.Run(id, time.Now())
	if err != nil {
		return "", err
	}
	conv, err := b.store.NewWebConv("▶ " + name)
	if err != nil {
		return "", fmt.Errorf("새 대화 생성 실패: %w", err)
	}
	tgt := WebTarget(conv.ID)
	// Apply the recorded working directory (only if it still resolves) and backend
	// before dispatch, so the very first turn runs in the right place. Failures are
	// non-fatal: the routine still runs in the default context.
	if workDir != "" {
		if verr := validateDir(workDir); verr == nil {
			conv.WorkDir = workDir
			_ = b.store.UpdateWebConv(conv)
		}
	}
	if backend != "" {
		_ = b.setChannelBackend(tgt, backend)
	}
	_ = b.store.SetActive("", conv.ID)
	go b.dispatchTargeted(chatID, prompt, &tgt)
	return conv.ID, nil
}
