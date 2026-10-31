package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// !flow — run a saved aglink-web flow straight from chat, with no model.
//
//	!flow                      list the saved flows
//	!flow <name> [text|elements]  run one, optionally with a page snapshot
//
// A flow is a run_steps batch saved with save_as (web/flows.go). Asking the AI
// to "open the settings screen again" spends a whole turn of tokens reasoning
// through steps it already knows; this calls `aglink-web cmd run_flow` directly
// and relays the step report, so repeating a known UI sequence costs nothing.

const flowRunTimeout = 3 * time.Minute

// flowReplyMax keeps the reply inside one Telegram message.
const flowReplyMax = 3500

func (b *Bot) handleFlow(reply replySender, chatID int64, fields []string) {
	self, _ := os.Executable()
	bin := resolveAglinkBinary("aglink-web", b.cfg().WebBinaryPath, self)
	if bin == "" {
		_ = reply.Send(chatID, "⚠️ aglink-web 을 찾지 못했습니다 — 이 기기에는 웹 제어가 설치돼 있지 않습니다.")
		return
	}
	args := []string{"cmd", "list_flows"}
	if len(fields) > 1 {
		args = []string{"cmd", "run_flow", fields[1]}
		if len(fields) > 2 {
			args = append(args, fields[2]) // snapshot: text | elements
		}
		_ = reply.Send(chatID, "▶ flow "+fields[1]+" 실행 중… (모델 사용 안 함)")
	}
	// A flow can take a while (waits, page loads); run it off the command lock
	// so !cancel and the rest stay responsive.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), flowRunTimeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, bin, args...).Output()
		_ = reply.Send(chatID, formatFlowReply(out, err, ctx.Err()))
	}()
}

// formatFlowReply turns `aglink-web cmd` output ({"text"} or {"error"}) into a
// chat message.
func formatFlowReply(out []byte, runErr, ctxErr error) string {
	if ctxErr == context.DeadlineExceeded {
		return fmt.Sprintf("⚠️ flow 가 %s 안에 끝나지 않아 중단했습니다.", flowRunTimeout)
	}
	var r struct {
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	if json.Unmarshal(out, &r) != nil {
		if runErr != nil {
			return "⚠️ flow 실행 실패: " + runErr.Error()
		}
		return "⚠️ aglink-web 응답을 읽지 못했습니다: " + truncate(strings.TrimSpace(string(out)), 300)
	}
	if r.Error != "" {
		return "⚠️ " + truncate(r.Error, flowReplyMax)
	}
	return "✅ " + truncate(r.Text, flowReplyMax)
}
