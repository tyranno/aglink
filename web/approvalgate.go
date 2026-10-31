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

// askApproval asks the host and reports whether the call may proceed.
func askApproval(url, source string, hits []string, method string, params map[string]any) (bool, string) {
	args, _ := json.Marshal(params)
	summary := method
	if len(hits) > 1 || hits[0] != method {
		summary += " (uses: " + strings.Join(hits, ", ") + ")"
	}
	summary += "\n" + oneLine(string(args), 500)
	body, _ := json.Marshal(map[string]string{"source": source, "tool": strings.Join(hits, ","), "summary": summary})
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
