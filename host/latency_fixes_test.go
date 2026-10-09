package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- fix 1: one-shot manager calls run without extended thinking ----

func TestOneShotManagerArgs_DisableThinking(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra []string
	}{
		{"summarize", nil},
		{"route", []string{"--json-schema", routeJSONSchema}},
	} {
		args := oneShotManagerArgs(&Config{ManagerModel: "haiku"}, tc.extra...)
		idx := -1
		for i, a := range args {
			if a == "--settings" {
				idx = i
			}
		}
		if idx < 0 || idx+1 >= len(args) {
			t.Fatalf("%s: --settings missing: %v", tc.name, args)
		}
		var s map[string]any
		if err := json.Unmarshal([]byte(args[idx+1]), &s); err != nil {
			t.Fatalf("%s: --settings value is not JSON: %q", tc.name, args[idx+1])
		}
		if v, ok := s["alwaysThinkingEnabled"].(bool); !ok || v {
			t.Errorf("%s: alwaysThinkingEnabled must be false, got %v", tc.name, s)
		}
		joined := strings.Join(args, " ")
		for _, want := range []string{"-p --output-format json", "--strict-mcp-config", "--tools ", "--system-prompt", "--model haiku"} {
			if !strings.Contains(joined, want) {
				t.Errorf("%s: missing %q in %v", tc.name, want, args)
			}
		}
		if tc.extra != nil && !strings.Contains(joined, "--json-schema") {
			t.Errorf("route args lost --json-schema: %v", args)
		}
	}
	if args := oneShotManagerArgs(&Config{}); strings.Contains(strings.Join(args, " "), "--model") {
		t.Errorf("no manager model configured → no --model: %v", args)
	}
}

// ---- fix 2: reuse an existing summary instead of a synchronous refresh ----

func TestSummaryGapReusable(t *testing.T) {
	small := historyN(6)
	if !summaryGapReusable(small) {
		t.Error("≤ summaryReuseMaxTurns turns must be reusable")
	}
	manyTiny := historyN(20)
	if !summaryGapReusable(manyTiny) {
		t.Error("many turns within the char budget must be reusable")
	}
	big := historyN(7)
	for i := range big {
		big[i].Response = strings.Repeat("x", 2000)
	}
	if summaryGapReusable(big) {
		t.Error("7 turns over the char budget must not be reusable")
	}
}

