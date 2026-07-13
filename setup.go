package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Interactive first-run setup wizard. Goal: install → run → guided config,
// so no manual config-file editing is ever required.

// isInteractive reports whether stdin is a terminal (wizard is usable).
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
}

// RunSetup walks the user through prerequisites + config and writes config.txt.
func RunSetup(cfgPath string) error {
	in := bufio.NewReader(os.Stdin)
	fmt.Println("================ teleclaude 설정 마법사 ================")

	// Prerequisite: at least one of claude/codex CLI must be present. Neither is
	// individually required — a codex-only machine (no claude installed) must be
	// able to complete setup, same as main.go's boot-time backend fallback.
	claudePath, claudeErr := findClaude("")
	codexPath, _ := findCodex("")
	if claudeErr != nil && codexPath == "" {
		fmt.Println("❌ claude, codex CLI 둘 다 찾을 수 없습니다.")
		fmt.Println("   둘 중 하나를 설치하고 로그인하세요:")
		fmt.Println("     - claude: claude Code 설치 → `claude` 실행해 로그인 → `claude --version` 확인")
		fmt.Println("     - codex:  codex CLI 설치   → `codex login` 실행           → `codex --version` 확인")
		fmt.Println("   그 다음 다시 실행해 주세요.")
		return claudeErr
	}
	defaultBackend := "claude"
	if claudeErr != nil {
		fmt.Printf("ℹ️ claude CLI 없음 (%v) — codex 전용으로 진행합니다.\n", claudeErr)
		defaultBackend = "codex"
	} else {
		fmt.Printf("✅ claude 발견: %s\n", claudePath)
		fmt.Println("   ⚠️ claude가 로그인되어 있어야 합니다. (안 되어 있으면 먼저 `claude` 실행해 로그인)")
	}
	if codexPath != "" {
		fmt.Printf("✅ codex 발견: %s\n", codexPath)
		fmt.Println("   ⚠️ codex가 로그인되어 있어야 합니다. (안 되어 있으면 먼저 `codex login` 실행)")
	}

	// [1/4] Create bot (guided) + token → validate via getMe.
	fmt.Println("\n[1/4] Telegram 봇 만들기 + 토큰")
	printBotFatherGuide()
	api, err := promptToken(in)
	if err != nil {
		return err
	}

	// [2/4] My Telegram user ID → auto-detect, manual fallback.
	fmt.Println("\n[2/4] 내 Telegram 계정 연결")
	userID, err := promptUserID(in, api)
	if err != nil {
		return err
	}

	// [3/4] First project (optional).
	fmt.Println("\n[3/4] 첫 프로젝트 등록 (선택, 나중에 /project add 가능)")
	if err := promptFirstProject(in); err != nil {
		return err
	}

	// [4/4] claude OAuth token (so headless services authenticate via config — no
	// systemd env setup). Only applicable when claude is installed; codex authenticates
	// via its own `codex login` and has no equivalent config-file token.
	var claudeToken string
	if claudeErr == nil {
		fmt.Println("\n[4/4] claude 인증 토큰 (headless 서버용, 선택)")
		claudeToken, err = promptClaudeToken(in, claudePath)
		if err != nil {
			return err
		}
	}

	// Save config.
	if err := writeConfigFile(cfgPath, api.Token, userID, claudeToken, defaultBackend); err != nil {
		return fmt.Errorf("설정 저장 실패: %w", err)
	}
	fmt.Printf("\n✅ 설정 저장됨: %s\n", cfgPath)
	fmt.Printf("   봇 @%s, 허용 ID %d. 이제 바로 사용할 수 있어요!\n", api.Self.UserName, userID)

	// Offer to fetch+build any missing aglink-* sibling (screen/browser/web-chat
	// control) so a from-source setup doesn't require manually cloning 4 repos.
	// No-op on non-Windows and when teleclaude's own exe path can't be resolved.
	if exe, eerr := os.Executable(); eerr == nil {
		ensureAglinkPlugins(in, filepath.Dir(exe))
	}

	fmt.Println("=======================================================")
	return nil
}

