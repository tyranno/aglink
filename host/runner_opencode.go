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
	"sync"
	"time"
)

// opencodeRunner implements ClaudeClient backed by the local opencode CLI
// (github.com/sst/opencode — an MIT, open-source Claude-Code-style agent). It is
// the "declarative extension" backend: opencode already contains the agent loop
// (planning, tool use, file reads), so a runner here only shells out to
// `opencode run`, exactly like codexRunner shells out to `codex exec`. The
// provider/model catalog (Anthropic, OpenAI, local Ollama/vLLM/LM Studio via
// OpenAI-compatible baseURL, and free cloud APIs like Groq/Cerebras/Gemini) is
// configured in opencode's OWN opencode.json — aglink only points at it via
// OpencodeConfigPath and selects a "provider/model" reference.
//
// IMPORTANT — CLI-contract verification status:
// opencode is not assumed to be installed on the build machine, so this runner's
// exact `opencode run` flags and stdout shape could NOT be verified live here.
// Every place that depends on opencode's concrete CLI contract is isolated into a
// small pure function tagged "VERIFY:" and gated behind a `--help` capability
// probe (runSupports), mirroring how codexRunner probes `codex exec --help`. On a
// machine WITH opencode installed, confirm each VERIFY note against
// `opencode run --help` before relying on the backend; until then the backend is
// simply inert (findOpencode returns "" → runner never constructed), so shipping
// it cannot regress claude/codex.
type opencodeRunner struct {
	opencodePath string
	cfgh         *ConfigHolder

	// runHelpOnce/Str cache `opencode run --help` output, probed once for
	// capability detection (runSupports) — same approach as codexRunner.
	runHelpOnce sync.Once
	runHelpStr  string

	// versionOnce/Str cache the opencode CLI version for the readiness notice.
	versionOnce sync.Once
	versionStr  string

	// readyNoticeOnce guards the one-time "opencode backend ready" heads-up.
	readyNoticeOnce sync.Once
}

// NewOpencodeRunner builds a ClaudeClient backed by the local opencode CLI.
// Like NewCodexRunner it unwraps a Windows .cmd/.bat shim path via exec when
// needed (handled in exec, not here, since opencode has no nested-native-binary
// layout to resolve the way codex does).
func NewOpencodeRunner(opencodePath string, cfgh *ConfigHolder) *opencodeRunner {
	return &opencodeRunner{opencodePath: opencodePath, cfgh: cfgh}
}

func (r *opencodeRunner) cfg() *Config { return r.cfgh.Get() }

// opencodeWorkerModel returns the "provider/model" reference for actual work
// (Run). "" = let opencode use its configured default model.
func opencodeWorkerModel(cfg *Config) string { return cfg.OpencodeModel }

// opencodeManagerModel returns the "provider/model" reference for routing
// (Route). Falls back to the worker model when unset.
func opencodeManagerModel(cfg *Config) string {
	if cfg.OpencodeManagerModel != "" {
		return cfg.OpencodeManagerModel
	}
	return cfg.OpencodeModel
}

// runHelp probes `opencode run --help` once and caches it. Best-effort: an empty
// string just means every runSupports() check returns false and the runner falls
// back to its most conservative argument set.
func (r *opencodeRunner) runHelp() string {
	r.runHelpOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stdout, stderr, _ := r.exec(ctx, "", []string{"run", "--help"}, "")
		r.runHelpStr = stdout + "\n" + stderr
	})
	return r.runHelpStr
}

// runSupports reports whether `opencode run --help` mentions a flag, so optional
// flags are only passed to CLI versions that accept them (an unknown flag would
// otherwise abort the whole turn). Split from runHelp so the decision is testable.
func (r *opencodeRunner) runSupports(flag string) bool {
	return helpMentionsFlag(r.runHelp(), flag)
}

// helpMentionsFlag is the pure decision behind runSupports.
func helpMentionsFlag(help, flag string) bool {
	return strings.Contains(help, flag)
}

