package main

import "testing"

func TestMainWindowTitleIsTeleclaude(t *testing.T) {
	opts := mainWindowOptions()
	if opts.Title != "teleclaude" {
		t.Fatalf("window title = %q, want teleclaude", opts.Title)
	}
}
