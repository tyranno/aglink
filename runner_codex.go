package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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
	cfgh      *ConfigHolder
}

func (r *codexRunner) cfg() *Config { return r.cfgh.Get() }

// resolveNativeCodex finds the platform-specific codex.exe given the npm .cmd wrapper path.
// npm installs codex.cmd at <root> and the native binary inside node_modules.
// Running the native exe directly avoids cmd.exe + node.exe stdin piping issues.
func resolveNativeCodex(cmdPath string) string {
	dir := filepath.Dir(cmdPath) // e.g. C:\Program Files\nodejs
	// Try both nested (npm global) and flat node_modules layouts.
	roots := []string{
		filepath.Join(dir, "node_modules", "@openai", "codex", "node_modules", "@openai"),
		filepath.Join(dir, "node_modules", "@openai"),
	}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), "codex-") {
				continue
			}
			vendorDir := filepath.Join(root, e.Name(), "vendor")
			triples, _ := os.ReadDir(vendorDir)
			for _, triple := range triples {
				exe := filepath.Join(vendorDir, triple.Name(), "bin", "codex.exe")
				if _, err := os.Stat(exe); err == nil {
					return exe
				}
			}
		}
	}
	return ""
}

// NewCodexRunner builds a ClaudeClient backed by the local codex CLI.
// If given an npm .cmd wrapper, it resolves to the native codex.exe to avoid
// cmd.exe + node.exe chain issues with stdin and spaces-in-path.
func NewCodexRunner(codexPath string, cfgh *ConfigHolder) *codexRunner {
	ext := strings.ToLower(filepath.Ext(codexPath))
	if ext == ".cmd" || ext == ".bat" {
		if native := resolveNativeCodex(codexPath); native != "" {
			log.Printf("[codex] resolved native binary: %s", native)
			codexPath = native
		}
	}
	return &codexRunner{codexPath: codexPath, cfgh: cfgh}
}

// codexWorkerModel returns the model for actual work (Run). "" = codex built-in default.
func codexWorkerModel(cfg *Config) string {
	return cfg.CodexModel
}

// codexScreenArgs returns the codex `-c` config overrides that inject the
// aglink-screen MCP server inline (the Codex analogue of Claude's
// pluginWorkerArgs / --mcp-config). Codex has no inline-JSON flag; instead it
// takes dotted-path TOML overrides via `-c key=value`. Combined with the
// existing --ignore-user-config, this needs no static config.toml file:
//
//	-c mcp_servers.screen.command="<path>"
//	-c mcp_servers.screen.args=["mcp"]
//
// The values are produced with encoding/json so a Windows path's backslashes are
// escaped correctly inside the TOML string/array literal (JSON string/array
// syntax is a valid TOML basic-string / array literal here) — no manual concat.
func codexScreenArgs(screenBinaryPath string) []string {
	cmdVal, err := json.Marshal(screenBinaryPath)
	if err != nil {
		return nil
	}
	argsVal, err := json.Marshal([]string{"mcp"})
	if err != nil {
		return nil
	}
	return []string{
		"-c", "mcp_servers.screen.command=" + string(cmdVal),
		"-c", "mcp_servers.screen.args=" + string(argsVal),
	}
}

// codexWebArgs is the aglink-web analogue of codexScreenArgs: it injects the
// "web" MCP server (list_tabs/navigate/get_page_text over the user's real
// Chrome) via the same `-c mcp_servers.<key>.*` mechanism. Unlike Claude's
// single --mcp-config JSON blob, codex's `-c` overrides are independent
// per-key flags, so this can simply be appended alongside codexScreenArgs
// without any merging.
func codexWebArgs(webBinaryPath string) []string {
	cmdVal, err := json.Marshal(webBinaryPath)
	if err != nil {
		return nil
	}
	argsVal, err := json.Marshal([]string{"mcp"})
	if err != nil {
		return nil
	}
	return []string{
		"-c", "mcp_servers.web.command=" + string(cmdVal),
		"-c", "mcp_servers.web.args=" + string(argsVal),
	}
}

