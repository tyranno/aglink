package main

// Delivery and control-API side of checkpoint.go: the "changes" frame that
// puts a turn's change card under its answer, the checkpoint_diff /
// checkpoint_restore / list_files requests, and the project file list behind
// the desktop composer's @file completion.

import (
	"context"
	"encoding/json"
	"io/fs"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// changesSender is the MessageSender-side view (target bound) runWorker
// type-asserts; boundSender implements it. Other senders just don't show cards.
type changesSender interface {
	Changes(chatID int64, ch *TurnChanges)
}

// changesChannel is the optional ChannelSender extension. Telegram has none.
type changesChannel interface {
	Changes(tgt Target, chatID int64, ch *TurnChanges)
}

func (h *Hub) Changes(tgt Target, chatID int64, c *TurnChanges) {
	for _, ch := range h.targets(tgt, chatID) {
		if cc, ok := ch.(changesChannel); ok {
			cc.Changes(tgt, chatID, c)
		}
	}
}

func (s boundSender) Changes(chatID int64, c *TurnChanges) { s.hub.Changes(s.tgt, chatID, c) }

// Changes sends a "changes" frame: Text carries the TurnChanges JSON. Older
// clients ignore the unknown type.
func (r *remoteChatChannel) Changes(tgt Target, _ int64, c *TurnChanges) {
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	r.push(controlOut{Kind: "frame", Frame: &wsFrame{Type: "changes", Text: string(b), At: time.Now().UnixMilli(), Target: &tgt}})
}

func handleCheckpointRequest(m controlIn) json.RawMessage {
	out := map[string]any{}
	switch m.Type {
	case "checkpoint_diff":
		diff, truncated, err := CheckpointDiff(m.Path, m.From, m.To, m.Text)
		if err != nil {
			out["error"] = err.Error()
		} else {
			out["diff"], out["truncated"] = diff, truncated
		}
	case "checkpoint_restore":
		undo, n, err := CheckpointRestore(m.Path, m.To)
		out["ok"] = err == nil
		out["undo"], out["changed"] = undo, n
		if err != nil {
			out["error"] = err.Error()
		}
	case "list_files":
		files, err := ListProjectFiles(m.Path, m.Text, m.Limit)
		if err != nil {
			out["error"] = err.Error()
		}
		out["files"] = files
	}
	b, _ := json.Marshal(out)
	return b
}

const (
	maxListedFiles = 30000
	fileListTTL    = 30 * time.Second
)

type fileListEntry struct {
	at    time.Time
	files []string
}

var (
	fileListMu    sync.Mutex
	fileListCache = map[string]fileListEntry{}
)

// projectFiles lists workDir's files (slash paths, relative). A git repo uses
// `git ls-files` (tracked + untracked, honoring .gitignore); anything else is
// walked with the checkpoint excludes. Cached briefly — the composer asks on
// every keystroke after "@".
func projectFiles(workDir string) ([]string, error) {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	key := strings.ToLower(abs)
	fileListMu.Lock()
	if e, ok := fileListCache[key]; ok && time.Since(e.at) < fileListTTL {
		fileListMu.Unlock()
		return e.files, nil
	}
	fileListMu.Unlock()

	var files []string
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-c", "core.quotepath=false", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Dir = abs
	if b, gerr := cmd.Output(); gerr == nil && isGitRepoAt(ctx, abs) {
		for _, p := range strings.Split(string(b), "\x00") {
			if p != "" {
				files = append(files, p)
			}
			if len(files) >= maxListedFiles {
				break
			}
		}
	} else {
		skipDir := map[string]bool{}
		for _, e := range checkpointExcludes {
			if strings.HasSuffix(e, "/") {
				skipDir[strings.TrimSuffix(e, "/")] = true
			}
		}
		_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
			if err != nil || ctx.Err() != nil {
				return filepath.SkipDir
			}
			if d.IsDir() {
				if p != abs && (skipDir[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if rel, err := filepath.Rel(abs, p); err == nil {
				files = append(files, filepath.ToSlash(rel))
			}
			if len(files) >= maxListedFiles {
				return fs.SkipAll
			}
			return nil
		})
	}
	sort.Strings(files)
	fileListMu.Lock()
	fileListCache[key] = fileListEntry{at: time.Now(), files: files}
	fileListMu.Unlock()
	return files, nil
}

func isGitRepoAt(ctx context.Context, dir string) bool {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	b, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(b)) == "true"
}

// ListProjectFiles returns up to limit files matching query, best first:
// basename prefix, then basename contains, then path contains, then
// subsequence match. Empty query → most shallow files first.
func ListProjectFiles(workDir, query string, limit int) ([]string, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if !checkpointEligible(workDir, "") {
		return []string{}, nil
	}
	all, err := projectFiles(workDir)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(query), "\\", "/"))
	type scored struct {
		p     string
		score int
	}
	var hits []scored
	for _, p := range all {
		lp := strings.ToLower(p)
		base := lp[strings.LastIndex(lp, "/")+1:]
		s := -1
		switch {
		case q == "":
			s = 100 - strings.Count(lp, "/")*10
		case strings.HasPrefix(base, q):
			s = 400
		case strings.Contains(base, q):
			s = 300
		case strings.Contains(lp, q):
			s = 200
		case subsequence(lp, q):
			s = 100
		}
		if s < 0 {
			continue
		}
		hits = append(hits, scored{p, s - len(p)/8})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := []string{}
	for i := 0; i < len(hits) && i < limit; i++ {
		out = append(out, hits[i].p)
	}
	return out, nil
}

func subsequence(s, q string) bool {
	qr := []rune(q)
	i := 0
	for _, c := range s {
		if i < len(qr) && c == qr[i] {
			i++
		}
	}
	return i == len(qr)
}