func TestSummaryForFreshSession_ReusesSummaryForSmallGap(t *testing.T) {
	fc := &scriptedClient{summary: "REFRESHED", summDone: make(chan struct{}, 1)}
	m, _ := tokenFixture(t, fc, &Config{SummaryOnReset: true})
	h := historyN(20)
	c := &Conversation{ID: "c1", History: h, RollingSummary: "OLD", RollingSummaryUpTo: h[10].Timestamp}

	sum, tail, ok := m.summaryForFreshSession(context.Background(), "k", c, h)
	if !ok || sum != "OLD" {
		t.Fatalf("expected reuse of the existing summary, got ok=%v sum=%q", ok, sum)
	}
	// Uncovered turns 11..14 + the verbatim tail 15..19.
	if len(tail) != 9 || tail[0].Prompt != "PROMPT-11" || tail[len(tail)-1].Prompt != "PROMPT-19" {
		t.Fatalf("tail must be uncovered turns + recent tail, got %d from %s", len(tail), tail[0].Prompt)
	}
	// The refresh runs in the background, folding in exactly the uncovered turns.
	select {
	case <-fc.summDone:
	case <-time.After(5 * time.Second):
		t.Fatal("async refresh never ran")
	}
	fc.mu.Lock()
	in := fc.summCalls[0]
	fc.mu.Unlock()
	if !strings.Contains(in, "OLD") || !strings.Contains(in, "PROMPT-11") || !strings.Contains(in, "PROMPT-14") || strings.Contains(in, "PROMPT-15") {
		t.Errorf("async refresh input wrong:\n%s", in)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if s, upTo := m.bestSummary("k", c); s == "REFRESHED" && upTo.Equal(h[14].Timestamp) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refreshed summary was not cached")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSummaryForFreshSession_SyncWhenNoSummaryOrLargeGap(t *testing.T) {
	// No summary at all → synchronous.
	fc := &scriptedClient{summary: "NEW"}
	m, _ := tokenFixture(t, fc, &Config{SummaryOnReset: true})
	h := historyN(20)
	c := &Conversation{ID: "c1", History: h}
	sum, tail, ok := m.summaryForFreshSession(context.Background(), "k", c, h)
	if !ok || sum != "NEW" || len(fc.summCalls) != 1 || len(tail) != summaryTailTurns {
		t.Fatalf("no summary → sync call: ok=%v sum=%q calls=%d tail=%d", ok, sum, len(fc.summCalls), len(tail))
	}

	// Existing summary but a large gap → synchronous.
	fc2 := &scriptedClient{summary: "NEW2"}
	m2, _ := tokenFixture(t, fc2, &Config{SummaryOnReset: true})
	big := historyN(20)
	for i := range big {
		big[i].Response = strings.Repeat("y", 2000)
	}
	c2 := &Conversation{ID: "c2", History: big, RollingSummary: "OLD", RollingSummaryUpTo: big[0].Timestamp}
	sum, tail, ok = m2.summaryForFreshSession(context.Background(), "k2", c2, big)
	if !ok || sum != "NEW2" || len(fc2.summCalls) != 1 {
		t.Fatalf("large gap → sync call: ok=%v sum=%q calls=%d", ok, sum, len(fc2.summCalls))
	}
	if len(tail) > summaryTailTurns {
		t.Errorf("sync path injects only the tail, got %d turns", len(tail))
	}
	if c2.RollingSummary != "NEW2" || !c2.RollingSummaryUpTo.Equal(big[14].Timestamp) {
		t.Errorf("sync summary not recorded on conv: %q %v", c2.RollingSummary, c2.RollingSummaryUpTo)
	}
}

func TestRunWorker_ResetReusesSummaryWithoutSyncCall(t *testing.T) {
	// The summarizer blocks until released: a synchronous call would deadlock the
	// turn, so the turn completing proves the summary was not awaited.
	release := make(chan struct{})
	fc := &blockingSummaryClient{scriptedClient: scriptedClient{results: []RunResult{{Text: "ok", SessionID: "fresh", ContextTokens: 4000}}}, release: release}
	m, st := tokenFixture(t, fc, &Config{SummaryOnReset: true})
	h := historyN(20)
	seedTelegram(t, st, func(c *Conversation) {
		c.History = h
		c.ClaudeContextTokens = 200_000
		c.RollingSummary, c.RollingSummaryUpTo = "OLD-SUM", h[12].Timestamp
	})

	done := make(chan struct{})
	go func() {
		m.HandleWebTarget(context.Background(), 1, "다음 작업", TelegramTarget(), &fakeSender{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("reset turn waited on the summarizer")
	}
	close(release)

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.calls) != 1 || fc.calls[0].Resume {
		t.Fatalf("expected one fresh (reset) run, got %+v", fc.calls)
	}
	p := fc.calls[0].Prompt
	for _, want := range []string{"OLD-SUM", "PROMPT-13", "PROMPT-19"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "PROMPT-12") {
		t.Errorf("turns covered by the summary must not be inlined:\n%s", p)
	}
	if tc := st.TelegramConversation(); !tc.TurnStats[len(tc.TurnStats)-1].SummaryUsed {
		t.Error("turn stat must record SummaryUsed")
	}
}

type blockingSummaryClient struct {
	scriptedClient
	release chan struct{}
}

func (c *blockingSummaryClient) Summarize(ctx context.Context, prompt string) (string, error) {
	select {
	case <-c.release:
	case <-ctx.Done():
	}
	return "", errors.New("released")
}

// ---- fix 3: a failed reset turn must not cause a second reset ----

// errQueueClient returns queued (result, error) pairs in order, then "ok".
type errQueueClient struct {
	mu    sync.Mutex
	calls []RunRequest
	steps []struct {
		res RunResult
		err error
	}
	summCalls int
}

func (c *errQueueClient) push(res RunResult, err error) {
	c.steps = append(c.steps, struct {
		res RunResult
		err error
	}{res, err})
}

func (c *errQueueClient) Route(context.Context, RouteRequest) (RouteDecision, error) {
	return RouteDecision{}, errors.New("not used")
}

func (c *errQueueClient) Run(_ context.Context, req RunRequest) (RunResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, req)
	if len(c.steps) == 0 {
		return RunResult{Text: "ok"}, nil
	}
	s := c.steps[0]
	c.steps = c.steps[1:]
	return s.res, s.err
}

func (c *errQueueClient) Summarize(context.Context, string) (string, error) {
	c.mu.Lock()
	c.summCalls++
	c.mu.Unlock()
	return "SUM", nil
}

func TestRunWorker_FailedResetTurnDoesNotResetAgain(t *testing.T) {
	for _, tc := range []struct {
		name string
		// second turn's CLI behavior for the never-confirmed new session id
		inUse bool
	}{
		{"session never created", false},
		{"session created by the failed turn", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := &errQueueClient{}
			fc.push(RunResult{}, errors.New("worker crashed")) // turn 1: the reset turn fails
			if tc.inUse {
				fc.push(RunResult{}, errors.New("Error: Session ID abc is already in use"))
			}
			fc.push(RunResult{Text: "ok", SessionID: "", ContextTokens: 8000}, nil)
			m, st := tokenFixture(t, fc, &Config{SummaryOnReset: true})
			seedTelegram(t, st, func(c *Conversation) { c.History = historyN(20); c.ClaudeContextTokens = 200_000 })

			s1 := &fakeSender{}
			m.HandleWebTarget(context.Background(), 1, "작업 1", TelegramTarget(), s1)
			after := st.TelegramConversation()
			if after.Started || after.ClaudeContextTokens != 0 {
				t.Fatalf("failed reset turn must persist Started=false and a zeroed counter, got started=%v ctx=%d", after.Started, after.ClaudeContextTokens)
			}
			newID := after.SessionID
			if newID == "" || newID == "live-session" || fc.calls[0].SessionID != newID {
				t.Fatalf("reset must persist the minted session id: stored %q, ran %q", newID, fc.calls[0].SessionID)
			}
			fc.mu.Lock()
			summAfterFirst := fc.summCalls
			fc.mu.Unlock()
			if summAfterFirst != 1 {
				t.Fatalf("reset turn should summarize once (no summary yet), got %d", summAfterFirst)
			}

			s2 := &fakeSender{}
			m.HandleWebTarget(context.Background(), 1, "작업 2", TelegramTarget(), s2)
			for _, msg := range s2.sent {
				if strings.Contains(msg, "♻️") {
					t.Errorf("second turn reset again: %q", msg)
				}
				if strings.Contains(msg, "📂") {
					t.Errorf("existing conversation must not get a new-conversation header: %q", msg)
				}
			}
			fc.mu.Lock()
			calls := append([]RunRequest(nil), fc.calls...)
			fc.mu.Unlock()
			second := calls[1]
			if second.Resume || second.SessionID != newID {
				t.Errorf("next turn must create the minted session fresh (--session-id), got resume=%v id=%q", second.Resume, second.SessionID)
			}
			if tc.inUse {
				if len(calls) != 3 || !calls[2].Resume || calls[2].SessionID != newID {
					t.Fatalf("already-in-use must fall back to --resume of the same id: %+v", calls)
				}
			}
			final := st.TelegramConversation()
			if !final.Started || final.SessionID != newID || final.ClaudeContextTokens != 8000 {
				t.Errorf("successful turn must settle the session: started=%v id=%q ctx=%d", final.Started, final.SessionID, final.ClaudeContextTokens)
			}
		})
	}
}

