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
	"sync"
	"time"
)

// codexRunner implements ClaudeClient backed by the local codex CLI.
// Codex JSONL sessions are identified by thread_id from the thread.started event.
type codexRunner struct {
	codexPath string
	cfgh      *ConfigHolder

	// ignoreUserConfigOnce/OK back supportsIgnoreUserConfig — a one-time probe
	// cached for this runner's lifetime.
	ignoreUserConfigOnce sync.Once
	ignoreUserConfigOK   bool

	// ephemeralOnce/OK back supportsEphemeral — a one-time probe cached for
	// this runner's lifetime.
	ephemeralOnce sync.Once
	ephemeralOK   bool

	// outputLastMessageOnce/OK back supportsOutputLastMessage — a one-time
	// probe cached for this runner's lifetime.
	outputLastMessageOnce sync.Once
	outputLastMessageOK   bool

	// versionOnce/Str cache the codex CLI version string ("0.142.5"), detected
	// once via `codex --version` for the readiness notice.
	versionOnce sync.Once
	versionStr  string

	// modelCatalogOnce/Slugs cache the user-selectable codex model slugs,
	// detected once via `codex debug models` for the settings UI's model
	// dropdown (see modelCatalog).
	modelCatalogOnce  sync.Once
	modelCatalogSlugs []string

	// readyNoticeOnce guards the one-time "codex backend ready" heads-up so it
	// fires only on the first codex-backed worker turn of this runner's life.
	readyNoticeOnce sync.Once
}

// supportsIgnoreUserConfig reports whether the installed codex CLI accepts
// --ignore-user-config, probed once via `codex exec --help` and cached. Older
// codex CLI builds don't have this flag yet — found live (2026-07-09) when a
// tester's install rejected every codex-backed turn with "error: unexpected
// argument '--ignore-user-config' found". Probing --help avoids hardcoding a
// version cutoff that would need updating every time codex ships a release;
// if the probe itself fails for any reason, this conservatively reports
// false so the turn still runs (without --ignore-user-config) instead of
// never starting at all.
func (r *codexRunner) supportsIgnoreUserConfig() bool {
	r.ignoreUserConfigOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stdout, stderr, _ := r.exec(ctx, "", []string{"exec", "--help"}, "", "")
		r.ignoreUserConfigOK = helpMentionsIgnoreUserConfig(stdout, stderr)
	})
	return r.ignoreUserConfigOK
}

// helpMentionsIgnoreUserConfig is the pure decision behind supportsIgnoreUserConfig,
// split out so it's testable without spawning a real codex process.
func helpMentionsIgnoreUserConfig(stdout, stderr string) bool {
	return strings.Contains(stdout, "--ignore-user-config") || strings.Contains(stderr, "--ignore-user-config")
}

// supportsEphemeral reports whether the installed codex CLI accepts
// --ephemeral, probed once via `codex exec --help` and cached. Older codex
// CLI builds don't have this flag yet — found live (2026-07-09) when a
// tester's install rejected every codex-backed turn with "error: unexpected
// argument '--ephemeral' found". Same probing approach as
// supportsIgnoreUserConfig: if the probe fails, conservatively report false so
// the call still runs instead of never starting at all.
//
// Only Route uses it. A worker turn must NOT be ephemeral: codex records no
// rollout for an ephemeral turn, and without a rollout `exec resume <thread>`
// cannot continue the conversation (see codexRunBaseArgs).
func (r *codexRunner) supportsEphemeral() bool {
	r.ephemeralOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stdout, stderr, _ := r.exec(ctx, "", []string{"exec", "--help"}, "", "")
		r.ephemeralOK = helpMentionsEphemeral(stdout, stderr)
	})
	return r.ephemeralOK
}

// helpMentionsEphemeral is the pure decision behind supportsEphemeral, split
// out so it's testable without spawning a real codex process.
func helpMentionsEphemeral(stdout, stderr string) bool {
	return strings.Contains(stdout, "--ephemeral") || strings.Contains(stderr, "--ephemeral")
}

// supportsOutputLastMessage reports whether the installed codex CLI accepts
// -o/--output-last-message, probed once via `codex exec --help` and cached.
// Older codex CLI builds don't have this flag yet — found live (2026-07-09)
// when a tester's install rejected every codex-backed turn with "error:
// unexpected argument '-o' found". Same probing approach as
// supportsIgnoreUserConfig/supportsEphemeral: if the probe fails,
// conservatively report false so the turn falls back to reading the final
// agent_message straight out of the --json stdout stream instead of never
// starting at all.
func (r *codexRunner) supportsOutputLastMessage() bool {
	r.outputLastMessageOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stdout, stderr, _ := r.exec(ctx, "", []string{"exec", "--help"}, "", "")
		r.outputLastMessageOK = helpMentionsOutputLastMessage(stdout, stderr)
	})
	return r.outputLastMessageOK
}

