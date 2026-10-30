//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/UserExistsError/conpty"
)

// claudeSetupTokenSupported: setup-token draws an interactive terminal UI, so
// it runs under a pseudo console here, the same way the interactive worker
// does (runner_conpty_windows.go).
const claudeSetupTokenSupported = true

// runSetupToken runs `claude setup-token`, follows its output into job, and
// returns the token it prints once the user has approved in the browser.
func runSetupToken(ctx context.Context, claudePath string, job *authJob) (string, error) {
	// Wide, so the token is not wrapped mid-way (scanSetupTokenOutput joins
	// lines anyway, but a box-drawing border inside a wrap would break it).
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		// An old token in the environment must not stand in for the login.
		if strings.HasPrefix(e, "CLAUDE_CODE_OAUTH_TOKEN=") {
			continue
		}
		env = append(env, e)
	}
	cpty, err := conpty.Start(
		buildWindowsCommandLine(claudePath, []string{"setup-token"}),
		conpty.ConPtyDimensions(400, 50),
		conpty.ConPtyEnv(env),
	)
	if err != nil {
		return "", err
	}
	defer cpty.Close()

	job.mu.Lock()
	job.input = func(s string) error { _, err := cpty.Write([]byte(s)); return err }
	job.mu.Unlock()

	out := &safeBuffer{}
	exited := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := cpty.Read(buf)
			if n > 0 {
				out.Write(stripANSI(buf[:n]))
			}
			if rerr != nil {
				close(exited)
				return
			}
		}
	}()

	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-exited:
			if token, _, _ := scanSetupTokenOutput(out.Since(0)); token != "" {
				return token, nil
			}
			return "", errors.New(lastLines(out.Since(0), 4))
		case <-tick.C:
			token, url, wantsCode := scanSetupTokenOutput(out.Since(0))
			if token != "" {
				return token, nil
			}
			job.set(func(s *authJobState) {
				if url != "" {
					s.URL = url
				}
				if wantsCode && s.Phase == "running" && !strings.HasPrefix(s.Message, "코드를 확인") {
					s.Phase = "need_code"
					s.Message = "브라우저에 표시된 코드를 붙여넣어 주세요."
				}
			})
		}
	}
}

// lastLines is the tail of the CLI's output, for an error message.
func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			lines = append(lines, t)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if len(lines) == 0 {
		return "claude setup-token 이 아무것도 출력하지 않고 끝났습니다"
	}
	return strings.Join(lines, " / ")
}
