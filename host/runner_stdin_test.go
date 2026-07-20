package main

import (
	"strings"
	"testing"
)

// The prompt must be piped to claude via stdin, never placed on the command line.
// A large prompt (full history + memory + MCP config) on argv would exceed the
// Windows command-line length limit (~32767 chars) and fail process creation with
// "The filename or extension is too long".
func TestWorkerBaseArgs_PromptNotInArgv(t *testing.T) {
	huge := strings.Repeat("x", 40000)
	req := RunRequest{Prompt: huge, SessionID: "sess-1"}
	args := workerBaseArgs(&Config{}, req, "", "")

	if len(args) == 0 || args[0] != "-p" {
		t.Fatalf("expected -p as first arg (prompt via stdin), got %v", args)
	}
	for i, a := range args {
		if strings.Contains(a, huge) {
			t.Fatalf("prompt leaked into argv at index %d — would blow the Windows cmdline limit", i)
		}
	}
	// Whole command line must stay well under the Windows limit regardless of prompt size.
	if total := len(strings.Join(args, " ")); total > 4096 {
		t.Errorf("argv unexpectedly large (%d chars) — prompt may be leaking into args", total)
	}
}
