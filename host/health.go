package main

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// GET /health — is this host actually working, not just running?
//
// On 2026-09-07 the host stopped answering Telegram and nobody noticed for
// eleven days: the watchdog's only test was "is there an aglink.exe process",
// and the long-poll loop logs nothing while it is healthy. This endpoint
// answers the real question — has the Telegram poll loop come back from
// getUpdates recently — so a watchdog (scripts/aglink-always-on.ps1, a
// systemd timer, curl over SSH) can tell a hung host from a live one.
//
// Loopback only, no secrets in the reply. Address: AGLINK_HEALTH_ADDR, default
// 127.0.0.1:27269; "off" disables it.

const (
	defaultHealthAddr = "127.0.0.1:27269"
	// getUpdates returns at least every 30s even with no messages; three
	// minutes without a successful return means the loop is stuck or the
	// network/token has been failing for a while.
	pollStaleAfter = 3 * time.Minute
)

var healthState = struct {
	sync.Mutex
	startedAt     time.Time
	telegram      bool
	lastPollOK    time.Time
	lastPollErr   string
	lastPollErrAt time.Time
	lastTurnEnd   time.Time
}{startedAt: time.Now()}

func healthTelegramEnabled(on bool) {
	healthState.Lock()
	healthState.telegram = on
	healthState.Unlock()
}

func notePollOK() {
	healthState.Lock()
	healthState.lastPollOK = time.Now()
	healthState.Unlock()
}

func notePollErr(err error) {
	healthState.Lock()
	healthState.lastPollErr = truncate(redactSecrets(err.Error()), 200)
	healthState.lastPollErrAt = time.Now()
	healthState.Unlock()
}

func noteTurnEnd() {
	healthState.Lock()
	healthState.lastTurnEnd = time.Now()
	healthState.Unlock()
}

type healthReport struct {
	OK        bool     `json:"ok"`
	Problems  []string `json:"problems,omitempty"`
	Version   string   `json:"version"`
	PID       int      `json:"pid"`
	StartedAt string   `json:"startedAt"`
	UptimeSec int64    `json:"uptimeSec"`
	Telegram  struct {
		Enabled        bool   `json:"enabled"`
		LastPollAgoSec int64  `json:"lastPollAgoSec,omitempty"`
		LastError      string `json:"lastError,omitempty"`
		LastErrorAgo   int64  `json:"lastErrorAgoSec,omitempty"`
	} `json:"telegram"`
	ActiveTurns    int   `json:"activeTurns"`
	QueuedTurns    int   `json:"queuedTurns"`
	LastTurnAgoSec int64 `json:"lastTurnAgoSec,omitempty"`
}

// buildHealth judges the host's state at now.
func buildHealth(now time.Time, active, queued int) healthReport {
	healthState.Lock()
	defer healthState.Unlock()
	r := healthReport{
		OK:          true,
		Version:     runningVersion(),
		PID:         os.Getpid(),
		StartedAt:   healthState.startedAt.Format(time.RFC3339),
		UptimeSec:   int64(now.Sub(healthState.startedAt).Seconds()),
		ActiveTurns: active,
		QueuedTurns: queued,
	}
	r.Telegram.Enabled = healthState.telegram
	if !healthState.lastTurnEnd.IsZero() {
		r.LastTurnAgoSec = int64(now.Sub(healthState.lastTurnEnd).Seconds())
	}
	if healthState.lastPollErr != "" {
		r.Telegram.LastError = healthState.lastPollErr
		r.Telegram.LastErrorAgo = int64(now.Sub(healthState.lastPollErrAt).Seconds())
	}
	if healthState.telegram {
		// Measured from the last success, or from start-up before the first.
		since := healthState.lastPollOK
		if since.IsZero() {
			since = healthState.startedAt
		} else {
			r.Telegram.LastPollAgoSec = int64(now.Sub(since).Seconds())
		}
		if now.Sub(since) > pollStaleAfter {
			r.OK = false
			msg := "telegram polling has not succeeded for " + now.Sub(since).Round(time.Second).String()
			if r.Telegram.LastError != "" {
				msg += " (last error: " + r.Telegram.LastError + ")"
			}
			r.Problems = append(r.Problems, msg)
		}
	}
	return r
}

// startHealthServer serves /health on addr until the process exits. A port
// already in use is logged, not fatal: health is for watchdogs, not for the
// bot's own work.
// healthAddr is where /health (and /approve) listen, or "" when disabled.
func healthAddr() string {
	addr := strings.TrimSpace(os.Getenv("AGLINK_HEALTH_ADDR"))
	if addr == "" {
		addr = defaultHealthAddr
	}
	if strings.EqualFold(addr, "off") {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err != nil || !isLoopbackHost(host) {
		return ""
	}
	return addr
}

func startHealthServer(b *Bot) {
	addr := healthAddr()
	if addr == "" {
		log.Printf("[health] endpoint disabled (AGLINK_HEALTH_ADDR off or not loopback)")
		return
	}
	mux := http.NewServeMux()
	// Tools marked for approval ask here before they run (approval.go).
	mux.HandleFunc("/approve", b.handleApproveHTTP)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		active, queued := b.dispatchLoad()
		rep := buildHealth(time.Now(), active, queued)
		w.Header().Set("Content-Type", "application/json")
		if !rep.OK {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(rep)
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("[health] listen %s: %v — health endpoint disabled", addr, err)
		return
	}
	log.Printf("[health] http://%s/health", addr)
	go func() {
		if err := http.Serve(ln, mux); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Printf("[health] serve: %v", err)
		}
	}()
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
