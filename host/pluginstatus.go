package main

import (
	"fmt"
	"strings"
)

// pluginRun is a plugin's live-process summary, filled by the per-OS
// pluginRunStatuses. total counts every process whose image is <plugin>+exeSuffix;
// serve counts those that look like the persistent "serve" daemon (aglink-web).
type pluginRun struct {
	running bool
	total   int
	serve   int
}

// detail renders a short Korean summary like "serve 데몬 · 프로세스 2개" for the
// UI. Empty when nothing is running. The non-serve count is labelled neutrally
// ("프로세스") rather than "MCP" because Win32_Process CommandLine can be empty
// (session/integrity dependent), in which case serve-vs-mcp can't be confirmed.
func (p pluginRun) detail() string {
	segs := make([]string, 0, 2)
	if p.serve > 0 {
		segs = append(segs, "serve 데몬")
	}
	if other := p.total - p.serve; other > 0 {
		segs = append(segs, fmt.Sprintf("프로세스 %d개", other))
	}
	return strings.Join(segs, " · ")
}
