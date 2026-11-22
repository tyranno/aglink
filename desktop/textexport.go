package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// maxTextAttachmentBytes caps a long paste turned into a .txt attachment.
const maxTextAttachmentBytes = 5 << 20

// SaveTextAttachment stages pasted text as a .txt file under aglink's
// attachments directory, so a very long paste can go up as a file instead of
// flooding the composer (the frontend offers this on a large paste).
func (c *ControlService) SaveTextAttachment(text string) (string, error) {
	return stageTextAttachment(text)
}

func stageTextAttachment(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", errors.New("text is empty")
	}
	if len(text) > maxTextAttachmentBytes {
		return "", fmt.Errorf("text is too large (%d bytes, max %d)", len(text), maxTextAttachmentBytes)
	}
	return writeStagedAttachmentBytes([]byte(text), ".txt")
}

// SaveMarkdownFile asks where to save (native save dialog, suggested name from
// the conversation title) and writes the exported conversation there. Returns
// the written path, or "" when the user cancelled.
func (c *ControlService) SaveMarkdownFile(title, content string) (string, error) {
	path, err := application.Get().Dialog.SaveFile().
		SetFilename(exportFileName(title)).
		AddFilter("Markdown", "*.md").
		CanCreateDirectories(true).
		PromptForSingleSelection()
	if err != nil || strings.TrimSpace(path) == "" {
		return "", err
	}
	return writeExportFile(path, content)
}

func writeExportFile(path, content string) (string, error) {
	if filepath.Ext(path) == "" {
		path += ".md"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write export: %w", err)
	}
	return path, nil
}

// exportFileName turns a conversation title into a safe Windows file name:
// reserved characters and control characters become "_", trailing dots and
// spaces go, it is kept short, and it always ends in ".md".
func exportFileName(title string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(title) {
		switch {
		case strings.ContainsRune(`<>:"/\|?*`, r), unicode.IsControl(r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	name := b.String()
	if utf8.RuneCountInString(name) > 80 {
		name = string([]rune(name)[:80])
	}
	name = strings.TrimRight(name, ". ")
	if name == "" || strings.Trim(name, "_") == "" {
		name = "대화"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".md") {
		name += ".md"
	}
	return name
}