// printBotFatherGuide walks the user through creating a Telegram bot.
// (Bot creation cannot be automated — it must be done with @BotFather by hand.)
func printBotFatherGuide() {
	fmt.Println("   아직 봇이 없다면 텔레그램에서 직접 만드세요 (약 1분):")
	fmt.Println("     1) 텔레그램에서 @BotFather 검색 → 대화 열기   (https://t.me/BotFather)")
	fmt.Println("     2) /newbot 전송")
	fmt.Println("     3) 봇 표시 이름 입력            (예: 내 코드봇)")
	fmt.Println("     4) 봇 username 입력             (반드시 'bot'으로 끝나야 함, 예: mycode_bot)")
	fmt.Println("     5) BotFather가 준 토큰 복사     (형식: 123456789:AAH...)")
	fmt.Println("   이미 봇이 있으면 그 토큰을 바로 붙여넣으세요.")
}

func promptToken(in *bufio.Reader) (*tgbotapi.BotAPI, error) {
	for {
		token, err := prompt(in, "   봇 토큰 입력: ")
		if err != nil {
			return nil, err
		}
		if token == "" {
			continue
		}
		api, err := tgbotapi.NewBotAPI(token)
		if err != nil {
			fmt.Printf("   ⚠️ 토큰이 유효하지 않습니다 (%v). 다시 입력하세요.\n", err)
			continue
		}
		fmt.Printf("   ✅ 봇 확인: @%s\n", api.Self.UserName)
		return api, nil
	}
}

func promptUserID(in *bufio.Reader, api *tgbotapi.BotAPI) (int64, error) {
	fmt.Printf("   지금 텔레그램에서 @%s 에게 아무 메시지나 보내세요. (예: 안녕)\n", api.Self.UserName)
	for {
		line, err := prompt(in, "   보냈으면 Enter (또는 user ID를 직접 입력): ")
		if err != nil {
			return 0, err
		}
		if line != "" { // manual entry
			id, perr := strconv.ParseInt(line, 10, 64)
			if perr != nil {
				fmt.Println("   숫자 ID가 아닙니다. 다시 입력하세요.")
				continue
			}
			return id, nil
		}
		// auto-detect
		id, name, derr := detectUserID(api)
		if derr != nil {
			fmt.Printf("   ⚠️ 감지 실패: %v\n   봇에게 메시지를 보냈는지 확인하고 다시 Enter (또는 ID 직접 입력)\n", derr)
			continue
		}
		ok, err := confirm(in, fmt.Sprintf("   감지됨: %d (%s). 맞나요? [Y/n]: ", id, name))
		if err != nil {
			return 0, err
		}
		if ok {
			return id, nil
		}
	}
}

// detectUserID polls getUpdates for the most recent sender, then clears pending updates.
func detectUserID(api *tgbotapi.BotAPI) (int64, string, error) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 3
	updates, err := api.GetUpdates(u)
	if err != nil {
		return 0, "", err
	}
	var lastID int
	var fromID int64
	var name string
	for _, up := range updates {
		if up.UpdateID > lastID {
			lastID = up.UpdateID
		}
		if up.Message != nil && up.Message.From != nil {
			fromID = up.Message.From.ID
			name = strings.TrimSpace(up.Message.From.FirstName + " " + up.Message.From.LastName)
		}
	}
	if fromID == 0 {
		return 0, "", fmt.Errorf("최근 메시지를 찾지 못했습니다")
	}
	// Confirm offset so the bot starts with a clean queue.
	clr := tgbotapi.NewUpdate(lastID + 1)
	clr.Timeout = 0
	_, _ = api.GetUpdates(clr)
	return fromID, name, nil
}

