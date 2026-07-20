package main

import "testing"

func TestMainWindowTitleIsAglink(t *testing.T) {
	opts := mainWindowOptions()
	if opts.Title != "aglink" {
		t.Fatalf("window title = %q, want aglink", opts.Title)
	}
}