// helpMentionsOutputLastMessage is the pure decision behind
// supportsOutputLastMessage, split out so it's testable without spawning a
// real codex process.
func helpMentionsOutputLastMessage(stdout, stderr string) bool {
	return strings.Contains(stdout, "--output-last-message") || strings.Contains(stderr, "--output-last-message")
}

// codexVersion detects the installed codex CLI version once via `codex
// --version` ("codex-cli 0.142.5" → "0.142.5") and caches it. Best-effort: an
// empty string means the version couldn't be determined (surfaced to the user
// as "알 수 없음" in the readiness messages).
func (r *codexRunner) codexVersion() string {
	r.versionOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stdout, stderr, _ := r.exec(ctx, "", []string{"--version"}, "", "")
		r.versionStr = parseCodexVersion(stdout + " " + stderr)
	})
	return r.versionStr
}

// parseCodexVersion pulls the semver-ish token out of `codex --version` output.
// It scans for the first whitespace-separated field that starts with a digit
// and contains a dot, so it survives label changes ("codex-cli 0.142.5",
// "codex-cli-exec 0.142.5"). Returns "" when nothing matches.
func parseCodexVersion(out string) string {
	for _, f := range strings.Fields(out) {
		if len(f) > 0 && f[0] >= '0' && f[0] <= '9' && strings.Contains(f, ".") {
			return f
		}
	}
	return ""
}

// modelCatalog detects the codex model slugs a user can actually pick, once
// per runner lifetime via `codex debug models` (a hidden but stable command
// that dumps the CLI's own model catalog as JSON — genuinely queried from the
// installed CLI, not a guessed/hardcoded list). Only "list"-visibility models
// are returned (internal ones like "codex-auto-review" are hidden). Returns
// nil on any failure (old codex-cli without the subcommand, unparseable
// output, etc.) so the settings UI can fall back to a free-text field instead
// of showing a broken empty dropdown.
func (r *codexRunner) modelCatalog() []string {
	r.modelCatalogOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stdout, _, err := r.exec(ctx, "", []string{"debug", "models"}, "", "")
		if err != nil {
			return
		}
		r.modelCatalogSlugs = parseCodexModelCatalog(stdout)
	})
	return r.modelCatalogSlugs
}

// parseCodexModelCatalog pulls the user-selectable model slugs out of `codex
// debug models`' JSON output, split out so it's testable without spawning a
// real codex process.
func parseCodexModelCatalog(stdout string) []string {
	var parsed struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		return nil
	}
	var slugs []string
	for _, m := range parsed.Models {
		if m.Visibility == "list" && m.Slug != "" {
			slugs = append(slugs, m.Slug)
		}
	}
	return slugs
}

// CheckReadiness gates a codex-backed worker turn on CLI capability and returns
// a one-time heads-up. It implements the backendReadiness interface the manager
// consults before running a turn.
//
// The gate is --ignore-user-config support: without it, an older codex-cli
// pulls the user's global ~/.codex config (skills, MCP servers) into every
// turn, which was observed to break execution outright (the sandbox setup
// helper exiting non-zero while running a skill's shell command). So a codex
// too old to accept the flag is blocked with upgrade guidance rather than left
// to dead-end mid-turn. When ready, the first call returns a one-time notice
// naming the detected version; later calls return ("", true).
func (r *codexRunner) CheckReadiness() (ok bool, msg string) {
	hasIgnoreUserConfig := r.supportsIgnoreUserConfig()
	version := r.codexVersion()
	first := false
	if hasIgnoreUserConfig {
		r.readyNoticeOnce.Do(func() { first = true })
	}
	return codexReadinessDecision(hasIgnoreUserConfig, version, first)
}

// codexReadinessDecision is the pure logic behind CheckReadiness, split out so
// it's testable without spawning codex. ok=false → block the turn and show msg;
// ok=true with a non-empty msg → one-time informational notice; ok=true with an
// empty msg → proceed silently.
func codexReadinessDecision(hasIgnoreUserConfig bool, version string, firstUse bool) (ok bool, msg string) {
	if !hasIgnoreUserConfig {
		return false, codexUpgradeGuidance(version)
	}
	if firstUse {
		return true, codexReadyNotice(version)
	}
	return true, ""
}

