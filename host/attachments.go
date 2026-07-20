package main

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// maxAttachments caps how many files are retained in <data dir>/attachments.
// Each time a new attachment is ingested, older files (by mtime) beyond this
// many are deleted so the directory can't grow without bound. Both the Telegram
// download path and the aglink-chat upload relay funnel through ingestAttachment,
// so pruning there covers every save. Trade-off: a history entry that references
// a pruned file becomes a dangling link — accepted vs unbounded growth.
const maxAttachments = 100

// pruneAttachments keeps the newest `keep` files in dir (by mtime) and removes
// the rest. Best-effort: any error is logged, never fatal — pruning must never
// break attachment ingestion. Subdirectories are ignored.
func pruneAttachments(dir string, keep int) {
	if keep <= 0 || dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type fileMod struct {
		path string
		mod  time.Time
	}
	files := make([]fileMod, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileMod{path: filepath.Join(dir, e.Name()), mod: info.ModTime()})
	}
	if len(files) <= keep {
		return
	}
	// Newest first, then delete everything past `keep`.
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for _, f := range files[keep:] {
		if err := os.Remove(f.path); err != nil {
			log.Printf("[bot] attachment prune: remove %s: %v", f.path, err)
		}
	}
}