// opencodeVersion detects the CLI version once via `opencode --version`.
func (r *opencodeRunner) opencodeVersion() string {
	r.versionOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stdout, stderr, _ := r.exec(ctx, "", []string{"--version"}, "")
		r.versionStr = parseOpencodeVersion(stdout + " " + stderr)
	})
	return r.versionStr
}

// parseOpencodeVersion pulls the first semver-ish token out of the version
// output (reused pattern from parseCodexVersion). Returns "" when none.
func parseOpencodeVersion(out string) string {
	for _, f := range strings.Fields(out) {
		if len(f) > 0 && f[0] >= '0' && f[0] <= '9' && strings.Contains(f, ".") {
			return f
		}
	}
	return ""
}

// CheckReadiness implements backendReadiness: it surfaces a one-time heads-up on
// the first opencode-backed turn naming the detected version. Unlike codex there
// is no hard capability gate (no flag whose absence breaks every turn is known),
// so it never blocks — it only informs. If opencode later proves to need such a
// gate, add it here the way codexReadinessDecision gates --ignore-user-config.
func (r *opencodeRunner) CheckReadiness() (ok bool, msg string) {
	first := false
	r.readyNoticeOnce.Do(func() { first = true })
	if !first {
		return true, ""
	}
	return true, opencodeReadyNotice(r.opencodeVersion())
}

// opencodeReadyNotice is the one-time first-turn heads-up.
func opencodeReadyNotice(version string) string {
	v := version
	if v == "" {
		v = "확인 불가"
	}
	return "ℹ️ OpenCode 백엔드 v" + v + " 사용 중 — provider·모델은 opencode.json에서 관리됩니다."
}

// opencodeEnv builds the child-process environment: the parent env plus
// OPENCODE_CONFIG (opencode's documented env var pointing at a specific
// opencode.json) when OpencodeConfigPath is set, plus AGLINK_OWNER_LABEL so an
// aglink-screen the worker spawns can name this conversation in its control
// lease. Returns nil ("inherit parent env unchanged") when nothing to add.
func opencodeEnv(configPath, ownerLabel string) []string {
	var extra []string
	if configPath != "" {
		extra = append(extra, "OPENCODE_CONFIG="+configPath)
	}
	if ownerLabel != "" {
		extra = append(extra, "AGLINK_OWNER_LABEL="+ownerLabel)
	}
	if len(extra) == 0 {
		return nil
	}
	return append(os.Environ(), extra...)
}

// opencodeRunArgs builds the argument list for one `opencode run` turn (without
// the trailing prompt, which exec appends). Split out as a pure function so the
// wiring is testable without spawning opencode.
//
// VERIFY (against `opencode run --help` on a machine with opencode installed):
//   - subcommand is `run`
//   - `--model <provider/model>` selects the model
//   - `--session <id>` resumes an existing session (id captured from turn 1)
//   - `--print-logs` / a JSON output flag is what exposes the session id; here we
//     opt into JSON via jsonOut only when the caller detected support, else we
//     read plain stdout text and get no resumable session id.
//
// jsonWanted/sessionSupported/modelSupported are the capability-probe results so
// this stays a pure function (no probing inside).
func opencodeRunArgs(model, sessionID string, resume, jsonWanted, sessionSupported, modelSupported bool) []string {
	args := []string{"run"}
	if jsonWanted {
		// VERIFY: opencode's structured/JSON output flag. If the installed CLI
		// uses a different flag name, adjust here (kept behind a probe so an
		// unsupported flag is never passed).
		args = append(args, "--print-logs")
	}
	if resume && sessionID != "" && sessionSupported {
		args = append(args, "--session", sessionID)
	}
	if model != "" && modelSupported {
		args = append(args, "--model", model)
	}
	return args
}

