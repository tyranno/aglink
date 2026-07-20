package main

import (
	"fmt"
	"log"
	"os"
)

// aglink-web: standalone agentlink plugin giving teleclaude workers control of
// the user's real Chrome browser (list tabs, navigate, extract page text).
//
// Architecture (see README): a Chrome MV3 extension dials OUT to a persistent
// local daemon over a localhost WebSocket — no Native Messaging, no registry
// manifest. teleclaude keeps its spawn-a-stdio-MCP-server model unchanged by
// spawning this binary's `mcp` subcommand, a thin bridge that forwards tool
// calls to the daemon.
//
//	aglink-web            — MCP stdio server (default; teleclaude points its
//	                        worker's --mcp-config at this binary)
//	aglink-web mcp        — same, explicit
//	aglink-web serve      — the persistent daemon the extension connects to
//	                        (auto-spawned by the bridge if not already running)
//	aglink-web cmd <sub>  — fast-path, no LLM; prints {"text","error"} JSON
func main() {
	args := os.Args[1:]
	sub := "mcp"
	if len(args) > 0 {
		sub = args[0]
	}

	switch sub {
	case "mcp":
		if err := RunMCPWeb(); err != nil {
			log.Fatal(err)
		}
	case "serve":
		if err := runDaemon(); err != nil {
			log.Fatal(err)
		}
	case "cmd":
		runCmd(args[1:])
	default:
		fmt.Fprintln(os.Stderr, "usage: aglink-web [mcp | serve | cmd <subcommand> [args...]]")
		os.Exit(2)
	}
}
