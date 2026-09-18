package main

import (
	"encoding/json"
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

// maxLastLine caps, in RUNES, how much of a session's own words leave the
// machine. A session's transcript can hold anything that was on screen — this
// morning it held a file of credentials — so the list shows a glance, not the
// content. Bytes would split a Korean character in half; runes do not.
const maxLastLine = 110

// truncRunes cuts s to n runes, appending an ellipsis when it had to cut.
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// shellQuote wraps s in single quotes so the remote shell takes it literally.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// tailCmd reads the LAST n bytes of a transcript. Transcripts run past 30 MB;
// anything that reads one whole is a bug, not a slow path.
func tailCmd(transcript string, n int) string {
	return fmt.Sprintf("tail -c %d %s 2>/dev/null", n, shellQuote(transcript))
}

// transcriptRec is the slice of a transcript record this code needs. The file
// holds far more per line; decoding only these fields keeps the parse cheap and
// stops a schema change elsewhere from breaking the read.
type transcriptRec struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// tailSummary reads a chunk taken from the END of a transcript and returns the
// last assistant text as a single line, plus the timestamp of the last record
// of any kind (which is how "has it gone quiet?" gets answered later).
//
// The first line is almost always cut mid-record, so it is dropped.
func tailSummary(chunk []byte) (string, time.Time) {
	lines := strings.Split(string(chunk), "\n")
	if len(lines) > 0 {
		lines = lines[1:]
	}
	var line string
	var last time.Time
	for _, raw := range lines {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var rec transcriptRec
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			continue
		}
		if ts, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
			last = ts
		}
		if rec.Type != "assistant" {
			continue
		}
		if text := recordText(rec.Message.Content); text != "" {
			line = text
		}
	}
	return collapseSpace(line), last
}

// recordText pulls the visible text out of a record's content, which is either
// a bare string or a list of blocks of which only "text" is shown to a reader.
func recordText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, " ")
}

// collapseSpace folds every run of whitespace into one space so a paragraph
// fits on the single line the list gives it.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
