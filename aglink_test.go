package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A relative PATH entry (".", "bin", …) must never resolve a helper binary: that
// is how a binary dropped into whatever directory aglink happens to be
// running from gets executed instead of the real one. exec.LookPath refuses
// these (exec.ErrDot); resolveAglinkBinary must not undo that by walking PATH
// itself and absolutising the hit.
func TestResolveAglinkBinary_RejectsRelativePathEntry(t *testing.T) {
	dir := t.TempDir()
	exe := "aglink-screen" + exeSuffix
	if err := os.WriteFile(filepath.Join(dir, exe), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The binary is in the current directory, reachable only via the "." on PATH.
	t.Chdir(dir)
	t.Setenv("PATH", ".")

	// selfExe empty → no sibling candidates, so PATH is the only route.
	if got := resolveAglinkBinary("aglink-screen", "", ""); got != "" {
		t.Errorf("resolved via a relative PATH entry: %q, want \"\"", got)
	}
}

// An absolute PATH entry resolves normally, and a same-named file in the current
// directory must not shadow it.
func TestResolveAglinkBinary_AbsolutePathEntryWinsOverCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	cwd := filepath.Join(dir, "cwd")
	pathDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := "aglink-screen" + exeSuffix
	if err := os.WriteFile(filepath.Join(cwd, exe), []byte("decoy"), 0o755); err != nil {
		t.Fatal(err)
	}
	pathBin := filepath.Join(pathDir, exe)
	if err := os.WriteFile(pathBin, []byte("real"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(cwd)
	t.Setenv("PATH", pathDir)

	if got := resolveAglinkBinary("aglink-screen", "", ""); got != pathBin {
		t.Errorf("resolveAglinkBinary() = %q, want the PATH binary %q", got, pathBin)
	}
}
