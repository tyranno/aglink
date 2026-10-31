package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// upload_file puts files from this PC into an <input type="file"> — the step
// every "attach a file" flow stalls on, since page script cannot choose files
// and the OS file picker is outside the page. Both targets use the DevTools
// DOM.setFileInputFiles, which also fires the input/change events frameworks
// listen for: an app window over its CDP connection, a Chrome tab through the
// extension's debugger.

const maxUploadFiles = 20

// uploadPaths reads the 'paths' argument — a JSON array, or one path, or
// several separated by newlines or '|' — and checks each is an existing file
// here. Checked in the daemon so a typo fails with the path named, before
// anything is attached to the page.
func uploadPaths(v any) ([]string, error) {
	var raw []string
	switch p := v.(type) {
	case []any:
		for _, e := range p {
			if s, ok := e.(string); ok {
				raw = append(raw, s)
			}
		}
	case string:
		s := strings.TrimSpace(p)
		if strings.HasPrefix(s, "[") {
			if err := json.Unmarshal([]byte(s), &raw); err != nil {
				return nil, fmt.Errorf("paths: %v", err)
			}
		} else {
			raw = strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '|' })
		}
	}
	var out []string
	for _, r := range raw {
		r = strings.TrimSpace(strings.Trim(strings.TrimSpace(r), `"`))
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, fmt.Errorf("path %q: %v", r, err)
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("file not found on this PC: %s", abs)
		}
		if fi.IsDir() {
			return nil, fmt.Errorf("%s is a folder, not a file", abs)
		}
		out = append(out, abs)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("upload_file requires 'paths' (one or more files on this PC)")
	}
	if len(out) > maxUploadFiles {
		return nil, fmt.Errorf("too many files (%d, max %d)", len(out), maxUploadFiles)
	}
	return out, nil
}

// toStrings reads a paths value that may be []string (in-process) or []any
// (decoded JSON).
func toStrings(v any) []string {
	switch p := v.(type) {
	case []string:
		return p
	case []any:
		out := make([]string, 0, len(p))
		for _, e := range p {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// uploadedText is the result line both targets share.
func uploadedText(paths []string) string {
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = filepath.Base(p)
	}
	return fmt.Sprintf("ok: attached %d file(s): %s", len(paths), strings.Join(names, ", "))
}

// fileInputExpr evaluates to the element the selector names, using the shared
// resolver when the page has it (CallPage injects it).
func fileInputExpr(sel string) string {
	q, _ := json.Marshal(sel)
	return fmt.Sprintf(`(() => { const s = %s; const r = globalThis.__aglink; return r && r.resolve ? r.resolve(s) : document.querySelector(s); })()`, q)
}