// parseOpencodeSessionID extracts the session id opencode assigns on the first
// turn so later turns can `--session <id>` to continue the conversation — the
// direct analogue of codex's thread_id (extractThreadID). Returns "" when not
// found (the turn still succeeds; it just won't be resumable).
//
// VERIFY: opencode's session-id output shape. This scans each stdout line for a
// JSON object carrying a session id under the common field names opencode is
// known to use; confirm the actual key/shape once installed and prune the rest.
func parseOpencodeSessionID(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var ev struct {
			SessionID string `json:"sessionID"`
			Session   string `json:"session"`
			ID        string `json:"id"`
			Info      *struct {
				ID string `json:"id"`
			} `json:"info"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		switch {
		case ev.SessionID != "":
			return ev.SessionID
		case ev.Session != "":
			return ev.Session
		case ev.Info != nil && ev.Info.ID != "":
			return ev.Info.ID
		case strings.HasPrefix(ev.ID, "ses"): // opencode session ids are prefixed
			return ev.ID
		}
	}
	return ""
}

// parseOpencodeText extracts the assistant's final answer from opencode's
// output. In plain-text mode opencode prints the answer directly, so the whole
// trimmed stdout is the answer. In JSON/log mode we'd instead pull the last
// assistant message — VERIFY the event shape and implement that branch once the
// real format is known; until then plain-text is the safe default.
func parseOpencodeText(stdout string, jsonMode bool) string {
	if !jsonMode {
		return strings.TrimSpace(stdout)
	}
	// VERIFY: in JSON/log mode, locate the final assistant message. Fallback to
	// trimmed stdout so a format we don't yet parse still returns *something*
	// rather than an empty answer.
	if msg := lastOpencodeAssistantMessage(stdout); msg != "" {
		return msg
	}
	return strings.TrimSpace(stdout)
}

// lastOpencodeAssistantMessage scans JSON/NDJSON lines for the last assistant
// text. VERIFY the event/field names against a real opencode --print-logs stream.
func lastOpencodeAssistantMessage(stdout string) string {
	last := ""
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Role string `json:"role"`
			Text string `json:"text"`
			Part *struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"part"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Role == "assistant" && ev.Text != "" {
			last = ev.Text
		}
		if ev.Part != nil && ev.Part.Type == "text" && ev.Part.Text != "" {
			last = ev.Part.Text
		}
	}
	return strings.TrimSpace(last)
}

// exec runs opencode with process-tree cancellation. stdinData, if non-empty, is
// piped to stdin. Windows .cmd/.bat shims are invoked through cmd.exe /C with the
// double-outer-quote pattern (identical to codexRunner.exec) so spaces-in-path
// survive.
func (r *opencodeRunner) exec(ctx context.Context, dir string, args []string, ownerLabel string) (stdout, stderr string, err error) {
	ext := strings.ToLower(filepath.Ext(r.opencodePath))
	var cmd *exec.Cmd
	if ext == ".cmd" || ext == ".bat" {
		var sb strings.Builder
		sb.WriteString(`"`)
		sb.WriteString(`"`)
		sb.WriteString(r.opencodePath)
		sb.WriteString(`"`)
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
		sb.WriteString(`"`)
		cmd = exec.CommandContext(ctx, "cmd.exe")
		applyCmdLine(cmd, "cmd.exe /C "+sb.String())
	} else {
		cmd = exec.CommandContext(ctx, r.opencodePath, args...)
	}
	cmd.Dir = dir
	if env := opencodeEnv(resolveOpencodeConfigPath(r.cfg()), ownerLabel); env != nil {
		cmd.Env = env
	}
	cmd.Cancel = func() error {
		log.Printf("[opencode] ⚠ cancelling (PID %d) — killing process tree", cmd.Process.Pid)
		killErr := killTree(cmd.Process.Pid)
		killByImageName("opencode" + exeSuffix)
		return killErr
	}

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	// Don't let a process this turn spawned outlive opencode and pin Wait() on the
	// inherited output pipes forever — see workerWaitDelay.
	cmd.WaitDelay = workerWaitDelay

	if startErr := cmd.Start(); startErr != nil {
		return "", "", startErr
	}
	shown := args
	if len(shown) > 4 {
		shown = shown[:4]
	}
	log.Printf("[opencode] started PID %d: %s %s", cmd.Process.Pid, r.opencodePath, strings.Join(shown, " "))

	err = ignoreWaitDelay(cmd.Wait(), "opencode")
	if s := strings.TrimSpace(errBuf.String()); s != "" {
		if len(s) > 500 {
			s = s[:500] + "...(truncated)"
		}
		log.Printf("[opencode] stderr: %s", s)
	}
	if err != nil {
		log.Printf("[opencode] PID %d exited: %v", cmd.Process.Pid, err)
	}
	return outBuf.String(), errBuf.String(), err
}

