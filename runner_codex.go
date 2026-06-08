package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// codexRunner implements ClaudeClient backed by the local codex CLI.
// Codex JSONL sessions are identified by thread_id from the thread.started event.
type codexRunner struct {
	codexPath string
	cfg       *Config
}

// NewCodexRunner builds a ClaudeClient backed by the local codex CLI.
func NewCodexRunner(codexPath string, cfg *Config) *codexRunner {
	return &codexRunner{codexPath: codexPath, cfg: cfg}
}

// codexDefaultModel returns the configured model, or "" to let codex use its own default.
func codexDefaultModel(cfg *Config) string {
	return cfg.CodexModel
}

// extractThreadID scans JSONL lines for the thread_id from a thread.started event.
// Returns "" if not found.
func extractThreadID(jsonl string) string {
	for _, line := range strings.Split(jsonl, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev["type"] == "thread.started" {
			if tid, ok := ev["thread_id"].(string); ok && tid != "" {
				return tid
			}
		}
	}
	return ""
}

// parseCodexOutput trims whitespace from the -o file content.
func parseCodexOutput(content string) string {
	return strings.TrimSpace(content)
}

// parseCodexRouteDecision parses a RouteDecision from the codex output string.
func parseCodexRouteDecision(s string) (RouteDecision, error) {
	if dec, ok := unmarshalDecision(s); ok {
		return dec, nil
	}
	return RouteDecision{}, fmt.Errorf("codex 라우팅 JSON 파싱 실패: %q", s)
}

// exec runs codex with process-tree cancellation (Windows-aware).
// On cancellation, kills the process tree AND any remaining codex child processes
// by image name to prevent orphan accumulation.
func (r *codexRunner) exec(ctx context.Context, dir string, args []string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, r.codexPath, args...)
	cmd.Dir = dir
	cmd.Cancel = func() error {
		err := killTree(cmd.Process.Pid)
		// Secondary cleanup: kill any codex processes that detached from the tree.
		exec.Command("taskkill", "/F", "/IM", "codex.exe").Run()
		return err
	}

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// codexRouteSchema is routeJSONSchema with additionalProperties:false added at the top level.
// OpenAI structured output (--output-schema) requires this on all object schemas.
// With --output-schema, Codex outputs the JSON directly without running shell commands.
const codexRouteSchema = `{"type":"object","additionalProperties":false,"properties":{"project":{"type":"string"},"conversationId":{"type":"string"},"action":{"type":"string","enum":["resume","new","clarify","status","schedule"]},"newTitle":{"type":"string"},"clarify":{"type":"string"},"confidence":{"type":"number"},"scheduleType":{"type":"string","enum":["remind","cron"]},"scheduleInterval":{"type":"string"},"scheduleTask":{"type":"string"},"scheduleIsTask":{"type":"boolean"}},"required":["action"]}`

// Route asks Codex to classify the user message and return a routing decision.
// Uses --output-schema to force structured JSON output and --sandbox read-only to
// prevent Codex from executing shell commands (routing is text classification only).
// A 60-second sub-timeout prevents orphan processes if the schema enforcement fails.
func (r *codexRunner) Route(ctx context.Context, req RouteRequest) (RouteDecision, error) {
	// Route must complete quickly — 60s sub-deadline regardless of worker timeout.
	routeCtx, routeCancel := context.WithTimeout(ctx, 60*time.Second)
	defer routeCancel()

	sf, err := os.CreateTemp("", "teleclaude_route_schema_*.json")
	if err != nil {
		return RouteDecision{}, fmt.Errorf("codex route schema 임시 파일 생성 실패: %w", err)
	}
	schemaFile := sf.Name()
	sf.Close()
	defer os.Remove(schemaFile)
	if err := os.WriteFile(schemaFile, []byte(codexRouteSchema), 0600); err != nil {
		return RouteDecision{}, fmt.Errorf("codex route schema 쓰기 실패: %w", err)
	}

	of, err := os.CreateTemp("", "teleclaude_route_out_*.txt")
	if err != nil {
		return RouteDecision{}, fmt.Errorf("codex route 출력 임시 파일 생성 실패: %w", err)
	}
	outFile := of.Name()
	of.Close()
	defer os.Remove(outFile)

	prompt := buildRoutePrompt(req)
	args := []string{
		"exec",
		"--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox",
		"--ephemeral",
		"--sandbox", "read-only", // prevent command execution during routing
		"--output-schema", schemaFile,
		"--json",
		"-o", outFile,
	}
	if m := codexDefaultModel(r.cfg); m != "" {
		args = append(args, "-m", m)
	}
	args = append(args, prompt)

	home, _ := os.UserHomeDir()
	_, stderr, err := r.exec(routeCtx, home, args)
	if err != nil {
		return RouteDecision{}, fmt.Errorf("codex manager 호출 실패: %w (%s)", err, strings.TrimSpace(stderr))
	}

	content, rerr := os.ReadFile(outFile)
	if rerr != nil {
		return RouteDecision{}, fmt.Errorf("codex route 결과 파일 읽기 실패: %w", rerr)
	}
	return parseCodexRouteDecision(string(content))
}

// Run executes a worker turn via codex exec.
func (r *codexRunner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	outFile := filepath.Join(os.TempDir(), fmt.Sprintf("teleclaude_codex_%d_%s.txt", os.Getpid(), req.SessionID))
	defer os.Remove(outFile)

	model := req.Model
	if model == "" {
		model = codexDefaultModel(r.cfg)
	}

	var args []string
	if req.Resume && req.SessionID != "" {
		args = []string{
			"exec", "resume", req.SessionID,
			"--dangerously-bypass-approvals-and-sandbox",
			"--skip-git-repo-check",
			"--json",
			"-o", outFile,
		}
	} else {
		args = []string{
			"exec",
			"-C", req.WorkDir,
			"--dangerously-bypass-approvals-and-sandbox",
			"--skip-git-repo-check",
			"--json",
			"-o", outFile,
		}
	}
	if model != "" {
		args = append(args, "-m", model)
	}
	args = append(args, req.Prompt)

	stdout, stderr, err := r.exec(ctx, req.WorkDir, args)
	if err != nil {
		if ctx.Err() != nil {
			return RunResult{}, ctx.Err()
		}
		// Read output file even on non-zero exit — codex may still produce output.
		if content, rerr := os.ReadFile(outFile); rerr == nil && len(content) > 0 {
			return RunResult{Text: parseCodexOutput(string(content))}, nil
		}
		return RunResult{}, fmt.Errorf("codex worker 실행 실패: %w (%s)", err, strings.TrimSpace(stderr))
	}

	// Extract thread_id for new sessions so the store can persist it.
	// If empty, codex changed its JSONL event format — resume will fall back to UUID-based attempt.
	threadID := extractThreadID(stdout)
	if !req.Resume && threadID == "" {
		log.Printf("[codex] warning: thread_id not found in JSONL output; session resume may not work")
	}

	content, rerr := os.ReadFile(outFile)
	if rerr != nil {
		return RunResult{}, fmt.Errorf("codex 결과 파일 읽기 실패: %w", rerr)
	}

	result := RunResult{Text: parseCodexOutput(string(content))}
	if !req.Resume && threadID != "" {
		result.SessionID = threadID
	}
	return result, nil
}
