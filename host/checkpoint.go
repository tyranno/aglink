package main

// Per-turn file checkpoints: before a turn runs, the conversation's working
// folder is snapshotted into a private "shadow" git repository under
// ~/.aglink/checkpoints/<hash>/ (GIT_DIR there, GIT_WORK_TREE = the folder), and
// again after the turn. The diff between the two becomes the turn's change card
// in the desktop (files changed, +/− lines, view diff, restore to before).
//
// The user's own repository is never touched: no commits, no index, no stash —
// the shadow repo has its own index and object store, and the folder's
// .gitignore files still apply because git reads them from the work tree.
// Folders that are too broad (the home dir, a drive root) or too slow to
// snapshot are skipped.

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TurnChanges is what one turn changed in its working folder. Stored on the
// ConversationTurn and sent to the desktop as a "changes" frame.
type TurnChanges struct {
	WorkDir   string        `json:"workDir"`
	Before    string        `json:"before"` // shadow commit before the turn
	After     string        `json:"after"`  // shadow commit after the turn
	Files     []ChangedFile `json:"files"`
	Truncated bool          `json:"truncated,omitempty"` // more files than maxChangedFiles
}

// ChangedFile is one path in a TurnChanges. Status is git's letter (A/M/D).
// Add/Del are -1 for binary files.
type ChangedFile struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Add    int    `json:"add"`
	Del    int    `json:"del"`
}

const (
	maxChangedFiles      = 200
	maxCheckpointDiff    = 400 << 10 // bytes of unified diff returned to the client
	checkpointSnapTimout = 25 * time.Second
)

// checkpointExcludes are skipped on top of the folder's own .gitignore files:
// dependency/build output that is big, regenerable and never what a user
// means by "the files the AI changed".
var checkpointExcludes = []string{
	".git/", ".aglink/", "node_modules/", ".venv/", "venv/", "__pycache__/",
	".next/", ".nuxt/", ".gradle/", ".idea/", ".vs/", ".vscode-test/",
	"target/", "obj/", ".cache/", "*.log", "*.tmp", "Thumbs.db", ".DS_Store",
}

var (
	checkpointMu    sync.Mutex
	checkpointLocks = map[string]*sync.Mutex{} // per shadow repo
	checkpointSkip  = map[string]bool{}        // folders that timed out once
)

func checkpointLock(key string) *sync.Mutex {
	checkpointMu.Lock()
	defer checkpointMu.Unlock()
	l := checkpointLocks[key]
	if l == nil {
		l = &sync.Mutex{}
		checkpointLocks[key] = l
	}
	return l
}

// checkpointRepoDir is the shadow GIT_DIR for workDir.
func checkpointRepoDir(workDir string) (string, error) {
	base, err := dataDir()
	if err != nil {
		return "", err
	}
	key := strings.ToLower(filepath.Clean(workDir))
	sum := sha1.Sum([]byte(key))
	return filepath.Join(base, "checkpoints", hex.EncodeToString(sum[:8])), nil
}

// checkpointEligible rejects folders that are too broad to snapshot.
func checkpointEligible(workDir, home string) bool {
	if workDir == "" {
		return false
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return false
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return false
	}
	clean := strings.ToLower(filepath.Clean(abs))
	if home != "" && clean == strings.ToLower(filepath.Clean(home)) {
		return false
	}
	if uh, err := os.UserHomeDir(); err == nil && clean == strings.ToLower(filepath.Clean(uh)) {
		return false
	}
	// Drive root / filesystem root.
	if filepath.Dir(clean) == clean {
		return false
	}
	return true
}

type shadowRepo struct {
	gitDir  string
	workDir string
}

func (r shadowRepo) git(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	full := append([]string{
		"--git-dir=" + r.gitDir, "--work-tree=" + r.workDir,
		"--literal-pathspecs",
		"-c", "core.autocrlf=false", "-c", "core.safecrlf=false",
		"-c", "core.quotepath=false", "-c", "core.longpaths=true",
		"-c", "user.name=aglink", "-c", "user.email=aglink@localhost",
		"-c", "commit.gpgsign=false", "-c", "gc.auto=0",
	}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = r.workDir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return out.Bytes(), fmt.Errorf("git %s: %w %s", args[0], err, msg)
	}
	return out.Bytes(), nil
}

func openShadowRepo(ctx context.Context, workDir string) (shadowRepo, error) {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return shadowRepo{}, err
	}
	dir, err := checkpointRepoDir(abs)
	if err != nil {
		return shadowRepo{}, err
	}
	r := shadowRepo{gitDir: dir, workDir: abs}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		return r, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return r, err
	}
	if _, err := r.git(ctx, nil, "init", "-q"); err != nil {
		return r, err
	}
	excl := strings.Join(checkpointExcludes, "\n") + "\n"
	_ = os.MkdirAll(filepath.Join(dir, "info"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, "info", "exclude"), []byte(excl), 0o644); err != nil {
		return r, err
	}
	// Remember which folder this repo belongs to (for humans / cleanup).
	_ = os.WriteFile(filepath.Join(dir, "aglink-workdir.txt"), []byte(abs+"\n"), 0o644)
	return r, nil
}

