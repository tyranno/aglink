package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestExtractThreadID: JSONL 스트림에서 thread_id 추출
func TestExtractThreadID(t *testing.T) {
	jsonl := "{\"type\":\"thread.started\",\"thread_id\":\"abc-123\"}\n{\"type\":\"turn.started\"}\n{\"type\":\"agent_message\",\"content\":\"hello\"}"

	got := extractThreadID(jsonl)
	if got != "abc-123" {
		t.Errorf("extractThreadID = %q, want %q", got, "abc-123")
	}
}

func TestExtractThreadID_Missing(t *testing.T) {
	jsonl := "{\"type\":\"turn.started\"}\n{\"type\":\"agent_message\",\"content\":\"hello\"}"

	got := extractThreadID(jsonl)
	if got != "" {
		t.Errorf("extractThreadID = %q, want empty", got)
	}
}

func TestParseCodexOutput_Plain(t *testing.T) {
	content := "  hello world  \n"
	got := parseCodexOutput(content)
	if got != "hello world" {
		t.Errorf("parseCodexOutput = %q, want %q", got, "hello world")
	}
}

func TestParseCodexRouteDecision(t *testing.T) {
	raw := "{\"action\":\"new\",\"project\":\"myapp\",\"newTitle\":\"새 기능\"}"
	got, err := parseCodexRouteDecision(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "new" || got.Project != "myapp" || got.NewTitle != "새 기능" {
		t.Errorf("unexpected decision: %+v", got)
	}
}

func TestCodexDefaultModel(t *testing.T) {
	// Empty CodexModel → "" so codex uses its own built-in default.
	cfg := &Config{}
	if codexWorkerModel(cfg) != "" {
		t.Error("expected empty string (let codex choose default)")
	}
	cfg.CodexModel = "o3"
	if codexWorkerModel(cfg) != "o3" {
		t.Error("expected o3")
	}
	// Manager model falls back to worker model when not set.
	cfg.CodexModel = "gpt-5.4"
	cfg.CodexManagerModel = ""
	if codexManagerModel(cfg) != "gpt-5.4" {
		t.Error("expected fallback to worker model")
	}
	cfg.CodexManagerModel = "gpt-4o-mini"
	if codexManagerModel(cfg) != "gpt-4o-mini" {
		t.Error("expected manager model override")
	}
}

func TestRouteDecisionJSONRoundTrip(t *testing.T) {
	dec := RouteDecision{Action: "resume", Project: "p1", ConversationID: "c1"}
	b, _ := json.Marshal(dec)
	var got RouteDecision
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Action != dec.Action || got.Project != dec.Project {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

// TestHelpMentionsIgnoreUserConfig guards a live-found bug: an older codex CLI
// that doesn't support --ignore-user-config rejected every codex-backed turn
// with "error: unexpected argument '--ignore-user-config' found". The runner
// probes `codex exec --help` once and must correctly detect presence/absence
// so it only passes the flag to codex builds that actually accept it.
func TestHelpMentionsIgnoreUserConfig(t *testing.T) {
	newerHelp := "      --ignore-user-config\n          Do not load `$CODEX_HOME/config.toml`; auth still uses `CODEX_HOME`\n"
	olderHelp := "  -C, --cd <DIR>\n          Tell the agent to use the specified directory as its working root\n"

	if !helpMentionsIgnoreUserConfig(newerHelp, "") {
		t.Error("newer codex --help output mentions --ignore-user-config but was not detected")
	}
	if helpMentionsIgnoreUserConfig(olderHelp, "") {
		t.Error("older codex --help output (no --ignore-user-config) was incorrectly detected as supporting it")
	}
	// codex sometimes writes --help to stderr depending on version/platform.
	if !helpMentionsIgnoreUserConfig("", newerHelp) {
		t.Error("--ignore-user-config on stderr was not detected")
	}
	if helpMentionsIgnoreUserConfig("", "") {
		t.Error("empty output (failed probe) must not be reported as supporting the flag")
	}
}

// TestHelpMentionsEphemeral guards a live-found bug: an older codex CLI that
// doesn't support --ephemeral rejected every codex-backed turn with "error:
// unexpected argument '--ephemeral' found". The runner probes `codex exec
// --help` once and must correctly detect presence/absence so it only passes
// the flag to codex builds that actually accept it.
func TestHelpMentionsEphemeral(t *testing.T) {
	newerHelp := "      --ephemeral\n          Run a single non-interactive turn and exit\n"
	olderHelp := "  -C, --cd <DIR>\n          Tell the agent to use the specified directory as its working root\n"

	if !helpMentionsEphemeral(newerHelp, "") {
		t.Error("newer codex --help output mentions --ephemeral but was not detected")
	}
	if helpMentionsEphemeral(olderHelp, "") {
		t.Error("older codex --help output (no --ephemeral) was incorrectly detected as supporting it")
	}
	// codex sometimes writes --help to stderr depending on version/platform.
	if !helpMentionsEphemeral("", newerHelp) {
		t.Error("--ephemeral on stderr was not detected")
	}
	if helpMentionsEphemeral("", "") {
		t.Error("empty output (failed probe) must not be reported as supporting the flag")
	}
}

// TestHelpMentionsOutputLastMessage guards a live-found bug: an older codex
// CLI that doesn't support -o/--output-last-message rejected every
// codex-backed turn with "error: unexpected argument '-o' found". The runner
// probes `codex exec --help` once and must correctly detect presence/absence
// so it only passes the flag to codex builds that actually accept it.
func TestHelpMentionsOutputLastMessage(t *testing.T) {
	newerHelp := "  -o, --output-last-message <FILE>\n          Specifies file where the last message from the agent should be written\n"
	olderHelp := "  -C, --cd <DIR>\n          Tell the agent to use the specified directory as its working root\n"

	if !helpMentionsOutputLastMessage(newerHelp, "") {
		t.Error("newer codex --help output mentions --output-last-message but was not detected")
	}
	if helpMentionsOutputLastMessage(olderHelp, "") {
		t.Error("older codex --help output (no --output-last-message) was incorrectly detected as supporting it")
	}
	// codex sometimes writes --help to stderr depending on version/platform.
	if !helpMentionsOutputLastMessage("", newerHelp) {
		t.Error("--output-last-message on stderr was not detected")
	}
	if helpMentionsOutputLastMessage("", "") {
		t.Error("empty output (failed probe) must not be reported as supporting the flag")
	}
}

// TestExtractLastAgentMessage guards the fallback used when
// supportsOutputLastMessage is false: the final answer must come out of the
// --json NDJSON stream itself, picking the LAST agent_message item (earlier
// ones may just be the agent narrating its plan).
func TestExtractLastAgentMessage(t *testing.T) {
	jsonl := `{"type":"thread.started","thread_id":"abc"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"thinking out loud"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"ls"}}
{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"final answer"}}
{"type":"turn.completed","usage":{}}`

	if got := extractLastAgentMessage(jsonl); got != "final answer" {
		t.Errorf("extractLastAgentMessage() = %q, want %q", got, "final answer")
	}
	if got := extractLastAgentMessage(""); got != "" {
		t.Errorf("extractLastAgentMessage(\"\") = %q, want empty", got)
	}
}

func TestExtractLastAgentMessageLegacyAgentMessageEvent(t *testing.T) {
	jsonl := `{"type":"thread.started","thread_id":"abc"}
{"type":"turn.started"}
{"type":"agent_reasoning","content":"thinking out loud"}
{"type":"agent_message","content":"final answer"}`

	if got := extractLastAgentMessage(jsonl); got != "final answer" {
		t.Errorf("extractLastAgentMessage() = %q, want %q", got, "final answer")
	}
}

// TestParseCodexVersion covers the version-token extraction used by the codex
// readiness notice: it must survive both `codex --version` and `codex exec
// --version` label forms and return "" when no version is present.
func TestParseCodexVersion(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"codex-cli 0.142.5", "0.142.5"},
		{"codex-cli-exec 0.142.5", "0.142.5"},
		{"  codex-cli   1.0.0  \n", "1.0.0"},
		{"codex-cli 0.142.5\ncodex-cli-exec 0.142.5", "0.142.5"},
		{"no version here", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseCodexVersion(c.in); got != c.want {
			t.Errorf("parseCodexVersion(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseCodexModelCatalog(t *testing.T) {
	// Real (trimmed) shape of `codex debug models` output: user-selectable
	// models are "list" visibility; internal ones like the auto-reviewer are
	// "hide" and must not show up in the settings dropdown.
	out := `{"models":[
		{"slug":"gpt-5.6-sol","display_name":"GPT-5.6-Sol","visibility":"list"},
		{"slug":"gpt-5.5","display_name":"GPT-5.5","visibility":"list"},
		{"slug":"codex-auto-review","display_name":"Codex Auto Review","visibility":"hide"}
	]}`
	got := parseCodexModelCatalog(out)
	want := []string{"gpt-5.6-sol", "gpt-5.5"}
	if len(got) != len(want) {
		t.Fatalf("parseCodexModelCatalog() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parseCodexModelCatalog()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseCodexModelCatalog_InvalidJSON(t *testing.T) {
	if got := parseCodexModelCatalog("not json"); got != nil {
		t.Errorf("parseCodexModelCatalog(invalid) = %v, want nil", got)
	}
}

// TestCodexReadinessDecision locks the gate logic: a codex missing
// --ignore-user-config is blocked with upgrade guidance naming the version; a
// ready codex emits a one-time notice on first use and proceeds silently after.
func TestCodexReadinessDecision(t *testing.T) {
	// Not ready → blocked, guidance names the detected version and upgrade cmd.
	ok, msg := codexReadinessDecision(false, "0.10.0", true)
	if ok {
		t.Fatal("missing --ignore-user-config must block the turn")
	}
	if !strings.Contains(msg, "0.10.0") {
		t.Errorf("block guidance should name the detected version, got: %q", msg)
	}
	if !strings.Contains(msg, "npm i -g @openai/codex@latest") {
		t.Errorf("block guidance should include the upgrade command, got: %q", msg)
	}

	// Not ready with unknown version → still blocked, no empty version leak.
	ok, msg = codexReadinessDecision(false, "", true)
	if ok || !strings.Contains(msg, "알 수 없음") {
		t.Errorf("blocked-with-unknown-version = (%v, %q)", ok, msg)
	}

	// Ready + first use → proceed with a one-time notice naming the version.
	ok, msg = codexReadinessDecision(true, "0.142.5", true)
	if !ok {
		t.Fatal("ready codex must proceed")
	}
	if !strings.Contains(msg, "0.142.5") {
		t.Errorf("ready notice should name the version, got: %q", msg)
	}

	// Ready + not first use → proceed silently (no repeated notice).
	ok, msg = codexReadinessDecision(true, "0.142.5", false)
	if !ok || msg != "" {
		t.Errorf("subsequent ready turns must proceed silently, got (%v, %q)", ok, msg)
	}
}

// TestCodexRunnerCheckReadinessNoticeFiresOnce verifies the runner-level
// one-time guard: with the capability probe pre-seeded true (no process
// spawned), the first CheckReadiness returns a notice and the second is silent.
func TestCodexRunnerCheckReadinessNoticeFiresOnce(t *testing.T) {
	r := &codexRunner{}
	// Pre-seed the probes so CheckReadiness doesn't spawn codex.
	r.ignoreUserConfigOnce.Do(func() { r.ignoreUserConfigOK = true })
	r.versionOnce.Do(func() { r.versionStr = "0.142.5" })

	ok, msg := r.CheckReadiness()
	if !ok || msg == "" {
		t.Fatalf("first CheckReadiness = (%v, %q), want ready + notice", ok, msg)
	}
	ok2, msg2 := r.CheckReadiness()
	if !ok2 || msg2 != "" {
		t.Errorf("second CheckReadiness = (%v, %q), want ready + silent", ok2, msg2)
	}
}
