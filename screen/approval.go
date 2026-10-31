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
	if name == "run_sequence" {
		if s, ok := args["steps"].(string); ok {
			var steps []struct {
				Action string `json:"action"`
			}
			if json.Unmarshal([]byte(s), &steps) == nil {
				for _, st := range steps {
					add(st.Action)
				}
			}
		}
	}
	sort.Strings(hits)
	return hits
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
