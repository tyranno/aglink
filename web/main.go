package main

import (
	"fmt"
	"log"
	"os"
	"strings"
)

// aglink-web: standalone agentlink plugin giving teleclaude workers control of
// the user's real Chrome browser (list tabs, navigate, extract page text).
//
// Architecture (see README): a Chrome MV3 extension dials OUT to a persistent
// local daemon over a localhost WebSocket, or through the nm-host native
// messaging relay when it is registered. teleclaude keeps its spawn-a-stdio-MCP-server model unchanged by
// spawning this binary's `mcp` subcommand, a thin bridge that forwards tool
// calls to the daemon.
//
//	aglink-web            — MCP stdio server (default; teleclaude points its
//	                        worker's --mcp-config at this binary)
//	aglink-web mcp        — same, explicit
//	aglink-web serve      — the persistent daemon the extension connects to
//	                        (auto-spawned by the bridge if not already running)
//	aglink-web cmd <sub>  — fast-path, no LLM; prints {"text","error"} JSON
//	aglink-web trust-cert <url> — trust an internal site's certificate for good
//	aglink-web nm-host    — Chrome native messaging host (com.aglink.web). Chrome
//	                        runs the binary with the extension's origin as the
//	                        first argument, which selects this too.
func main() {
	args := os.Args[1:]
	sub := "mcp"
	if len(args) > 0 {
		sub = args[0]
	}
	if strings.HasPrefix(sub, "chrome-extension://") {
		sub = "nm-host" // launched by Chrome: a host manifest cannot add arguments
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
	case "trust-cert":
		runTrustCert(args[1:])
	case "token":
		runTokenCmd(args[1:])
	case "nm-host":
		runNMHost(args)
	default:
		fmt.Fprintln(os.Stderr, "usage: aglink-web [mcp | serve | cmd <subcommand> [args...] | trust-cert <https-url> [--yes] | token [--off] | nm-host]")
		os.Exit(2)
	}
}