// extractCodexToolResultImages pulls decoded images out of codex's --json NDJSON
// stream (the Codex analogue of Claude's extractToolResultImages). Screen MCP
// images arrive in an "item.completed" event whose item.type == "mcp_tool_call",
// inside item.result.content[] as {"type":"image","data":"<base64>"} blocks
// (a sibling {"type":"text",...} becomes the caption). Returns nil when none.
func extractCodexToolResultImages(ndjson string) []capturedImage {
	var out []capturedImage
	for _, line := range strings.Split(ndjson, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Item *struct {
				Type   string          `json:"type"`
				Result json.RawMessage `json:"result"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Type != "item.completed" || ev.Item == nil || ev.Item.Type != "mcp_tool_call" || len(ev.Item.Result) == 0 {
			continue
		}
		// result may be an object with a content array, or a bare string for
		// text-only tool results — only the object form carries images.
		var res struct {
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Data   string `json:"data"`
				Source *struct {
					Data string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		}
		if json.Unmarshal(ev.Item.Result, &res) != nil {
			continue
		}
		caption := ""
		var pngs [][]byte
		for _, b := range res.Content {
			switch b.Type {
			case "text":
				if caption == "" {
					caption = strings.TrimSpace(b.Text)
				}
			case "image":
				data := b.Data
				if data == "" && b.Source != nil {
					data = b.Source.Data
				}
				if data != "" {
					if dec, derr := base64.StdEncoding.DecodeString(data); derr == nil && len(dec) > 0 {
						pngs = append(pngs, dec)
					}
				}
			}
		}
		for _, png := range pngs {
			out = append(out, capturedImage{png: png, caption: caption})
		}
	}
	return out
}

// codexManagerModel returns the model for routing (Route). Falls back to worker model.
func codexManagerModel(cfg *Config) string {
	if cfg.CodexManagerModel != "" {
		return cfg.CodexManagerModel
	}
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

// logCodexEvent logs a single JSONL event from codex stdout in real-time.
func logCodexEvent(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return
	}
	evType, _ := ev["type"].(string)
	switch evType {
	case "thread.started":
		log.Printf("[codex] ▶ session: %v", ev["thread_id"])
	case "turn.started":
		log.Printf("[codex] ▶ turn started")
	case "item.started":
		if item, ok := ev["item"].(map[string]any); ok {
			cmd, _ := item["command"].(string)
			if len(cmd) > 100 {
				cmd = cmd[:100] + "..."
			}
			log.Printf("[codex] ⚙ %s: %s", item["type"], cmd)
		}
	case "item.completed":
		if item, ok := ev["item"].(map[string]any); ok {
			log.Printf("[codex] ✓ %s (exit=%v)", item["type"], item["exit_code"])
		}
	case "turn.completed":
		if usage, ok := ev["usage"].(map[string]any); ok {
			log.Printf("[codex] ✅ turn done — in:%v cached:%v out:%v reasoning:%v",
				usage["input_tokens"], usage["cached_input_tokens"],
				usage["output_tokens"], usage["reasoning_output_tokens"])
		}
	}
}

// exec runs codex with real-time JSONL event logging and process-tree cancellation.
// stdinData, if non-empty, is piped to the process stdin (used for single-turn prompts).
// Passing prompt via stdin + EOF tells codex to process one turn then exit (avoids REPL loop).
func (r *codexRunner) exec(ctx context.Context, dir string, args []string, stdinData string) (stdout, stderr string, err error) {
	// On Windows, .cmd/.bat files need cmd.exe /C with special quoting.
	// Go's automatic arg escaping conflicts with cmd.exe /C quoting rules:
	//   cmd.exe /C "path with spaces" → strips first & last quote → unquoted path
	// Fix: use the double-outer-quote pattern and bypass Go quoting via SysProcAttr.CmdLine:
	//   cmd.exe /C ""path with spaces" arg1 "arg2 with spaces" ..."
	// cmd.exe strips outer quotes leaving the properly-quoted inner command.
	ext := strings.ToLower(filepath.Ext(r.codexPath))
	var cmd *exec.Cmd
	if ext == ".cmd" || ext == ".bat" {
		var sb strings.Builder
		sb.WriteString(`"`) // outer quote open
		sb.WriteString(`"`) // inner path quote open
		sb.WriteString(r.codexPath)
		sb.WriteString(`"`) // inner path quote close
		for _, a := range args {
			sb.WriteString(" ")
			if strings.ContainsAny(a, " \t") {
				sb.WriteString(`"`)
				sb.WriteString(a)
				sb.WriteString(`"`)
			} else {
				sb.WriteString(a)
			}
		}
		sb.WriteString(`"`) // outer quote close
		cmd = exec.CommandContext(ctx, "cmd.exe")
		applyCmdLine(cmd, "cmd.exe /C "+sb.String())
	} else {
		cmd = exec.CommandContext(ctx, r.codexPath, args...)
	}
	cmd.Dir = dir
	cmd.Cancel = func() error {
		log.Printf("[codex] ⚠ cancelling (PID %d) — killing process tree + all codex.exe", cmd.Process.Pid)
		killErr := killTree(cmd.Process.Pid)
		killByImageName("codex" + exeSuffix)
		return killErr
	}

	// Tee stdout: buffer for return value + pipe for real-time logging
	pr, pw := io.Pipe()
	var outBuf bytes.Buffer
	cmd.Stdout = io.MultiWriter(&outBuf, pw)

	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf

	// Pipe prompt via stdin so codex processes one turn then exits on EOF.
	if stdinData != "" {
		cmd.Stdin = strings.NewReader(stdinData)
	}

	if startErr := cmd.Start(); startErr != nil {
		pr.Close()
		pw.Close()
		return "", "", startErr
	}

	// Show first few args (skip secrets/tokens)
	shown := args
	if len(shown) > 4 {
		shown = shown[:4]
	}
	log.Printf("[codex] started PID %d: %s %s", cmd.Process.Pid, r.codexPath, strings.Join(shown, " "))

	// Goroutine: drain pipe and log events as they arrive.
	//
	// Codex --json can emit very long NDJSON lines (e.g. a tool_result carrying a
	// base64 image from a screen/web MCP tool). bufio.Scanner's default 64KB token
	// cap would make Scan() stop with an error, after which this goroutine would
	// stop reading pr — and the stdout copier's next pw.Write would block forever
	// on the unread pipe. That deadlocks cmd.Wait(): the worker never returns and,
	// because the block is on the pipe (not the process), even ctx-timeout killing
	// codex can't unblock it — the turn hangs indefinitely with no completion and
	// no timeout. Fix: allow large lines, and ALWAYS drain any remainder so the
	// copier can never block.
	logDone := make(chan struct{})
	go func() {
		defer close(logDone)
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for scanner.Scan() {
			logCodexEvent(scanner.Text())
		}
		// If Scan stopped early (line still over the cap, or a read error), keep
		// draining so the pipe writer never blocks.
		_, _ = io.Copy(io.Discard, pr)
	}()

	err = cmd.Wait()
	pw.Close() // signal EOF to logging goroutine
	<-logDone  // wait for all events to be logged

	if stderrStr := strings.TrimSpace(errBuf.String()); stderrStr != "" {
		// Only log first 500 chars of stderr to avoid flooding
		if len(stderrStr) > 500 {
			stderrStr = stderrStr[:500] + "...(truncated)"
		}
		log.Printf("[codex] stderr: %s", stderrStr)
	}
	if err != nil {
		log.Printf("[codex] PID %d exited: %v", cmd.Process.Pid, err)
	}

	return outBuf.String(), errBuf.String(), err
}

// Route asks Codex to classify the user message and return a routing decision.
// Uses a cheap/fast model (codex_manager_model) and a 60s timeout.
// --sandbox read-only prevents command execution during text classification.
func (r *codexRunner) Route(ctx context.Context, req RouteRequest) (RouteDecision, error) {
	routeCtx, routeCancel := context.WithTimeout(ctx, 60*time.Second)
	defer routeCancel()

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
		"--ignore-user-config",
		"--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox",
		"--ephemeral",
		"--sandbox", "read-only",
		"--json",
		"-o", outFile,
	}
	if m := codexManagerModel(r.cfg()); m != "" {
		args = append(args, "-m", m)
	}
	args = append(args, prompt)

	log.Printf("[codex] route: model=%q projects=%d", codexManagerModel(r.cfg()), len(req.Projects))
	home, _ := os.UserHomeDir()
	_, stderr, err := r.exec(routeCtx, home, args, "")
	if err != nil {
		return RouteDecision{}, fmt.Errorf("codex manager 호출 실패: %w (%s)", err, strings.TrimSpace(stderr))
	}

	content, rerr := os.ReadFile(outFile)
	if rerr != nil {
		return RouteDecision{}, fmt.Errorf("codex route 결과 파일 읽기 실패: %w", rerr)
	}
	dec, perr := parseCodexRouteDecision(string(content))
	if perr != nil {
		log.Printf("[codex] route parse error — raw output: %q", string(content))
		return RouteDecision{}, perr
	}
	log.Printf("[codex] route: action=%s project=%q conv=%q", dec.Action, dec.Project, dec.ConversationID)
	return dec, nil
}

