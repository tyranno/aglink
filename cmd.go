package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// cmdResult is the JSON shape printed to stdout by `aglink-web cmd`, matching
// aglink-screen's fast-path contract: callers check Error first, else Text.
type cmdResult struct {
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

// runCmd is the no-LLM fast-path: it maps a subcommand to a daemon call and
// prints the result as JSON. Mirrors aglink-screen's `cmd` so teleclaude's
// !web-style shortcuts can drive the browser without spinning up a worker.
//
//	aglink-web cmd list_tabs
//	aglink-web cmd navigate <url> [tabId]
//	aglink-web cmd get_page_text [tabId] [maxChars]
func runCmd(args []string) {
	if len(args) == 0 {
		emitCmd(cmdResult{Error: "cmd requires a subcommand (list_tabs | navigate | get_page_text)"})
		os.Exit(2)
	}

	var res CallResult
	switch args[0] {
	case "list_tabs":
		res = callDaemon("list_tabs", nil)
	case "navigate":
		if len(args) < 2 {
			emitCmd(cmdResult{Error: "navigate requires a <url>"})
			os.Exit(2)
		}
		params := map[string]any{"url": args[1]}
		if len(args) >= 3 {
			if tabID, err := strconv.Atoi(args[2]); err == nil {
				params["tabId"] = tabID
			}
		}
		res = callDaemon("navigate", params)
	case "get_page_text":
		params := map[string]any{}
		if len(args) >= 2 {
			if tabID, err := strconv.Atoi(args[1]); err == nil {
				params["tabId"] = tabID
			}
		}
		if len(args) >= 3 {
			if maxChars, err := strconv.Atoi(args[2]); err == nil {
				params["maxChars"] = maxChars
			}
		}
		res = callDaemon("get_page_text", params)
	default:
		emitCmd(cmdResult{Error: fmt.Sprintf("unknown subcommand %q", args[0])})
		os.Exit(2)
	}

	if res.Error != "" {
		emitCmd(cmdResult{Error: res.Error})
		os.Exit(1)
	}
	emitCmd(cmdResult{Text: res.Text})
}

func emitCmd(r cmdResult) {
	b, _ := json.Marshal(r)
	fmt.Println(string(b))
}
