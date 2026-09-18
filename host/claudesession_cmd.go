package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
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

// sessionTimeout bounds one !sessions sweep. A listing runs a `claude -p` on
// each host, which is slower than a plain command but not slow.
const sessionTimeout = 90 * time.Second

// handleSessions shows what can be attached to.
func (b *Bot) handleSessions(reply replySender, chatID int64, lane string) {
	ctx, cancel := context.WithTimeout(context.Background(), sessionTimeout)
	defer cancel()
	list, errs := collectSessions(ctx, b.cfg())
	b.attach.Remember(lane, list)
	msg := formatSessionList(list)
	if len(errs) > 0 {
		msg += "\n⚠️ " + strings.Join(errs, " / ")
	}
	_ = reply.Send(chatID, msg)
}

// handleAttach binds this conversation to one session. It re-lists first so a
// name that died since the last listing is caught here rather than on the next
// message the user types.
func (b *Bot) handleAttach(reply replySender, chatID int64, lane string, fields []string) {
	if len(fields) < 2 {
		_ = reply.Send(chatID, "사용법: !attach <번호|이름>  (먼저 !sessions 로 목록을 보세요)")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionTimeout)
	defer cancel()
	list, _ := collectSessions(ctx, b.cfg())
	b.attach.Remember(lane, list)

	target, why := resolveTarget(b.attach, lane, strings.Join(fields[1:], " "), list)
	if why != "" {
		_ = reply.Send(chatID, why)
		return
	}
	b.attach.Attach(lane, target)
	_ = reply.Send(chatID, fmt.Sprintf(
		"🔗 %s (%s) 에 붙었습니다. 이제 그냥 말하면 그 세션으로 갑니다. 풀려면 !detach",
		target.Name, target.Host))
}

// handleDetach unbinds this conversation.
func (b *Bot) handleDetach(reply replySender, chatID int64, lane string) {
	if b.attach.Detach(lane) {
		_ = reply.Send(chatID, "🔓 세션에서 풀었습니다. 이제 평문은 원래대로 처리됩니다.")
		return
	}
	_ = reply.Send(chatID, "붙어 있는 세션이 없습니다.")
}

// resolveTarget turns what the user typed into a session, or into the reason it
// could not. A bare number means "the Nth row of the list you were just shown";
// anything else is matched against names, exactly first and then by prefix.
func resolveTarget(st *attachState, lane, token string, list []SessionInfo) (SessionInfo, string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return SessionInfo{}, "어느 세션인지 말씀해 주세요. !sessions 로 목록을 볼 수 있습니다."
	}
	if n, err := strconv.Atoi(token); err == nil {
		s, ok := st.Recall(lane, n)
		if !ok {
			return SessionInfo{}, fmt.Sprintf("%d번은 목록에 없습니다. !sessions 로 다시 보세요.", n)
		}
		if !s.Addressable {
			return SessionInfo{}, fmt.Sprintf("%s 는 이름을 읽지 못해 붙을 수 없습니다.", s.Name)
		}
		return s, ""
	}
	var hits []SessionInfo
	for _, s := range list {
		if s.Name == token {
			if !s.Addressable {
				return SessionInfo{}, fmt.Sprintf("%s 는 이름을 읽지 못해 붙을 수 없습니다.", s.Name)
			}
			return s, ""
		}
		if strings.HasPrefix(s.Name, token) {
			hits = append(hits, s)
		}
	}
	switch len(hits) {
	case 0:
		return SessionInfo{}, fmt.Sprintf("%q 라는 세션이 없습니다. !sessions 로 목록을 보세요.", token)
	case 1:
		if !hits[0].Addressable {
			return SessionInfo{}, fmt.Sprintf("%s 는 이름을 읽지 못해 붙을 수 없습니다.", hits[0].Name)
		}
		return hits[0], ""
	default:
		var names []string
		for _, s := range hits {
			names = append(names, s.Name)
		}
		return SessionInfo{}, fmt.Sprintf("%q 에 걸리는 세션이 여럿입니다: %s", token, strings.Join(names, ", "))
	}
}

