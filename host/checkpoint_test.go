package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckpointDiffAndRestore(t *testing.T) {
	t.Setenv(dataDirEnv, t.TempDir())
	work := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(filepath.Join(work, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("keep.txt", "a\nb\n")
	write("src/edit.go", "one\ntwo\n")
	write("gone.txt", "bye\n")
	if err := os.MkdirAll(filepath.Join(work, "node_modules", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("node_modules/x/big.js", "ignored")

	before := Checkpoint(work, "", "before")
	if before == "" {
		t.Fatal("no snapshot")
	}
	if again := Checkpoint(work, "", "same"); again != before {
		t.Fatalf("unchanged folder made a new commit: %s vs %s", again, before)
	}

	write("src/edit.go", "one\nTWO\nthree\n")
	write("new 한글.txt", "hi\n")
	os.Remove(filepath.Join(work, "gone.txt"))
	write("node_modules/x/big.js", "changed but ignored")
	after := Checkpoint(work, "", "after")

	ch := TurnChangesBetween(work, before, after)
	if ch == nil {
		t.Fatal("no changes")
	}
	got := map[string]ChangedFile{}
	for _, f := range ch.Files {
		got[f.Path] = f
	}
	if len(got) != 3 || got["src/edit.go"].Status != "M" || got["new 한글.txt"].Status != "A" || got["gone.txt"].Status != "D" {
		t.Fatalf("files = %+v", ch.Files)
	}
	if f := got["src/edit.go"]; f.Add != 2 || f.Del != 1 {
		t.Fatalf("numstat = %+v", f)
	}

	diff, _, err := CheckpointDiff(work, before, after, "src/edit.go")
	if err != nil || !strings.Contains(diff, "+TWO") || !strings.Contains(diff, "-two") {
		t.Fatalf("diff err=%v\n%s", err, diff)
	}

	undo, n, err := CheckpointRestore(work, before)
	if err != nil || n != 3 || undo == "" {
		t.Fatalf("restore n=%d undo=%q err=%v", n, undo, err)
	}
	read := func(p string) string { b, _ := os.ReadFile(filepath.Join(work, p)); return string(b) }
	if read("src/edit.go") != "one\ntwo\n" || read("gone.txt") != "bye\n" {
		t.Fatal("files not restored")
	}
	if _, err := os.Stat(filepath.Join(work, "new 한글.txt")); !os.IsNotExist(err) {
		t.Fatal("added file not removed")
	}
	if read("node_modules/x/big.js") != "changed but ignored" {
		t.Fatal("excluded file touched")
	}

	// Undo the restore.
	if _, _, err := CheckpointRestore(work, undo); err != nil {
		t.Fatal(err)
	}
	if read("src/edit.go") != "one\nTWO\nthree\n" || read("new 한글.txt") != "hi\n" {
		t.Fatal("undo did not bring changes back")
	}

	if _, _, err := CheckpointDiff(work, "zz; rm", after, ""); err == nil {
		t.Fatal("bad sha accepted")
	}
}

func TestCheckpointSkipsHome(t *testing.T) {
	t.Setenv(dataDirEnv, t.TempDir())
	home := t.TempDir()
	if Checkpoint(home, home, "x") != "" {
		t.Fatal("home folder snapshotted")
	}
}

func TestListProjectFiles(t *testing.T) {
	work := t.TempDir()
	for _, p := range []string{"src/main.go", "src/util/helper.go", "README.md", "node_modules/a/index.js"} {
		os.MkdirAll(filepath.Dir(filepath.Join(work, p)), 0o755)
		os.WriteFile(filepath.Join(work, p), []byte("x"), 0o644)
	}
	got, err := ListProjectFiles(work, "help", 10)
	if err != nil || len(got) != 1 || got[0] != "src/util/helper.go" {
		t.Fatalf("got %v err=%v", got, err)
	}
	all, _ := ListProjectFiles(work, "", 10)
	for _, p := range all {
		if strings.HasPrefix(p, "node_modules") {
			t.Fatalf("excluded dir listed: %v", all)
		}
	}
}
