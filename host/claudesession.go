package main

import "strings"

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