func TestMarkSessionReset(t *testing.T) {
	c := &Conversation{Started: true, ClaudeContextTokens: 1, CodexContextTokens: 2, SessionID: "x"}
	markSessionReset(c)
	if c.Started || c.ClaudeContextTokens != 0 || c.CodexContextTokens != 0 || c.SessionID != "x" {
		t.Errorf("markSessionReset: %+v", c)
	}
}

// ---- fix 4: Notion MCP from a global install, without npx ----

func writeFakePkg(t *testing.T, root, pkg, packageJSON string, files ...string) string {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(pkg))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(packageJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("// entry"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestFindGlobalPackageBin(t *testing.T) {
	const pkg = "@notionhq/notion-mcp-server"
	missing := filepath.Join(t.TempDir(), "nope")

	// bin as a map, entry named after the package → that entry; first root missing.
	root := t.TempDir()
	dir := writeFakePkg(t, root, pkg, `{"bin":{"other":"bin/other.js","notion-mcp-server":"bin/cli.mjs"}}`, "bin/cli.mjs", "bin/other.js")
	got, ok := findGlobalPackageBin([]string{"", missing, root}, pkg)
	if want := filepath.Join(dir, "bin", "cli.mjs"); !ok || got != want {
		t.Fatalf("map bin: got %q ok=%v, want %q", got, ok, want)
	}

	// bin as a string.
	root2 := t.TempDir()
	dir2 := writeFakePkg(t, root2, pkg, `{"bin":"dist/index.js"}`, "dist/index.js")
	if got, ok := findGlobalPackageBin([]string{root2}, pkg); !ok || got != filepath.Join(dir2, "dist", "index.js") {
		t.Errorf("string bin: got %q ok=%v", got, ok)
	}

	// map without a matching name → alphabetically first entry.
	root3 := t.TempDir()
	dir3 := writeFakePkg(t, root3, pkg, `{"bin":{"zeta":"z.js","alpha":"a.js"}}`, "a.js", "z.js")
	if got, ok := findGlobalPackageBin([]string{root3}, pkg); !ok || got != filepath.Join(dir3, "a.js") {
		t.Errorf("fallback bin: got %q ok=%v", got, ok)
	}

	// entry file missing / no bin / not installed → not found (caller keeps npx).
	root4 := t.TempDir()
	writeFakePkg(t, root4, pkg, `{"bin":{"notion-mcp-server":"bin/cli.mjs"}}`)
	root5 := t.TempDir()
	writeFakePkg(t, root5, pkg, `{"name":"x"}`)
	if got, ok := findGlobalPackageBin([]string{root4, root5, missing}, pkg); ok {
		t.Errorf("unusable installs must not resolve, got %q", got)
	}
}

func TestNpmGlobalRootCandidates(t *testing.T) {
	t.Setenv("APPDATA", filepath.Join("C:", "Users", "u", "AppData", "Roaming"))
	node := filepath.Join("C:", "Program Files", "nodejs", "node.exe")
	got := npmGlobalRootCandidates(node)
	want := []string{
		filepath.Join("C:", "Users", "u", "AppData", "Roaming", "npm", "node_modules"),
		filepath.Join("C:", "Program Files", "nodejs", "node_modules"),
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
			}
		}
		if !found {
			t.Errorf("candidates %v missing %q", got, w)
		}
	}
}

