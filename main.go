package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: aglink-chat <serve>")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "serve":
		serveCmd(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func serveCmd(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:1717", "browser-facing HTTP/WS address")
	controlAddr := fs.String("control-addr", "127.0.0.1:17170", "teleclaude control-API address to connect to")
	_ = fs.Parse(args)

	fmt.Printf("aglink-chat serve: addr=%s control-addr=%s (scaffold — not yet implemented)\n", *addr, *controlAddr)
	os.Exit(1)
}
