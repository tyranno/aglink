package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUploadPathsChecksTheFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.png")
	b := filepath.Join(dir, "b 공백.pdf")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for in, want := range map[string]int{
		a:             1,
		a + "|" + b:   2,
		`"` + a + `"`: 1,
		`["` + strings.ReplaceAll(a, `\`, `\\`) + `","` + strings.ReplaceAll(b, `\`, `\\`) + `"]`: 2,
	} {
		got, err := uploadPaths(in)
		if err != nil || len(got) != want {
			t.Errorf("uploadPaths(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := uploadPaths(filepath.Join(dir, "missing.txt")); err == nil || !strings.Contains(err.Error(), "file not found on this PC") {
		t.Errorf("a missing file must be named: %v", err)
	}
	if _, err := uploadPaths(dir); err == nil || !strings.Contains(err.Error(), "is a folder") {
		t.Errorf("a folder must be refused: %v", err)
	}
	if _, err := uploadPaths(""); err == nil {
		t.Error("no paths must be refused")
	}
}

func TestAppUploadFile(t *testing.T) {
	f := &fakePage{
		results: map[string]string{"elementExists": `{"exists":true,"visible":false,"count":1}`},
		rawResults: map[string]string{
			"Runtime.evaluate":       `{"result":{"type":"object","subtype":"node","objectId":"obj-1"}}`,
			"Runtime.callFunctionOn": `{"result":{"value":[true,false,"input"]}}`,
		},
	}
	r := run(t, f, "upload_file", map[string]any{"selector": "input[type=file]", "paths": []string{`C:\tmp\a.png`}})
	if !r.OK || r.Text != "ok: attached 1 file(s): a.png" {
		t.Fatalf("got %+v", r)
	}
	if got := strings.Join(f.raws, ","); got != "Runtime.evaluate,Runtime.callFunctionOn,DOM.setFileInputFiles" {
		t.Fatalf("raws = %s", got)
	}
	set := f.rawParams["DOM.setFileInputFiles"].(map[string]any)
	if set["objectId"] != "obj-1" {
		t.Fatalf("setFileInputFiles params: %v", set)
	}

	// Two files into a single-file input: refused before anything is set.
	f.raws = nil
	r = run(t, f, "upload_file", map[string]any{"selector": "input[type=file]", "paths": []string{`C:\a`, `C:\b`}})
	if r.OK || !strings.Contains(r.Error, "takes one file") {
		t.Fatalf("multiple into single: %+v", r)
	}

	// Not a file input: says what it found instead.
	f.rawResults["Runtime.callFunctionOn"] = `{"result":{"value":[false,false,"button"]}}`
	r = run(t, f, "upload_file", map[string]any{"selector": "text=첨부", "paths": []string{`C:\a`}})
	if r.OK || !strings.Contains(r.Error, `element <button> matched by text=첨부 is not an <input type="file">`) {
		t.Fatalf("not a file input: %+v", r)
	}
}
