package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 작업 기록: one JSON line per gated request in ~/.aglink/approvals.log — what
// the agent wanted to run, and who let it (or did not). The file is cut over
// to approvals.log.1 at approvalLogMax, so the two hold the last ~2MB.

const approvalLogMax = 1 << 20

// approvalLogFile overrides the log path (tests); "" = <dataDir>/approvals.log.
var approvalLogFile string

var approvalLogMu sync.Mutex

type approvalLogEntry struct {
	Time     string `json:"time"`
	ID       string `json:"id,omitempty"`
	Source   string `json:"source,omitempty"`
	Tool     string `json:"tool"`
	Target   string `json:"target,omitempty"`
	Session  string `json:"session,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Decision string `json:"decision"`        // approved | denied | timeout | auto-session | auto-target | auto-always
	Scope    string `json:"scope,omitempty"` // approved: once | session | target | always
	By       string `json:"by,omitempty"`    // desktop | telegram | command | host | none
}

func approvalLogPath() string {
	if approvalLogFile != "" {
		return approvalLogFile
	}
	dir, err := dataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "approvals.log")
}

// approvalLogLine renders e as the JSON line written to the log: time
// stamped, summary on one line, cut short and scrubbed of secrets.
func approvalLogLine(e approvalLogEntry) []byte {
	if e.Time == "" {
		e.Time = time.Now().Format(time.RFC3339)
	}
	e.Summary = truncate(strings.Join(strings.Fields(redactSecrets(e.Summary)), " "), 300)
	d, _ := json.Marshal(e)
	return append(d, '\n')
}

// logApproval appends e to the log. Failures are logged, never fatal.
func logApproval(e approvalLogEntry) {
	path := approvalLogPath()
	if path == "" {
		return
	}
	line := approvalLogLine(e)
	approvalLogMu.Lock()
	defer approvalLogMu.Unlock()
	if st, err := os.Stat(path); err == nil && st.Size()+int64(len(line)) > approvalLogMax {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("[approval] log: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		log.Printf("[approval] log: %v", err)
	}
}

// readApprovalLog returns the last n entries, oldest first.
func readApprovalLog(n int) []approvalLogEntry {
	path := approvalLogPath()
	if path == "" || n <= 0 {
		return nil
	}
	approvalLogMu.Lock()
	defer approvalLogMu.Unlock()
	var all []approvalLogEntry
	for _, p := range []string{path + ".1", path} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var e approvalLogEntry
			if json.Unmarshal(sc.Bytes(), &e) == nil && e.Tool != "" {
				all = append(all, e)
			}
		}
		f.Close()
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

var decisionLabel = map[string]string{
	"approved":     "✅ 승인",
	"denied":       "🚫 거부",
	"timeout":      "⌛ 시간초과",
	"auto-session": "💬 자동(이 대화)",
	"auto-target":  "🎯 자동(대상)",
	"auto-always":  "♾️ 자동(도구)",
}

var scopeLabel = map[string]string{scopeSession: "이 대화", scopeTarget: "이 대상", scopeAlways: "항상"}

var byLabel = map[string]string{"desktop": "앱", "telegram": "텔레그램", "command": "명령", "host": "", "none": "채널 없음"}

// formatApprovalLogEntry is one short Korean line for !approvals.
func formatApprovalLogEntry(e approvalLogEntry) string {
	when := e.Time
	if t, err := time.Parse(time.RFC3339, e.Time); err == nil {
		when = t.Local().Format("01-02 15:04")
	}
	what := e.Tool
	if e.Target != "" {
		what += " → " + e.Target
	}
	dec := decisionLabel[e.Decision]
	if dec == "" {
		dec = e.Decision
	}
	if s := scopeLabel[e.Scope]; s != "" {
		dec += "(" + s + ")"
	}
	line := when + " " + dec + " " + what
	if by := byLabel[e.By]; by != "" {
		line += " · " + by
	} else if _, known := byLabel[e.By]; !known && e.By != "" {
		line += " · " + e.By
	}
	if e.Target == "" && e.Summary != "" {
		// The tool alone says little; the first bit of the call does.
		s := e.Summary
		if i := strings.Index(s, " "); i > 0 && strings.HasPrefix(s, e.Tool) {
			s = s[i+1:]
		}
		line += "\n   " + truncate(s, 60)
	}
	return line
}

// handleApprovalsCommand is
//
//	!approvals [n]               last n approval log entries (default 10)
//	!approvals allow             what runs without asking: target allows, this-conversation grants, gated tools
//	!approvals revoke <항목|번호>  drop a target allow ("launch_app:notepad", or its number in the list)
//	!approvals revoke session    drop every "이 대화에서 허용" grant
func (b *Bot) handleApprovalsCommand(reply replySender, chatID int64, fields []string) {
	cfg := b.cfg()
	sub := ""
	if len(fields) > 1 {
		sub = strings.ToLower(fields[1])
	}
	switch sub {
	case "allow", "list", "허용":
		var sb strings.Builder
		sb.WriteString("🎯 대상별 항상 허용 (approvals.allow)\n")
		if len(cfg.ApprovalAllow) == 0 {
			sb.WriteString("  (없음)\n")
		}
		for i, e := range cfg.ApprovalAllow {
			fmt.Fprintf(&sb, "  %d. %s\n", i+1, e)
		}
		if s := listApprovalSessions(); len(s) > 0 {
			sb.WriteString("💬 이 대화에서 허용 (재시작하면 사라짐)\n")
			for _, l := range s {
				sb.WriteString("  " + l + "\n")
			}
		}
		sb.WriteString("🔐 승인받을 도구 (approvals.tools): ")
		if len(cfg.ApprovalTools) == 0 {
			sb.WriteString("(없음 — 승인 꺼짐)")
		} else {
			sb.WriteString(strings.Join(cfg.ApprovalTools, ", "))
		}
		sb.WriteString("\n\n취소: !approvals revoke <번호|항목> · !approvals revoke session")
		_ = reply.Send(chatID, sb.String())
	case "revoke", "remove", "취소":
		arg := strings.TrimSpace(strings.Join(fields[min(2, len(fields)):], " "))
		if arg == "" {
			_ = reply.Send(chatID, "사용법: !approvals revoke <번호|도구:대상> 또는 !approvals revoke session")
			return
		}
		if strings.EqualFold(arg, "session") || arg == "대화" {
			_ = reply.Send(chatID, fmt.Sprintf("💬 이 대화 허용 %d건을 지웠습니다.", clearApprovalSessions("")))
			return
		}
		entry := arg
		if n, err := strconv.Atoi(arg); err == nil {
			if n < 1 || n > len(cfg.ApprovalAllow) {
				_ = reply.Send(chatID, fmt.Sprintf("%d번 항목이 없습니다. !approvals allow 로 목록을 보세요.", n))
				return
			}
			entry = cfg.ApprovalAllow[n-1]
		}
		found, err := revokeApprovalAllow(cfg, entry)
		switch {
		case err != nil:
			_ = reply.Send(chatID, "⚠️ 저장 실패: "+err.Error())
		case !found:
			_ = reply.Send(chatID, "그런 항목이 없습니다: "+entry)
		default:
			_ = reply.Send(chatID, "🗑️ 대상 허용을 취소했습니다: "+normalizeAllowEntry(entry))
		}
	default:
		n := 10
		if sub != "" {
			v, err := strconv.Atoi(sub)
			if err != nil || v < 1 {
				_ = reply.Send(chatID, "사용법: !approvals [개수] · !approvals allow · !approvals revoke <항목>")
				return
			}
			n = min(v, 50)
		}
		entries := readApprovalLog(n)
		if len(entries) == 0 {
			_ = reply.Send(chatID, "승인 기록이 없습니다.")
			return
		}
		lines := make([]string, 0, len(entries)+1)
		lines = append(lines, fmt.Sprintf("📜 최근 승인 기록 %d건", len(entries)))
		for _, e := range entries {
			lines = append(lines, formatApprovalLogEntry(e))
		}
		_ = reply.Send(chatID, strings.Join(lines, "\n"))
	}
}