// codexUpgradeGuidance is the block message shown when the installed codex-cli
// is too old to run a clean turn. It names the detected version and the exact
// upgrade command.
func codexUpgradeGuidance(version string) string {
	v := version
	if v == "" {
		v = "알 수 없음"
	}
	return "⚠️ codex 백엔드를 사용할 수 없습니다 (설치된 codex-cli: " + v + ").\n\n" +
		"정상 동작에 필요한 `--ignore-user-config` 옵션을 이 버전은 지원하지 않습니다. " +
		"이 옵션이 없으면 사용자 전역 ~/.codex 설정(스킬·MCP)이 매 턴에 끼어들어 실행이 실패합니다.\n\n" +
		"최신 codex-cli로 업데이트한 뒤 다시 시도하세요:\n" +
		"  npm i -g @openai/codex@latest\n" +
		"또는 설정에서 백엔드를 claude로 전환하세요."
}

// codexReadyNotice is the one-time heads-up shown on the first codex-backed
// worker turn, confirming the detected version supports the required options.
func codexReadyNotice(version string) string {
	v := version
	if v == "" {
		v = "확인 불가"
	}
	return "ℹ️ codex 백엔드 v" + v + " 사용 중 — 정상 동작에 필요한 옵션(--ignore-user-config 등)을 모두 지원합니다."
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

// extractLastAgentMessage scans --json NDJSON output for the last agent
// message. Newer codex emits item.completed events with item.type ==
// "agent_message"; older builds emitted top-level agent_message events with a
// content field. This is the fallback used in place of -o/--output-last-message
// on codex CLI builds too old to support that flag.
func extractLastAgentMessage(jsonl string) string {
	last := ""
	for _, line := range strings.Split(jsonl, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Content string `json:"content"`
			Text    string `json:"text"`
			Item    *struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Type == "item.completed" && ev.Item != nil && ev.Item.Type == "agent_message" {
			last = ev.Item.Text
			continue
		}
		if ev.Type == "agent_message" {
			if ev.Content != "" {
				last = ev.Content
			} else {
				last = ev.Text
			}
		}
	}
	return strings.TrimSpace(last)
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
func (r *codexRunner) exec(ctx context.Context, dir string, args []string, stdinData, ownerLabel string) (stdout, stderr string, err error) {
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
	// Export AGLINK_OWNER_LABEL so the aglink-screen the worker spawns can name
	// this conversation in its control-lease messages (docs/control-ownership.md
	// §5). codex has no OAuth token, so oauth is empty here. nil = inherit env.
	if env := workerCmdEnv("", ownerLabel); env != nil {
		cmd.Env = env
	}
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

	useOutFile := r.supportsOutputLastMessage()
	var outFile string
	if useOutFile {
		of, err := os.CreateTemp("", "aglink_route_out_*.txt")
		if err != nil {
			return RouteDecision{}, fmt.Errorf("codex route 출력 임시 파일 생성 실패: %w", err)
		}
		outFile = of.Name()
		of.Close()
		defer os.Remove(outFile)
	}

	prompt := buildRoutePrompt(req)
	args := []string{"exec"}
	if r.supportsIgnoreUserConfig() {
		args = append(args, "--ignore-user-config")
	}
	args = append(args,
		"--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox",
	)
	if r.supportsEphemeral() {
		args = append(args, "--ephemeral")
	}
	args = append(args,
		"--sandbox", "read-only",
		"--json",
	)
	if useOutFile {
		args = append(args, "-o", outFile)
	}
	if m := codexManagerModel(r.cfg()); m != "" {
		args = append(args, "-m", m)
	}
	args = append(args, prompt)

	log.Printf("[codex] route: model=%q projects=%d", codexManagerModel(r.cfg()), len(req.Projects))
	home, _ := os.UserHomeDir()
	stdout, stderr, err := r.exec(routeCtx, home, args, "", "") // router never drives the screen — no owner label
	if err != nil {
		return RouteDecision{}, fmt.Errorf("codex manager 호출 실패: %w (%s)", err, strings.TrimSpace(stderr))
	}

	raw := ""
	if useOutFile {
		content, rerr := os.ReadFile(outFile)
		if rerr != nil {
			return RouteDecision{}, fmt.Errorf("codex route 결과 파일 읽기 실패: %w", rerr)
		}
		raw = string(content)
	} else {
		raw = extractLastAgentMessage(stdout)
	}
	dec, perr := parseCodexRouteDecision(raw)
	if perr != nil {
		log.Printf("[codex] route parse error — raw output: %q", raw)
		return RouteDecision{}, perr
	}
	log.Printf("[codex] route: action=%s project=%q conv=%q", dec.Action, dec.Project, dec.ConversationID)
	return dec, nil
}

// codexRunBaseArgs builds the leading arguments of a worker turn: the exec
// subcommand, the working directory, and — when the conversation already owns a
// codex thread — the resume subcommand that continues it.
//
// Split out from Run so the resume wiring is testable without spawning codex.
// The order matters: codex rejects `exec resume <id> -C <dir>` with "unexpected
// argument '-C'", so -C has to come first.
func codexRunBaseArgs(workDir, sessionID string, resume, ignoreUserConfig bool) []string {
	args := []string{"exec", "-C", workDir}
	if resume && sessionID != "" {
		args = append(args, "resume", sessionID)
	}
	if ignoreUserConfig {
		args = append(args, "--ignore-user-config")
	}
	return append(args,
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check",
		"--json",
	)
}

// newCodexOutFile creates the file codex writes its final message to (-o), one
// per turn.
//
// It used to be named after (pid, SessionID), which is per *conversation*, not
// per turn — and nothing serializes turns of the same web conversation (only the
// telegram stream takes a lock). Two overlapping turns therefore shared one
// file: each codex wrote its answer over the other's, and whichever read first
// could hand the user the other turn's reply. Route already used os.CreateTemp;
// this brings Run in line.
func newCodexOutFile() (string, error) {
	of, err := os.CreateTemp("", "aglink_codex_out_*.txt")
	if err != nil {
		return "", err
	}
	name := of.Name()
	return name, of.Close()
}

// Run executes a worker turn via codex exec.
// Uses a powerful model (codex_model) for actual work.
func (r *codexRunner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	useOutFile := r.supportsOutputLastMessage()
	var outFile string
	if useOutFile {
		f, err := newCodexOutFile()
		if err != nil {
			// Not fatal: fall back to reading the last agent_message off stdout.
			log.Printf("[codex] run: out-file create failed (%v) — reading stdout instead", err)
			useOutFile = false
		} else {
			outFile = f
			defer os.Remove(outFile)
		}
	}

	model := req.Model
	if model == "" {
		model = codexWorkerModel(r.cfg())
	}

	// Continue the conversation's codex thread when we have one. Without this the
	// worker only ever saw the few history turns buildContextPrompt inlines, so a
	// long conversation was answered from a three-turn memory; resuming also lets
	// the server reuse its prompt cache (cached_input_tokens on turn.completed).
	//
	// --ephemeral is what prevented it: an ephemeral turn records no rollout, so
	// `exec resume <id>` fails with "no rollout found". It was there because exec
	// otherwise waits for more stdin after a turn — but only when stdin stays
	// open, and r.exec passes no stdin here, so the turn ends on EOF regardless.
	// Route still uses --ephemeral: a throwaway classification should not leave a
	// rollout behind.
	//
	// -C must precede the resume subcommand; codex rejects `resume <id> -C dir`.
	args := codexRunBaseArgs(req.WorkDir, req.SessionID, req.Resume, r.supportsIgnoreUserConfig())
	if useOutFile {
		args = append(args, "-o", outFile)
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

	stdout, stderr, err := r.exec(ctx, req.WorkDir, args, "", req.OwnerLabel)

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
		if useOutFile {
			if content, rerr := os.ReadFile(outFile); rerr == nil && len(content) > 0 {
				log.Printf("[codex] run: partial output on error (%d bytes)", len(content))
				return RunResult{Text: parseCodexOutput(string(content))}, nil
			}
		} else if text := extractLastAgentMessage(stdout); text != "" {
			log.Printf("[codex] run: partial output on error (%d bytes)", len(text))
			return RunResult{Text: text}, nil
		}
		return RunResult{}, fmt.Errorf("codex worker 실행 실패: %w (%s)", err, strings.TrimSpace(stderr))
	}

	// Extract thread_id for new sessions so the store can persist it.
	threadID := extractThreadID(stdout)
	if !req.Resume && threadID == "" {
		log.Printf("[codex] warning: thread_id not found in JSONL output; session resume may not work")
	}

	var text string
	if useOutFile {
		content, rerr := os.ReadFile(outFile)
		if rerr != nil {
			return RunResult{}, fmt.Errorf("codex 결과 파일 읽기 실패: %w", rerr)
		}
		text = parseCodexOutput(string(content))
	} else {
		text = extractLastAgentMessage(stdout)
	}
	log.Printf("[codex] run done: output=%d bytes session=%s", len(text), threadID)

	result := RunResult{Text: text}
	if !req.Resume && threadID != "" {
		result.SessionID = threadID
	}
	return result, nil
}
