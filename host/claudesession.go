package main

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PeerSession is one Claude session the remote's own `claude -p` + ListAgents
// reported. It carries the two things the shell probe cannot know: the
// session's addressable NAME (what SendMessage's `to:` takes) and whether it is
// busy right now.
type PeerSession struct {
	Name    string // addressable name, e.g. "proj-a-cf"
	Ref     string // short ref shown in brackets
	Kind    string // "interactive" for a VS Code or terminal session
	Started string // human text, e.g. "1h ago"
	Busy    bool
}

// parsePeerSessions extracts sessions from ListAgents output. That output
// arrives wrapped in whatever the relaying `claude -p` chose to print around
// it — code fences, a preamble, a closing sentence — so anything that does not
// look like a session row is dropped without complaint. A row looks like:
//
//	proj-a-cf [875607]  ·  interactive  ·  idle  ·  started 1h ago
func parsePeerSessions(out string) []PeerSession {
	var sessions []PeerSession
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		open := strings.Index(line, "[")
		shut := strings.Index(line, "]")
		if open <= 0 || shut <= open {
			continue
		}
		name := strings.TrimSpace(line[:open])
		if name == "" || strings.ContainsAny(name, " \t") {
			continue
		}
		s := PeerSession{Name: name, Ref: strings.TrimSpace(line[open+1 : shut])}
		for _, f := range strings.Split(line[shut+1:], "·") {
			f = strings.TrimSpace(f)
			switch {
			case f == "busy":
				s.Busy = true
			case f == "idle":
				s.Busy = false
			case strings.HasPrefix(f, "started "):
				s.Started = strings.TrimSpace(strings.TrimPrefix(f, "started "))
			case f == "interactive" || f == "background":
				s.Kind = f
			}
		}
		sessions = append(sessions, s)
	}
	return sessions
}

// remoteSession is one live Claude process the shell probe found. Everything
// here is measured, not inferred: the pid owns the socket, the cwd comes from
// /proc, and the transcript is the newest .jsonl in the project directory that
// the cwd encodes.
type remoteSession struct {
	PID        int
	Cwd        string
	Transcript string
	StartedAt  time.Time
}

// probeCmd is the remote shell that lists live sessions. It costs no tokens and
// its output is deterministic, which is why the whole read path avoids an LLM.
// It only ever stats the transcript — reading a 30 MB file here would be a bug,
// and a test guards against it.
//
// Session sockets are 0600 under /run/user/<uid>, so this sees exactly the
// sessions the SSH account itself started. That boundary is intentional.
func probeCmd() string {
	return `for s in /run/user/$(id -u)/cc-socks/*.sock; do ` +
		`p=${s##*/}; p=${p%.sock}; ` +
		`[ -d /proc/$p ] || continue; ` +
		`cwd=$(readlink /proc/$p/cwd 2>/dev/null) || continue; ` +
		`[ -n "$cwd" ] || continue; ` +
		`enc=$(printf %s "$cwd" | sed "s#[/_.]#-#g"); ` +
		`f=$(ls -t "$HOME/.claude/projects/$enc"/*.jsonl 2>/dev/null | head -1); ` +
		`st=$(stat -c %Y /proc/$p 2>/dev/null); ` +
		`echo "$p|$cwd|$f|$st"; done`
}

// parseProbe turns probeCmd's output into sessions, dropping any line the shell
// or a login banner may have mixed in.
func parseProbe(out string) []remoteSession {
	var sessions []remoteSession
	for _, raw := range strings.Split(out, "\n") {
		parts := strings.Split(strings.TrimSpace(raw), "|")
		if len(parts) < 4 {
			continue
		}
		pid, err := strconv.Atoi(parts[0])
		if err != nil || pid <= 0 {
			continue
		}
		s := remoteSession{PID: pid, Cwd: parts[1], Transcript: parts[2]}
		if secs, err := strconv.ParseInt(parts[3], 10, 64); err == nil && secs > 0 {
			s.StartedAt = time.Unix(secs, 0)
		}
		sessions = append(sessions, s)
	}
	return sessions
}

// SessionInfo is one row of what !sessions shows: the shell probe's facts and
// the LLM-sourced name/busy joined together.
//
// Addressable says whether SendMessage can reach it. A session the probe found
// but ListAgents did not name cannot be messaged — it is still listed, because
// hiding a session the user can see in their own editor is worse than showing
// one they cannot talk to.
type SessionInfo struct {
	Host        string // ssh.hosts registry name it was found on
	Name        string // addressable name, or the cwd's basename as a label
	Cwd         string
	Transcript  string
	Started     string
	Last        string // one line from the transcript tail
	PID         int
	Busy        bool
	Addressable bool
}

// mergeSessions joins the shell probe to ListAgents by the session name's
// prefix. Names are generated from the working directory's basename with a
// short suffix ("…/proj-a" → "proj-a-cf"), which is what makes the join
// possible. A custom name set with `claude -n` breaks the join; both sides then
// survive as separate rows rather than one of them vanishing.
func mergeSessions(host string, probe []remoteSession, peers []PeerSession) []SessionInfo {
	used := make(map[int]bool, len(peers))
	out := make([]SessionInfo, 0, len(probe)+len(peers))

	for _, p := range probe {
		base := path.Base(p.Cwd)
		info := SessionInfo{
			Host: host, Name: base, Cwd: p.Cwd,
			Transcript: p.Transcript, PID: p.PID,
		}
		if !p.StartedAt.IsZero() {
			info.Started = humanSince(p.StartedAt)
		}
		for i, peer := range peers {
			if used[i] || !strings.HasPrefix(peer.Name, base+"-") {
				continue
			}
			used[i] = true
			info.Name = peer.Name
			info.Busy = peer.Busy
			info.Addressable = true
			if peer.Started != "" {
				info.Started = peer.Started
			}
			break
		}
		out = append(out, info)
	}

	for i, peer := range peers {
		if used[i] {
			continue
		}
		out = append(out, SessionInfo{
			Host: host, Name: peer.Name, Started: peer.Started,
			Busy: peer.Busy, Addressable: true,
		})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// humanSince renders an uptime the way the session list shows it.
func humanSince(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "방금"
	case d < time.Hour:
		return fmt.Sprintf("%d분째", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d시간째", int(d.Hours()))
	}
}