func promptFirstProject(in *bufio.Reader) error {
	path, err := prompt(in, "   관리할 폴더 경로 (없으면 Enter로 건너뛰기): ")
	if err != nil {
		return err
	}
	if path == "" {
		fmt.Println("   건너뜀. 나중에 봇에서 /project add <이름> <경로>")
		return nil
	}
	dir, derr := dataDir()
	if derr != nil {
		return derr
	}
	store := NewFileStore(filepath.Join(dir, "store.json"))
	if err := store.Load(); err != nil {
		return err
	}
	name, err := prompt(in, fmt.Sprintf("   프로젝트 이름 (기본: %s): ", filepath.Base(path)))
	if err != nil {
		return err
	}
	if name == "" {
		name = filepath.Base(filepath.Clean(path))
	}
	if err := store.AddProject(name, path); err != nil {
		fmt.Printf("   ⚠️ 등록 실패: %v (나중에 /project add 로 추가하세요)\n", err)
		return nil
	}
	fmt.Printf("   ✅ 프로젝트 등록: %s\n", name)
	return nil
}

// promptClaudeToken optionally captures a CLAUDE_CODE_OAUTH_TOKEN for headless use.
// Empty result means "use claude's own login" (desktop with interactive claude login).
func promptClaudeToken(in *bufio.Reader, claudePath string) (string, error) {
	fmt.Println("   서버(systemd 등 headless)에서 돌릴 거면 claude 인증 토큰을 넣어두면 편합니다.")
	fmt.Println("   발급: 터미널에서  claude setup-token  실행 → 브라우저 승인 → sk-ant-oat01-... 복사")
	fmt.Println("   (데스크톱에서 claude가 이미 로그인돼 있으면 Enter로 건너뛰어도 됩니다)")
	token, err := prompt(in, "   claude 토큰 붙여넣기 (없으면 Enter): ")
	if err != nil {
		return "", err
	}
	if token == "" {
		fmt.Println("   건너뜀 (claude 자체 로그인 사용).")
		return "", nil
	}
	fmt.Println("   토큰 검증 중... (수십 초 걸릴 수 있음)")
	if verr := verifyClaudeToken(claudePath, token); verr != nil {
		ans, _ := prompt(in, fmt.Sprintf("   ⚠️ 검증 실패(%v). 그래도 저장할까요? [y/N]: ", verr))
		if s := strings.ToLower(ans); s != "y" && s != "yes" {
			fmt.Println("   토큰 저장 안 함.")
			return "", nil
		}
	} else {
		fmt.Println("   ✅ claude 토큰 검증 성공")
	}
	return token, nil
}

// verifyClaudeToken runs a tiny claude call with the token to confirm it authenticates.
func verifyClaudeToken(claudePath, token string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, claudePath, "-p", "hi", "--output-format", "json")
	cmd.Env = append(os.Environ(), "CLAUDE_CODE_OAUTH_TOKEN="+token)
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	res, perr := parseRunResult(string(out))
	if perr != nil {
		return perr
	}
	if res.IsError {
		return fmt.Errorf("인증 거부(401 등)")
	}
	return nil
}

// writeConfigFile writes a complete config.yaml with sensible defaults.
// claudeToken (optional) is persisted as claude.oauth_token for headless auth.
// defaultBackend is "claude" or "codex", picked by which CLI setup found installed.
func writeConfigFile(path, token string, userID int64, claudeToken, defaultBackend string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	cfg := &Config{
		TelegramBotToken: token,
		AllowedUserIDs:   []int64{userID},
		ManagerModel:     "haiku",
		TimeoutMinutes:   10,
		ManagerAlways:    true,
		MaxWorkers:       3,
		RateLimitPerMin:  20,
		ClaudeOauthToken: claudeToken,
		DefaultBackend:   defaultBackend,
	}
	out, err := marshalConfigYAML(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

// prompt prints a label and reads a trimmed line. Returns the read error on EOF.
func prompt(r *bufio.Reader, label string) (string, error) {
	fmt.Print(label)
	line, err := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if err != nil && line == "" {
		if err == io.EOF {
			return "", fmt.Errorf("입력 스트림 종료(EOF) — 대화형 터미널에서 실행하세요")
		}
		return "", err
	}
	return line, nil
}

func confirm(r *bufio.Reader, label string) (bool, error) {
	line, err := prompt(r, label)
	if err != nil {
		return false, err
	}
	s := strings.ToLower(line)
	return s == "" || s == "y" || s == "yes", nil
}
