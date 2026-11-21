package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Approval gate for screen tools. The aglink host lists the tools the user
// marked as risky (config approvals.tools) and its /approve address in this
// server's environment (AGLINK_APPROVAL_TOOLS / AGLINK_APPROVAL_URL, set in the
// worker's MCP config). A listed tool — or a run_sequence containing a listed
// action — waits for the user's !ok in chat before it runs; anything else runs
// as before.

var approvalClient = &http.Client{Timeout: 15 * time.Minute}

func screenGatedSet() (string, map[string]bool) {
	url := os.Getenv("AGLINK_APPROVAL_URL")
	if url == "" {
		return "", nil
	}
	set := map[string]bool{}
	for _, t := range strings.FieldsFunc(os.Getenv("AGLINK_APPROVAL_TOOLS"), func(r rune) bool { return r == ',' || r == ' ' }) {
		set[strings.TrimSpace(t)] = true
	}
	if len(set) == 0 {
		return "", nil
	}
	return url, set
}

// gatedFlowSteps returns a saved flow's steps JSON so flow_run is gated by the
// actions it replays, like run_sequence. Set where the flow store is created;
// an unknown flow yields "".
var gatedFlowSteps = func(name string) string { return "" }

// screenGatedHits is the gated tools/actions a call would use.
func screenGatedHits(name string, args map[string]any, set map[string]bool) []string {
	seen := map[string]bool{}
	var hits []string
	add := func(t string) {
		if set[t] && !seen[t] {
			seen[t] = true
			hits = append(hits, t)
		}
	}
	add(name)
	var stepsJSON string
	switch name {
	case "run_sequence":
		stepsJSON, _ = args["steps"].(string)
	case "flow_run":
		n, _ := args["name"].(string)
		stepsJSON = gatedFlowSteps(strings.TrimSpace(n))
	}
	if stepsJSON != "" {
		var steps []struct {
			Action string `json:"action"`
		}
		if json.Unmarshal([]byte(stepsJSON), &steps) == nil {
			for _, st := range steps {
				add(st.Action)
			}
		}
	}
	sort.Strings(hits)
	return hits
}

// screenApprovalTarget is the raw target of a direct call to one gated tool —
// the app name for launch_app — which the host normalizes and keys its
// "이 대상만 항상 허용" on. A batch (run_sequence/flow_run) has none. The
// session sent alongside is the conversation label the host gives each worker
// (AGLINK_OWNER_LABEL), so "이 대화에서 허용" knows which conversation asked.
func screenApprovalTarget(name string, args map[string]any, hits []string) string {
	if len(hits) != 1 || hits[0] != name {
		return ""
	}
	switch name {
	case "launch_app":
		s, _ := args["name"].(string)
		return strings.TrimSpace(s)
	}
	return ""
}

func screenApprovalMiddleware(next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		url, set := screenGatedSet()
		if set == nil {
			return next(ctx, req)
		}
		args := req.GetArguments()
		hits := screenGatedHits(req.Params.Name, args, set)
		if len(hits) == 0 {
			return next(ctx, req)
		}
		raw, _ := json.Marshal(args)
		s := string(raw)
		if r := []rune(s); len(r) > 500 {
			s = string(r[:500]) + "…"
		}
		body, _ := json.Marshal(map[string]string{
			"source":  "aglink-screen",
			"tool":    strings.Join(hits, ","),
			"summary": req.Params.Name + "\n" + s,
			"target":  screenApprovalTarget(req.Params.Name, args, hits),
			"session": strings.TrimSpace(os.Getenv("AGLINK_OWNER_LABEL")),
		})
		resp, err := approvalClient.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("not approved — could not reach the aglink host to ask (%v)", err)), nil
		}
		defer resp.Body.Close()
		var out struct {
			Approved bool   `json:"approved"`
			Reason   string `json:"reason"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !out.Approved {
			reason := out.Reason
			if reason == "" {
				reason = "no answer"
			}
			return mcp.NewToolResultError("not approved — " + reason + ". Do not retry this action unless the user asks for it."), nil
		}
		return next(ctx, req)
	}
}
