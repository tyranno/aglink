package main

import (
	"slices"
	"testing"
)

// A desync can leave a conversation marked Started with an empty session id. If
// that empty id reaches the claude CLI, `--resume ""` (or `--session-id ""`) is
// rejected — "--resume requires a valid session ID" — failing every turn. These
// tests pin that workerBaseArgs never emits a session flag with an empty value.

func TestWorkerBaseArgs_EmptySessionOmitsSessionFlags(t *testing.T) {
	for _, resume := range []bool{true, false} {
		args := workerBaseArgs(&Config{}, RunRequest{SessionID: "", Resume: resume}, "", "", "")
		if i := slices.Index(args, "--resume"); i != -1 {
			t.Errorf("resume=%v: --resume must not be emitted for an empty session id: %v", resume, args)
		}
		if i := slices.Index(args, "--session-id"); i != -1 {
			t.Errorf("resume=%v: --session-id must not be emitted for an empty session id: %v", resume, args)
		}
	}
}

func TestWorkerBaseArgs_NonEmptySessionEmitsCorrectFlag(t *testing.T) {
	sid := "1c638281-7c9e-41ca-8e6a-910536b16445"

	resumeArgs := workerBaseArgs(&Config{}, RunRequest{SessionID: sid, Resume: true}, "", "", "")
	if i := slices.Index(resumeArgs, "--resume"); i == -1 || i+1 >= len(resumeArgs) || resumeArgs[i+1] != sid {
		t.Errorf("resume turn should emit --resume %s, got %v", sid, resumeArgs)
	}
	if slices.Contains(resumeArgs, "--session-id") {
		t.Errorf("resume turn must not also emit --session-id: %v", resumeArgs)
	}

	freshArgs := workerBaseArgs(&Config{}, RunRequest{SessionID: sid, Resume: false}, "", "", "")
	if i := slices.Index(freshArgs, "--session-id"); i == -1 || i+1 >= len(freshArgs) || freshArgs[i+1] != sid {
		t.Errorf("fresh turn should emit --session-id %s, got %v", sid, freshArgs)
	}
	if slices.Contains(freshArgs, "--resume") {
		t.Errorf("fresh turn must not emit --resume: %v", freshArgs)
	}
}
