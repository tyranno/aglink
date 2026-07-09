package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// cmdResult is the JSON shape printed to stdout by `aglink-web cmd`, matching
// aglink-screen's fast-path contract: callers check Error first, else Text.
type cmdResult struct {
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

// runCmd is the no-LLM fast-path: it maps a subcommand to a daemon call and
// prints the result as JSON. Mirrors aglink-screen's `cmd` so teleclaude's
// !web-style shortcuts can drive the browser without spinning up a worker. The
// available subcommands and their args come from the shared command table in
// command.go, e.g.:
//
//	aglink-web cmd list_tabs
//	aglink-web cmd navigate <url> [tabId]
//	aglink-web cmd get_page_text [tabId] [maxChars]
//	aglink-web cmd click <selector> [tabId]
//	aglink-web cmd screenshot [tabId]   — prints base64 PNG (pipe to a decoder to view)
//	aglink-web cmd type <selector> <text> [tabId]
//	aglink-web cmd close_tab [tabId]
func runCmd(args []string) {
	if len(args) == 0 {
		emitCmd(cmdResult{Error: "cmd requires a subcommand (" + commandNames() + ")"})
		os.Exit(2)
	}

	c, ok := lookupCommand(args[0])
	if !ok {
		emitCmd(cmdResult{Error: fmt.Sprintf("unknown subcommand %q", args[0])})
		os.Exit(2)
	}

	params, err := c.parseCLIArgs(args[1:])
	if err != nil {
		emitCmd(cmdResult{Error: err.Error()})
		os.Exit(2)
	}

	res := callDaemon(c.name, params)
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