// snapshot records the folder's current state and returns its commit. When
// nothing changed since the last snapshot the previous commit is returned
// instead of piling up empty commits.
//
// strict is for the backup taken right before a restore: there a file git
// could not read (locked, no permission) would be missing from the backup and
// the restore could not be undone for it, so the whole snapshot fails. The
// per-turn checkpoints are lenient instead — one locked database or
// root-owned file must not switch checkpoints off for the whole folder — and
// keep everything git could read.
func (r shadowRepo) snapshot(ctx context.Context, msg string, strict bool) (string, error) {
	if strict {
		if _, err := r.git(ctx, nil, "add", "-A", "."); err != nil {
			return "", err
		}
	} else if _, err := r.git(ctx, nil, "add", "-A", "--ignore-errors", "."); err != nil {
		// --ignore-errors still exits non-zero on unreadable files; the index
		// holds everything else, so carry on (write-tree fails by itself when
		// the index could not be updated at all, e.g. a stale index.lock).
		if ctx.Err() != nil {
			return "", err
		}
		logCheckpoint("snapshot %s: some files skipped: %v", r.workDir, err)
	}
	treeOut, err := r.git(ctx, nil, "write-tree")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(string(treeOut))
	head, herr := r.git(ctx, nil, "rev-parse", "-q", "--verify", "HEAD")
	headSHA := strings.TrimSpace(string(head))
	if herr == nil && headSHA != "" {
		if ht, err := r.git(ctx, nil, "rev-parse", "HEAD^{tree}"); err == nil && strings.TrimSpace(string(ht)) == tree {
			return headSHA, nil
		}
	}
	args := []string{"commit-tree", tree, "-m", msg}
	if headSHA != "" && herr == nil {
		args = append(args, "-p", headSHA)
	}
	c, err := r.git(ctx, nil, args...)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(c))
	if _, err := r.git(ctx, nil, "update-ref", "HEAD", commit); err != nil {
		return "", err
	}
	return commit, nil
}

// Checkpoint takes a snapshot of workDir. Returns "" (no error) when the
// folder isn't eligible, git is missing, or the folder proved too slow.
func Checkpoint(workDir, home, msg string) string {
	if !checkpointEligible(workDir, home) {
		return ""
	}
	key := strings.ToLower(filepath.Clean(workDir))
	checkpointMu.Lock()
	skip := checkpointSkip[key]
	checkpointMu.Unlock()
	if skip {
		return ""
	}
	l := checkpointLock(key)
	l.Lock()
	defer l.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), checkpointSnapTimout)
	defer cancel()
	r, err := openShadowRepo(ctx, workDir)
	if err != nil {
		logCheckpoint("open %s: %v", workDir, err)
		return ""
	}
	sha, err := r.snapshot(ctx, msg, false)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			checkpointMu.Lock()
			checkpointSkip[key] = true
			checkpointMu.Unlock()
			logCheckpoint("snapshot %s too slow, checkpoints off for this folder", workDir)
		} else {
			logCheckpoint("snapshot %s: %v", workDir, err)
		}
		return ""
	}
	return sha
}

// diffChanges lists the files that differ between two snapshots.
func diffChanges(ctx context.Context, r shadowRepo, from, to string) ([]ChangedFile, bool, error) {
	ns, err := r.git(ctx, nil, "diff", "--no-renames", "--name-status", "-z", from, to)
	if err != nil {
		return nil, false, err
	}
	parts := strings.Split(strings.TrimRight(string(ns), "\x00"), "\x00")
	var files []ChangedFile
	idx := map[string]int{}
	for i := 0; i+1 < len(parts); i += 2 {
		st, p := parts[i], parts[i+1]
		if st == "" || p == "" {
			continue
		}
		idx[p] = len(files)
		files = append(files, ChangedFile{Path: p, Status: st[:1]})
	}
	num, err := r.git(ctx, nil, "diff", "--no-renames", "--numstat", "-z", from, to)
	if err == nil {
		for _, rec := range strings.Split(string(num), "\x00") {
			f := strings.SplitN(rec, "\t", 3)
			if len(f) != 3 {
				continue
			}
			i, ok := idx[f[2]]
			if !ok {
				continue
			}
			if f[0] == "-" {
				files[i].Add, files[i].Del = -1, -1
				continue
			}
			files[i].Add, _ = strconv.Atoi(f[0])
			files[i].Del, _ = strconv.Atoi(f[1])
		}
	}
	truncated := false
	if len(files) > maxChangedFiles {
		files, truncated = files[:maxChangedFiles], true
	}
	return files, truncated, nil
}