func TestBuildMCPServerList_NotionUsesResolvedLaunch(t *testing.T) {
	cmd, args := notionMCPLaunch()
	list := buildMCPServerList(&Config{NotionControl: true, NotionToken: "ntn_x"}, "", "", "")
	if len(list) != 1 || list[0].Name != "notion" || list[0].Command != cmd || strings.Join(list[0].Args, " ") != strings.Join(args, " ") {
		t.Fatalf("notion entry = %+v, want %s %v", list, cmd, args)
	}
	if cmd == "npx" {
		if strings.Join(args, " ") != "-y "+notionMCPPackage {
			t.Errorf("npx fallback args = %v", args)
		}
	} else if len(args) != 1 || !strings.Contains(filepath.ToSlash(args[0]), notionMCPPackage) {
		t.Errorf("direct launch must run the package's bin entry, got %s %v", cmd, args)
	}
	if list[0].Env["NOTION_TOKEN"] != "ntn_x" {
		t.Error("NOTION_TOKEN not passed")
	}
}

// ---- fix 5: manager_always is dead config ----

func TestManagerAlways_NotInSettingsSchemaButYAMLStillLoads(t *testing.T) {
	for _, sec := range buildSettings(&Config{ManagerAlways: true}, nil) {
		for _, f := range sec.Fields {
			if f.Key == "models.manager_always" {
				t.Errorf("models.manager_always must not be in the settings schema (section %q)", sec.Title)
			}
		}
	}
	y := []byte("telegram:\n  bot_token: t\n  allowed_user_ids: [1]\nmodels:\n  manager: haiku\n  manager_always: true\n")
	cfg, err := unmarshalConfigYAML(y)
	if err != nil {
		t.Fatalf("legacy manager_always must still load: %v", err)
	}
	if cfg.ManagerModel != "haiku" {
		t.Errorf("ManagerModel = %q", cfg.ManagerModel)
	}
	if _, err := marshalConfigYAML(cfg); err != nil {
		t.Errorf("saving a config loaded with manager_always: %v", err)
	}
}
