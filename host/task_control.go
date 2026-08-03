package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// Wire types + control-API glue for the scheduler (예약/작업) subsystem, mirroring
// playbook_control.go. The scheduler itself lives in scheduler.go; this file
// adapts it to the chatControlServer verb switch and builds the JSON the
// desktop/web UIs render.

// taskView augments a Task with the scheduler-computed next fire time, since
// that lives in cron's in-memory entry table, not in the persisted Task.
type taskView struct {
	*Task
	NextFire time.Time `json:"nextFire,omitempty"`
}

// buildTasksResponse lists tasks matching filter ("pending"|"paused"|"cancelled"|"all"|"").
func buildTasksResponse(bot *Bot, filter string) []taskView {
	if bot == nil || bot.scheduler == nil {
		return []taskView{}
	}
	tasks := bot.scheduler.ListTasks(filter)
	out := make([]taskView, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, taskView{Task: t, NextFire: bot.scheduler.NextFire(t.ID)})
	}
	return out
}

func (s *chatControlServer) pauseTask(id string) json.RawMessage {
	if s.bot == nil || s.bot.scheduler == nil {
		return mutateResult(fmt.Errorf("scheduler unavailable"))
	}
	return mutateResult(s.bot.scheduler.PauseTask(id))
}

func (s *chatControlServer) resumeTask(id string) json.RawMessage {
	if s.bot == nil || s.bot.scheduler == nil {
		return mutateResult(fmt.Errorf("scheduler unavailable"))
	}
	return mutateResult(s.bot.scheduler.ResumeTask(id))
}

func (s *chatControlServer) cancelTask(id string) json.RawMessage {
	if s.bot == nil || s.bot.scheduler == nil {
		return mutateResult(fmt.Errorf("scheduler unavailable"))
	}
	return mutateResult(s.bot.scheduler.CancelTask(id))
}

// saveTask upserts a task (reminder/recurring job) from the raw JSON payload and
// returns the saved entity (with its assigned id) so the client can reconcile
// without a refetch. fallbackChatID (the requester's resolved chat, usually the
// owner) is used for new tasks that don't specify one — the desktop/web editor
// has no notion of a telegram chat id, so it never sends this field.
func (s *chatControlServer) saveTask(payload json.RawMessage, fallbackChatID int64) json.RawMessage {
	if s.bot == nil || s.bot.scheduler == nil {
		return mutateResult(fmt.Errorf("scheduler unavailable"))
	}
	var t Task
	if err := json.Unmarshal(payload, &t); err != nil {
		return mutateResult(fmt.Errorf("invalid task payload: %w", err))
	}
	if t.ChatID == 0 {
		t.ChatID = fallbackChatID
	}
	saved, err := s.bot.scheduler.UpsertTask(&t)
	if err != nil {
		return mutateResult(err)
	}
	b, _ := json.Marshal(map[string]any{"ok": true, "task": taskView{Task: saved, NextFire: s.bot.scheduler.NextFire(saved.ID)}})
	return b
}

func (s *chatControlServer) deleteTask(id string) json.RawMessage {
	if s.bot == nil || s.bot.scheduler == nil {
		return mutateResult(fmt.Errorf("scheduler unavailable"))
	}
	return mutateResult(s.bot.scheduler.DeleteTask(id))
}