// TurnChangesBetween builds the change card for a turn, or nil when nothing
// changed (or either snapshot is missing).
func TurnChangesBetween(workDir, before, after string) *TurnChanges {
	if before == "" || after == "" || before == after {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkpointSnapTimout)
	defer cancel()
	r, err := openShadowRepo(ctx, workDir)
	if err != nil {
		return nil
	}
	files, truncated, err := diffChanges(ctx, r, before, after)
	if err != nil || len(files) == 0 {
		if err != nil {
			logCheckpoint("diff %s: %v", workDir, err)
		}
		return nil
	}
	return &TurnChanges{WorkDir: r.workDir, Before: before, After: after, Files: files, Truncated: truncated}
}

var shaRe = func(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// existingShadowRepo opens the shadow repo for workDir only if one was already
// created by a checkpoint — client requests never create repos.
func existingShadowRepo(ctx context.Context, workDir string) (shadowRepo, error) {
	if workDir == "" {
		return shadowRepo{}, errors.New("작업 폴더가 없습니다")
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return shadowRepo{}, err
	}
	dir, err := checkpointRepoDir(abs)
	if err != nil {
		return shadowRepo{}, err
	}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		return shadowRepo{}, errors.New("이 폴더의 체크포인트가 없습니다")
	}
	return shadowRepo{gitDir: dir, workDir: abs}, nil
}

// CheckpointDiff returns the unified diff between two snapshots, optionally for
// one path. Truncated to maxCheckpointDiff bytes.
func CheckpointDiff(workDir, from, to, path string) (string, bool, error) {
	if !shaRe(from) || !shaRe(to) {
		return "", false, errors.New("잘못된 체크포인트입니다")
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkpointSnapTimout)
	defer cancel()
	r, err := existingShadowRepo(ctx, workDir)
	if err != nil {
		return "", false, err
	}
	args := []string{"diff", "--no-renames", "--no-color", "-U3", from, to}
	if path != "" {
		args = append(args, "--", path)
	}
	out, err := r.git(ctx, nil, args...)
	if err != nil {
		return "", false, err
	}
	if len(out) > maxCheckpointDiff {
		return string(out[:maxCheckpointDiff]), true, nil
	}
	return string(out), false, nil
}

// CheckpointRestore puts workDir back to snapshot `to`. The current state is
// snapshotted first and returned, so the restore itself can be undone by
// restoring to that commit. Only files that differ are touched.
func CheckpointRestore(workDir, to string) (undo string, changed int, err error) {
	if !shaRe(to) {
		return "", 0, errors.New("잘못된 체크포인트입니다")
	}
	key := strings.ToLower(filepath.Clean(workDir))
	l := checkpointLock(key)
	l.Lock()
	defer l.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*checkpointSnapTimout)
	defer cancel()
	r, err := existingShadowRepo(ctx, workDir)
	if err != nil {
		return "", 0, err
	}
	if _, err := r.git(ctx, nil, "cat-file", "-e", to+"^{commit}"); err != nil {
		return "", 0, errors.New("체크포인트를 찾을 수 없습니다")
	}
	cur, err := r.snapshot(ctx, "before restore to "+to, true)
	if err != nil {
		return "", 0, fmt.Errorf("현재 상태 저장 실패: %w", err)
	}
	// Full list (diffChanges truncates).
	ns, err := r.git(ctx, nil, "diff", "--no-renames", "--name-status", "-z", to, cur)
	if err != nil {
		return "", 0, err
	}
	parts := strings.Split(strings.TrimRight(string(ns), "\x00"), "\x00")
	var checkout []string
	var remove []string
	for i := 0; i+1 < len(parts); i += 2 {
		st, p := parts[i], parts[i+1]
		if st == "" || p == "" {
			continue
		}
		if st[0] == 'A' { // exists now, not in `to` → delete
			remove = append(remove, p)
		} else {
			checkout = append(checkout, p)
		}
	}
	for _, p := range remove {
		full := filepath.Join(r.workDir, filepath.FromSlash(p))
		if rel, err := filepath.Rel(r.workDir, full); err != nil || rel == "." || !filepath.IsLocal(rel) {
			return cur, changed, fmt.Errorf("복원할 수 없는 파일 경로: %q", p)
		}
		if err := os.Remove(full); err == nil {
			changed++
		} else if !os.IsNotExist(err) {
			return cur, changed, fmt.Errorf("파일 삭제 실패: %w", err)
		}
	}
	if len(checkout) > 0 {
		spec := []byte(strings.Join(checkout, "\x00") + "\x00")
		if _, err := r.git(ctx, spec, "checkout", to, "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return cur, changed, fmt.Errorf("파일 복원 실패: %w", err)
		}
		changed += len(checkout)
	}
	// Bring the shadow index/HEAD in line with the restored tree.
	if _, err := r.snapshot(ctx, "restored to "+to, false); err != nil {
		return cur, changed, fmt.Errorf("복원 후 체크포인트 기록 실패: %w", err)
	}
	return cur, changed, nil
}

func logCheckpoint(format string, args ...any) {
	log.Printf("[checkpoint] "+format, args...)
}