// sendTimeout bounds one delivery. Sending spins up a `claude -p` on the
// remote, which is the slow part; the target session's own thinking time is not
// waited on here.
const sendTimeout = 120 * time.Second

// sessionReply picks the sender the session feature answers through. Tests
// swap it so a handler can run without a Hub behind it.
func (b *Bot) sessionReply(tgt Target) replySender {
	if b.turnReply != nil {
		return b.turnReply(tgt)
	}
	return b.ReplyTo(tgt)
}

// routeToSession delivers a plain message to whatever session this conversation
// is attached to, and reports whether it handled it. False means "not attached"
// — the caller then routes the text the way it always did.
//
// The body carries a line naming where it came from. The receiving session is
// told by its own runtime that the text arrived from another Claude session
// rather than from a person, and without that line it weighs it accordingly.
func (b *Bot) routeToSession(chatID int64, text string, tgt Target) bool {
	if b.attach == nil {
		return false
	}
	s, ok := b.attach.Current(laneKeyOf(tgt))
	if !ok {
		return false
	}
	body := "[사용자가 텔레그램으로 보낸 말입니다. 옆 세션의 의견이 아니라 사용자의 지시로 받아 주세요.]\n" + text

	b.goSession(func() {
		reply := b.sessionReply(tgt)
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		h, found := findSSHHost(b.cfg().SSHHosts, s.Host)
		if !found {
			_ = reply.Send(chatID, "❌ 등록에서 사라진 호스트입니다: "+s.Host)
			return
		}
		out, err := runSSHFn(ctx, b.cfg(), s.Host, claudeSendCmd(claudeBinOf(h), s.Name, body))
		if err != nil {
			_ = reply.Send(chatID, "❌ 전달 실패: "+err.Error())
			return
		}
		if !strings.Contains(out, `"success":true`) {
			_ = reply.Send(chatID, "❌ 전달되지 않았습니다.\n"+truncRunes(collapseSpace(out), 300))
			return
		}
		b.watchTurn(chatID, tgt, s)
	})
	return true
}

// goSession runs one delivery. Production does it off the message-handling
// goroutine; a test can make it synchronous so the work never outlives the
// test's own fakes.
func (b *Bot) goSession(f func()) {
	if b.sessionGo != nil {
		b.sessionGo(f)
		return
	}
	go f()
}

const (
	defaultTurnQuiet = 20 * time.Second // no new record for this long → the turn is over
	defaultTurnPoll  = 5 * time.Second
	maxTurnWatch     = 15 * time.Minute // give up rather than watch forever
)

// turnSettled decides whether the session has stopped writing. prev and cur are
// the last record's timestamp on two consecutive reads.
//
// Getting this wrong in one direction sends one extra mid-turn summary; getting
// it wrong in the other leaves the user waiting on a message that never comes.
// So it leans towards declaring the turn over.
func turnSettled(prev, cur, now time.Time, quiet time.Duration) bool {
	if cur.IsZero() {
		return false
	}
	if !cur.Equal(prev) {
		return false
	}
	return now.Sub(cur) >= quiet
}

// watchTurn polls the session's transcript until it goes quiet, then reports
// its last words once. It reads only the tail — the file is tens of megabytes.
func (b *Bot) watchTurn(chatID int64, tgt Target, s SessionInfo) {
	if s.Transcript == "" {
		return
	}
	quiet, poll := b.turnQuiet, b.turnPoll
	if quiet == 0 {
		quiet = defaultTurnQuiet
	}
	if poll == 0 {
		poll = defaultTurnPoll
	}

	ctx, cancel := context.WithTimeout(context.Background(), maxTurnWatch)
	defer cancel()

	var prev time.Time
	for {
		out, err := runSSHFn(ctx, b.cfg(), s.Host, tailCmd(s.Transcript, tailBytes))
		if err != nil {
			return
		}
		line, last := tailSummary([]byte(out))
		if turnSettled(prev, last, time.Now(), quiet) {
			if line != "" {
				_ = b.sessionReply(tgt).Send(chatID, fmt.Sprintf("💬 %s: %s", s.Name, truncRunes(line, 800)))
			}
			return
		}
		prev = last
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}