// Route asks opencode to classify the user message and return a routing decision
// as JSON (parsed by the shared unmarshalDecision). Uses the manager model and a
// 60s timeout. buildRoutePrompt is the same prompt the claude/codex routers use.
func (r *opencodeRunner) Route(ctx context.Context, req RouteRequest) (RouteDecision, error) {
	routeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	model := opencodeManagerModel(r.cfg())
	jsonWanted := r.runSupports("--print-logs")
	args := opencodeRunArgs(model, "", false, jsonWanted, r.runSupports("--session"), r.runSupports("--model"))
	args = append(args, buildRoutePrompt(req))

	log.Printf("[opencode] route: model=%q projects=%d", model, len(req.Projects))
	home, _ := os.UserHomeDir()
	stdout, stderr, err := r.exec(routeCtx, home, args, "") // router never drives the screen — no owner label
	if err != nil {
		return RouteDecision{}, fmt.Errorf("opencode manager 호출 실패: %w (%s)", err, strings.TrimSpace(stderr))
	}
	raw := parseOpencodeText(stdout, jsonWanted)
	dec, ok := unmarshalDecision(raw)
	if !ok {
		log.Printf("[opencode] route parse error — raw output: %q", raw)
		return RouteDecision{}, fmt.Errorf("opencode 라우팅 JSON 파싱 실패: %q", raw)
	}
	log.Printf("[opencode] route: action=%s project=%q conv=%q", dec.Action, dec.Project, dec.ConversationID)
	return dec, nil
}

// Run executes a worker turn via `opencode run`. Uses the worker model. When the
// conversation already owns an opencode session it resumes it (--session);
// otherwise the session id opencode assigns is returned in RunResult.SessionID
// (only on the first turn) so the store persists it — identical lifecycle to the
// codex thread_id path, which is why the existing session plumbing needs no
// changes.
func (r *opencodeRunner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	model := req.Model
	if model == "" {
		model = opencodeWorkerModel(r.cfg())
	}
	jsonWanted := r.runSupports("--print-logs")
	args := opencodeRunArgs(model, req.SessionID, req.Resume, jsonWanted, r.runSupports("--session"), r.runSupports("--model"))
	args = append(args, req.Prompt)

	log.Printf("[opencode] run: model=%q session=%s resume=%v dir=%s prompt=%d chars",
		model, req.SessionID, req.Resume, req.WorkDir, len(req.Prompt))

	stdout, stderr, err := r.exec(ctx, req.WorkDir, args, req.OwnerLabel)
	if err != nil {
		if ctx.Err() != nil {
			log.Printf("[opencode] run: context cancelled/timed out")
			return RunResult{}, ctx.Err()
		}
		// opencode may still have printed a usable answer before a non-zero exit.
		if text := parseOpencodeText(stdout, jsonWanted); text != "" {
			log.Printf("[opencode] run: partial output on error (%d bytes)", len(text))
			return RunResult{Text: text}, nil
		}
		return RunResult{}, fmt.Errorf("opencode worker 실행 실패: %w (%s)", err, strings.TrimSpace(stderr))
	}

	text := parseOpencodeText(stdout, jsonWanted)
	result := RunResult{Text: text}
	if !req.Resume {
		if sid := parseOpencodeSessionID(stdout); sid != "" {
			result.SessionID = sid
		} else {
			log.Printf("[opencode] warning: session id not found in output; resume may not work (VERIFY output format)")
		}
	}
	log.Printf("[opencode] run done: output=%d bytes session=%s", len(text), result.SessionID)
	return result, nil
}
