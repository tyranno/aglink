package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Approval gate: tools the user marked as risky (aglink host config
// approvals.tools) ask the host before they run. The host passes the list and
// its /approve address in the worker's MCP environment; the bridge forwards
// them with each call (settings.go), and the daemon judges every call by the
// values that came with THAT call — not by the shared last-seen settings — so
// an unrelated call (a user's !flow, `aglink-web cmd`) racing in cannot switch
// the gate off for an agent's call.
//
// A run_steps or run_flow batch is judged as a whole: if any step uses a gated
// tool, the user approves the batch once, with the gated steps listed.

func gatedSet(env map[string]string) map[string]bool {
	if env["AGLINK_APPROVAL_URL"] == "" {
		return nil
	}
	set := map[string]bool{}
	for _, t := range strings.FieldsFunc(env["AGLINK_APPROVAL_TOOLS"], func(r rune) bool { return r == ',' || r == ' ' }) {
		set[strings.TrimSpace(t)] = true
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// toolsOfCall lists the tools a call would run: the method itself, plus each
// step of a batch.
func toolsOfCall(method string, params map[string]any) []string {
	tools := []string{method}
	var steps any
	switch method {
	case "run_steps":
		steps = params["steps"]
	case "run_flow":
		if f, err := loadFlow(strings.TrimSpace(str(params, "name"))); err == nil {
			steps = string(f.Steps)
		}
	}
	if steps != nil {
		if parsed, err := parseSteps(steps); err == nil {
			for _, s := range parsed {
				tools = append(tools, s.Tool)
			}
		}
	}
	return tools
}

// gatedHits is the set of gated tools this call would use, sorted.
func gatedHits(method string, params map[string]any, env map[string]string) []string {
	set := gatedSet(env)
	if set == nil {
		return nil
	}
	seen := map[string]bool{}
	var hits []string
	for _, t := range toolsOfCall(method, params) {
		if set[t] && !seen[t] {
			seen[t] = true
			hits = append(hits, t)
		}
	}
	sort.Strings(hits)
	return hits
}

// approvalClient waits as long as the host lets the user think; the host
// enforces the real timeout.
var approvalClient = &http.Client{Timeout: 15 * time.Minute}

// approvalTarget is the raw target of a direct call to one gated tool — the
// command line for vscode_terminal_run, the command id for vscode_command, a
// URL argument otherwise — which the host normalizes for its "이 대상만 항상
// 허용" scope. A batch has none.
func approvalTarget(hits []string, method string, params map[string]any) string {
	if len(hits) != 1 || hits[0] != method {
		return ""
	}
	switch method {
	case "vscode_terminal_run", "vscode_command":
		return str(params, "command")
	}
	return str(params, "url")
}

// approvalSession is the conversation a call came from, as the host names it
// (AGLINK_OWNER_LABEL): the bridge's session key without its "owner:" prefix.
// A bridge with no owner label sends its per-process key, which still keeps
// one Claude session's "이 대화에서 허용" apart from another's.
func approvalSession(session string) string {
	return strings.TrimPrefix(session, "owner:")
}

// askApproval asks the host and reports whether the call may proceed.
// session is the calling bridge's session key (callRequest.Session).
func askApproval(url, source string, hits []string, method string, params map[string]any, session string) (bool, string) {
	args, _ := json.Marshal(params)
	summary := method
	if len(hits) > 1 || hits[0] != method {
		summary += " (uses: " + strings.Join(hits, ", ") + ")"
	}
	summary += "\n" + oneLine(string(args), 500)
	body, _ := json.Marshal(map[string]string{"source": source, "tool": strings.Join(hits, ","), "summary": summary,
		"target": approvalTarget(hits, method, params), "session": approvalSession(session)})
	resp, err := approvalClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return false, fmt.Sprintf("could not reach the aglink host to ask for approval (%v)", err)
	}
	defer resp.Body.Close()
	var out struct {
		Approved bool   `json:"approved"`
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, "bad answer from the approval endpoint"
	}
	return out.Approved, out.Reason
}
