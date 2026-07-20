package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
)

// aglink-screen: standalone agentlink plugin exposing Windows screen control
// (UIA + Win32 + GDI capture + input) to teleclaude. Split out of teleclaude's
// embedded "__mcp-screen" subcommand so it can be versioned/run independently.
//
//	aglink-screen           — MCP stdio server (default; teleclaude points its
//	                          worker's --mcp-config at this binary)
//	aglink-screen mcp       — same, explicit
//	aglink-screen cmd <sub> [args...] [--presets <path>] — fast-path, no LLM;
//	                          prints {"text","image","error"} JSON to stdout
func main() {
	args := os.Args[1:]
	sub := "mcp"
	if len(args) > 0 {
		sub = args[0]
	}

	switch sub {
	case "mcp":
		if err := RunMCPScreen(); err != nil {
			log.Fatal(err)
		}
	case "cmd":
		runCmd(args[1:])
	default:
		fmt.Fprintln(os.Stderr, "usage: aglink-screen [mcp | cmd <subcommand> [args...] [--presets <path>]]")
		os.Exit(2)
	}
}

// cmdResult is the JSON shape printed to stdout by `aglink-screen cmd`.
// Callers (teleclaude's !screen fast-path) check Error first, then Image,
// else fall back to Text.
type cmdResult struct {
	Text  string `json:"text,omitempty"`
	Image string `json:"image,omitempty"` // base64-encoded PNG, when present
	Error string `json:"error,omitempty"`
}

// runCmd parses `--presets <path>` out of rawArgs (in any position), treats
// the first remaining token as the screenCommand subcommand and the rest as
// its args, then prints the result as JSON on stdout.
func runCmd(rawArgs []string) {
	var presetsPath string
	rest := make([]string, 0, len(rawArgs))
	for i := 0; i < len(rawArgs); i++ {
		if rawArgs[i] == "--presets" && i+1 < len(rawArgs) {
			presetsPath = rawArgs[i+1]
			i++
			continue
		}
		rest = append(rest, rawArgs[i])
	}

	if presetsPath == "" {
		p, err := defaultPresetsPath()
		if err != nil {
			emitError(err)
			os.Exit(1)
		}
		presetsPath = p
	}

	if len(rest) == 0 {
		emitError(fmt.Errorf("cmd requires a subcommand (list | shot | region | preset | click)"))
		os.Exit(2)
	}

	text, img, err := screenCommand(rest[0], rest[1:], presetsPath)
	if err != nil {
		emitError(err)
		os.Exit(1)
	}

	out := cmdResult{Text: text}
	if img != nil {
		out.Image = base64.StdEncoding.EncodeToString(img)
	}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}

func emitError(err error) {
	b, _ := json.Marshal(cmdResult{Error: err.Error()})
	fmt.Println(string(b))
}
