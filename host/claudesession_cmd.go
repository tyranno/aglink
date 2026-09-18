package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// attachState remembers, per conversation lane, which remote Claude session
// that conversation is talking to — and the last list it was shown, so "2" can
// mean something.
//
// It lives in memory only. A remote session dies well before this host does,
// and a persisted binding to a session that is no longer there is worse than no
// binding: the user would keep typing into nothing. A restart forgets, and
// !status always shows the truth.
type attachState struct {
	mu       sync.Mutex
	attached map[string]SessionInfo   // laneKey → session
	listed   map[string][]SessionInfo // laneKey → the last list shown there
}

func newAttachState() *attachState {
	return &attachState{
		attached: make(map[string]SessionInfo),
		listed:   make(map[string][]SessionInfo),
	}
}

// Attach binds a lane to a session, replacing whatever it had.
func (a *attachState) Attach(lane string, s SessionInfo) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.attached[lane] = s
}

// Detach clears a lane's binding and reports whether there was one.
func (a *attachState) Detach(lane string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, had := a.attached[lane]
	delete(a.attached, lane)
	return had
}

// Current returns the lane's session, if it has one.
func (a *attachState) Current(lane string) (SessionInfo, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.attached[lane]
	return s, ok
}

// Remember stores the list a lane was just shown so its numbering stays valid.
func (a *attachState) Remember(lane string, list []SessionInfo) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.listed[lane] = append([]SessionInfo(nil), list...)
}

// Recall resolves a 1-based number from the lane's last list.
func (a *attachState) Recall(lane string, n int) (SessionInfo, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	list := a.listed[lane]
	if n < 1 || n > len(list) {
		return SessionInfo{}, false
	}
	return list[n-1], true
}

// tailBytes is how much of a transcript's end gets pulled for the one-line
// summary. Large enough to hold a few whole records, small enough that a
// listing over a slow link stays quick.
const tailBytes = 200000

// collectSessions asks every opted-in host what it is running. Failures are
// returned per host rather than aborting: one unreachable machine must not hide
// the sessions on a machine that is up.
func collectSessions(ctx context.Context, cfg *Config) ([]SessionInfo, []string) {
	var all []SessionInfo
	var errs []string
	if cfg == nil || !cfg.SSHEnabled {
		return nil, nil
	}
	for _, h := range cfg.SSHHosts {
		if !h.ClaudeSessions {
			continue
		}
		probeOut, err := runSSHFn(ctx, cfg, h.Name, probeCmd())
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", h.Name, err))
			continue
		}
		probe := parseProbe(probeOut)

		// The name/busy lookup is the only part that costs tokens, and the only
		// part allowed to fail quietly: without it sessions still list, they
		// just cannot be attached to.
		var peers []PeerSession
		if listOut, err := runSSHFn(ctx, cfg, h.Name, claudeListCmd(claudeBinOf(h))); err == nil {
			peers = parsePeerSessions(listOut)
		}

		sessions := mergeSessions(h.Name, probe, peers)
		for i := range sessions {
			if sessions[i].Transcript == "" {
				continue
			}
			out, err := runSSHFn(ctx, cfg, h.Name, tailCmd(sessions[i].Transcript, tailBytes))
			if err != nil {
				continue
			}
			line, _ := tailSummary([]byte(out))
			sessions[i].Last = truncRunes(line, maxLastLine)
		}
		all = append(all, sessions...)
	}
	return all, errs
}

// formatSessionList renders the list the user picks from. The numbering is what
// makes picking work when the user does not know the names: it is valid until
// the next listing.
func formatSessionList(list []SessionInfo) string {
	if len(list) == 0 {
		return "붙을 수 있는 세션이 없습니다."
	}
	var sb strings.Builder
	for i, s := range list {
		state := "쉬는 중"
		if s.Busy {
			state = "일하는 중"
		}
		if !s.Addressable {
			state = "붙을 수 없음(이름을 못 읽음)"
		}
		started := s.Started
		if started == "" {
			started = "?"
		}
		sb.WriteString(fmt.Sprintf("%d. %s  %s  %s · %s\n", i+1, s.Name, s.Host, started, state))
		if s.Last != "" {
			sb.WriteString("   마지막: " + s.Last + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