// Run executes a worker turn via codex exec.
// Uses a powerful model (codex_model) for actual work.
func (r *codexRunner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	outFile := filepath.Join(os.TempDir(), fmt.Sprintf("teleclaude_codex_%d_%s.txt", os.Getpid(), req.SessionID))
	defer os.Remove(outFile)

	model := req.Model
	if model == "" {
		model = codexWorkerModel(r.cfg())
	}

	// Codex exec without --ephemeral enters a REPL loop and waits for more stdin after
	// each turn, causing "Reading additional input from stdin..." on EOF.
	// With --ephemeral, exec runs a single non-interactive turn and exits cleanly.
	// Conversation context is preserved via buildContextPrompt (history in every prompt).
	args := []string{
		"exec",
		"-C", req.WorkDir,
		"--ignore-user-config",
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check",
		"--ephemeral",
		"--json",
		"-o", outFile,
	}
	if model != "" {
		args = append(args, "-m", model)
	}
	// Inject the aglink-screen/aglink-web MCP servers inline (same gating as the
	// Claude path) so codex-backed workers can drive the screen / real Chrome too.
	selfExe, _ := os.Executable()
	if screenBin := resolveScreenBinaryPath(r.cfg(), selfExe); r.cfg().ScreenControl && screenBin != "" {
		args = append(args, codexScreenArgs(screenBin)...)
	}
	if webBin := resolveWebBinaryPath(r.cfg(), selfExe); r.cfg().WebControl && webBin != "" {
		args = append(args, codexWebArgs(webBin)...)
	}
	args = append(args, req.Prompt)

	log.Printf("[codex] run: model=%q session=%s resume=%v dir=%s prompt=%d chars",
		model, req.SessionID, req.Resume, req.WorkDir, len(req.Prompt))

	stdout, stderr, err := r.exec(ctx, req.WorkDir, args, "")

	// Relay any tool images (screen MCP screenshot/capture_*) to the caller —
	// codex exec is blocking, so we extract from the finished NDJSON stream (unlike
	// the Claude path which streams live). Fired regardless of exit so images
	// captured before an error still arrive.
	if req.OnImage != nil {
		for _, ci := range extractCodexToolResultImages(stdout) {
			req.OnImage(ci.png, ci.caption)
		}
	}

	if err != nil {
		if ctx.Err() != nil {
			log.Printf("[codex] run: context cancelled/timed out")
			return RunResult{}, ctx.Err()
		}
		// Read output file even on non-zero exit — codex may still produce output.
		if content, rerr := os.ReadFile(outFile); rerr == nil && len(content) > 0 {
			log.Printf("[codex] run: partial output on error (%d bytes)", len(content))
			return RunResult{Text: parseCodexOutput(string(content))}, nil
		}
		return RunResult{}, fmt.Errorf("codex worker 실행 실패: %w (%s)", err, strings.TrimSpace(stderr))
	}

	// Extract thread_id for new sessions so the store can persist it.
	threadID := extractThreadID(stdout)
	if !req.Resume && threadID == "" {
		log.Printf("[codex] warning: thread_id not found in JSONL output; session resume may not work")
	}

	content, rerr := os.ReadFile(outFile)
	if rerr != nil {
		return RunResult{}, fmt.Errorf("codex 결과 파일 읽기 실패: %w", rerr)
	}

	text := parseCodexOutput(string(content))
	log.Printf("[codex] run done: output=%d bytes session=%s", len(text), threadID)

	result := RunResult{Text: text}
	if !req.Resume && threadID != "" {
		result.SessionID = threadID
	}
	return result, nil
}
