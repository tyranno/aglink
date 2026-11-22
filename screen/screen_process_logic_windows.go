//go:build windows

package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Process listing and termination policy for list_processes / kill_process.
// Logic only (no Win32): the filter, sort, output cap and the list of processes
// kill_process refuses, unit-tested without touching real processes.

const (
	// defaultProcessLimit / ceilingProcessLimit bound one list_processes reply.
	defaultProcessLimit = 50
	ceilingProcessLimit = 500
)

// procInfo is one running process.
type procInfo struct {
	PID   uint32
	PPID  uint32
	Name  string  // executable name, e.g. "notepad.exe"
	MemMB float64 // working set; 0 when it could not be read (access denied)
	Title string  // first visible window title, "" if none
}

// criticalProcesses are executables kill_process never terminates: killing one
// crashes or logs off Windows (or, for the rest, breaks the desktop session).
// Lower-case.
var criticalProcesses = map[string]bool{
	"[system process]":   true,
	"system":             true,
	"secure system":      true,
	"registry":           true,
	"memory compression": true,
	"smss.exe":           true,
	"csrss.exe":          true,
	"wininit.exe":        true,
	"winlogon.exe":       true,
	"services.exe":       true,
	"lsass.exe":          true,
	"lsaiso.exe":         true,
	"svchost.exe":        true,
	"dwm.exe":            true,
	"fontdrvhost.exe":    true,
}

// procNameMatches is kill_process's exact name match: case-insensitive, with
// the ".exe" optional ("notepad" matches notepad.exe) — but never a substring.
func procNameMatches(procName, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	return strings.EqualFold(procName, want) || strings.EqualFold(procName, want+".exe")
}

// killRefusal says why kill_process must not terminate p ("" = allowed). self
// and parent are this server's pid and its parent's (the session driving it).
// force only lifts the explorer.exe guard (the shell restarts, but the
// taskbar/desktop vanish meanwhile); the rest are never killed.
func killRefusal(p procInfo, self, parent uint32, force bool) string {
	name := strings.ToLower(p.Name)
	switch {
	case p.PID <= 4:
		return "a Windows system process"
	case criticalProcesses[name]:
		return "a system-critical process"
	case p.PID == self || strings.HasPrefix(name, "aglink"):
		return "an aglink process"
	case p.PID == parent:
		return "the session that runs this screen server"
	case name == "explorer.exe" && !force:
		return "the Windows shell (pass force=true to restart it)"
	}
	return ""
}

// filterSortProcs keeps the processes whose name or window title contains
// filter (case-insensitive) or whose pid equals it, sorted by memory (largest
// first, the default) or name, and cut to limit. matched is the count before
// the cut.
func filterSortProcs(ps []procInfo, filter, sortBy string, limit int) (out []procInfo, matched int, err error) {
	sortBy = strings.ToLower(strings.TrimSpace(sortBy))
	if sortBy == "" {
		sortBy = "memory"
	}
	if sortBy != "memory" && sortBy != "name" && sortBy != "pid" {
		return nil, 0, fmt.Errorf("sort must be memory, name or pid, got %q", sortBy)
	}
	limit = clampLimit(limit, defaultProcessLimit, ceilingProcessLimit)
	f := strings.ToLower(strings.TrimSpace(filter))
	for _, p := range ps {
		if f == "" || strings.Contains(strings.ToLower(p.Name), f) || strings.Contains(strings.ToLower(p.Title), f) ||
			strconv.FormatUint(uint64(p.PID), 10) == f {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch sortBy {
		case "name":
			if la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name); la != lb {
				return la < lb
			}
		case "memory":
			if a.MemMB != b.MemMB {
				return a.MemMB > b.MemMB
			}
		}
		return a.PID < b.PID
	})
	matched = len(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, matched, nil
}

// formatProcs renders list_processes' reply: one "pid | name | MB | title" line
// per process, with a note when the list was cut.
func formatProcs(ps []procInfo, matched, total int, sortBy string) string {
	if sortBy == "" {
		sortBy = "memory"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d of %d processes match (showing %d, sorted by %s; pid | name | working set | window title):\n", matched, total, len(ps), sortBy)
	for _, p := range ps {
		mem := "?"
		if p.MemMB > 0 {
			mem = fmt.Sprintf("%.1f MB", p.MemMB)
		}
		fmt.Fprintf(&b, "%d | %s | %s", p.PID, p.Name, mem)
		if p.Title != "" {
			fmt.Fprintf(&b, " | %q", onelinePreview(p.Title, 80))
		}
		b.WriteString("\n")
	}
	if matched > len(ps) {
		fmt.Fprintf(&b, "…[%d more not shown — narrow with filter, or raise limit]", matched-len(ps))
	}
	return strings.TrimRight(b.String(), "\n")
}
