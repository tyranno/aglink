package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// previewMaxBytes caps how much of a file the in-app preview panel will load, so
// a stray click on a huge log or data file never freezes the UI.
const previewMaxBytes = 1 << 20 // 1 MiB

// PreviewFile reads a local text file for the preview panel and returns it as a
// JSON string {path,name,content,error}. A relative path is resolved against
// baseDir (the conversation's working directory); an absolute path is read as
// is. Problems (missing, a directory, too big, non-text) come back in the
// "error" field rather than as a Go error, so the panel shows the reason in
// place instead of failing opaquely. This is a local desktop app reading files
// the user already has access to, so there is no sandbox beyond the size and
// UTF-8 guards.
func (c *ControlService) PreviewFile(path, baseDir string) (string, error) {
	p := strings.Trim(strings.TrimSpace(path), "`\"' ")
	if p == "" {
		return previewResult("", "", "경로가 비어 있습니다"), nil
	}
	if !filepath.IsAbs(p) && baseDir != "" {
		p = filepath.Join(baseDir, p)
	}
	p = filepath.Clean(p)

	info, err := os.Stat(p)
	if err != nil {
		return previewResult(p, "", "파일을 열 수 없습니다: "+err.Error()), nil
	}
	if info.IsDir() {
		return previewResult(p, "", "폴더는 미리볼 수 없습니다"), nil
	}
	if info.Size() > previewMaxBytes {
		return previewResult(p, "", fmt.Sprintf("파일이 너무 큽니다 (%d bytes, 최대 %d)", info.Size(), previewMaxBytes)), nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return previewResult(p, "", "읽기 실패: "+err.Error()), nil
	}
	if !utf8.Valid(data) {
		return previewResult(p, "", "텍스트 파일이 아니라 미리볼 수 없습니다"), nil
	}
	return previewResult(p, string(data), ""), nil
}

func previewResult(path, content, errMsg string) string {
	b, _ := json.Marshal(map[string]any{
		"path":    path,
		"name":    filepath.Base(path),
		"content": content,
		"error":   errMsg,
	})
	return string(b)
}
