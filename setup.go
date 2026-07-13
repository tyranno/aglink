package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Interactive first-run setup wizard. Goal: install → run → guided config,
// so no manual config-file editing is ever required.

// webOnlyPlaceholderUserID is AllowedUserIDs[0] when setup skips Telegram
// entirely (web-chat-only). It is never checked against a real Telegram user
// — the polling loop that calls IsAllowed only runs once a bot token exists
// — it only serves as the internal "owner" identity WebChatOwnerChatID/
// ChatControlOwnerChatID fall back to for scheduling and web-originated
// actions. RunTelegramSetup replaces it once a real Telegram ID is linked.
const webOnlyPlaceholderUserID int64 = 1

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

	// [1/5] Channel: Telegram requires a BotFather-created bot + linking your
	// account; web chat needs neither (aglink-chat just serves a browser UI on
	// localhost). Defaulting to "yes" here would force every web-only user
	// through bot creation they don't want — the exact gap a user hit in
	// practice (main.go's boot already tolerates no bot token; the wizard
	// didn't offer that path at all).
	fmt.Println("\n[1/5] 사용 방식")
	useTelegram, err := confirm(in, "   텔레그램 봇을 연결할까요? 아니요를 고르면 웹 채팅만으로 시작합니다 (나중에 `teleclaude setup telegram`으로 언제든 추가 가능) [Y/n]: ")
	if err != nil {
		return err
	}

	var api *tgbotapi.BotAPI
	userID := webOnlyPlaceholderUserID
	if useTelegram {
		fmt.Println("\n[2/5] Telegram 봇 만들기 + 토큰")
		printBotFatherGuide()
		api, err = promptToken(in)
		if err != nil {
			return err
		}
		fmt.Println("\n   내 Telegram 계정 연결")
		userID, err = promptUserID(in, api)
		if err != nil {
			return err
		}
	} else {
		fmt.Println("   ℹ️ 텔레그램 없이 진행합니다. 실행 후 콘솔 로그에 뜨는 http://127.0.0.1:<포트>/?token=... 주소로 웹 채팅에 접속하세요.")
	}

	// [3/5] First project (optional).
	fmt.Println("\n[3/5] 첫 프로젝트 등록 (선택, 나중에 /project add 가능)")
	if err := promptFirstProject(in); err != nil {
		return err
	}

	// [4/5] claude OAuth token (so headless services authenticate via config — no
	// systemd env setup). Only applicable when claude is installed; codex authenticates
	// via its own `codex login` and has no equivalent config-file token.
	var claudeToken string
	if claudeErr == nil {
		fmt.Println("\n[4/5] claude 인증 토큰 (headless 서버용, 선택)")
		claudeToken, err = promptClaudeToken(in, claudePath)
		if err != nil {
			return err
		}
	}

	// [5/5] aglink-* plugins: offer to fetch+build any missing sibling
	// (screen/browser/web-chat control) so a from-source setup doesn't require
	// manually cloning 4 repos, then default-enable whichever ones are actually
	// present — they were deployed alongside teleclaude for a reason, so a
	// present-but-disabled binary would just be a silent dead weight the user
	// has to discover and turn on by hand. Screen control is the one exception:
	// it hands the AI direct mouse/keyboard control, so it stays opt-in even
	// when the binary is right there.
	fmt.Println("\n[5/5] aglink 보조 기능 (화면 제어 / 브라우저 제어 / 웹 채팅)")
	exe, eerr := os.Executable()
	if eerr == nil {
		ensureAglinkPlugins(in, filepath.Dir(exe))
	}
	enableAglinkChat := resolveAglinkBinary("aglink-chat", "", exe) != ""
	enableWebControl := resolveAglinkBinary("aglink-web", "", exe) != ""
	enableScreenControl := false
	if resolveAglinkBinary("aglink-screen", "", exe) != "" {
		ans, _ := prompt(in, "   화면 제어(마우스/키보드를 직접 조작)도 켤까요? 민감한 권한이라 기본은 끔입니다 [y/N]: ")
		enableScreenControl = strings.EqualFold(ans, "y") || strings.EqualFold(ans, "yes")
	}
	if !useTelegram && !enableAglinkChat {
		fmt.Println("   ⚠️ 텔레그램도 없고 aglink-chat도 못 찾았습니다 — 이대로면 봇과 대화할 방법이 없습니다.")
		fmt.Println("      나중에 aglink-chat을 설치하거나 `teleclaude setup telegram`으로 텔레그램을 연결하세요.")
	}

	// Save config.
	if err := writeConfigFile(cfgPath, writeConfigOpts{
		token:         tokenOf(api),
		userID:        userID,
		claudeToken:   claudeToken,
		backend:       defaultBackend,
		aglinkChat:    enableAglinkChat,
		webControl:    enableWebControl,
		screenControl: enableScreenControl,
	}); err != nil {
		return fmt.Errorf("설정 저장 실패: %w", err)
	}
	fmt.Printf("\n✅ 설정 저장됨: %s\n", cfgPath)
	if useTelegram {
		fmt.Printf("   봇 @%s, 허용 ID %d. 이제 바로 사용할 수 있어요!\n", api.Self.UserName, userID)
	} else {
		fmt.Println("   이제 바로 사용할 수 있어요! (웹 채팅 전용)")
	}
	fmt.Println("=======================================================")
	return nil
}

// tokenOf returns api.Token, or "" if api is nil (web-only setup skipped
// Telegram entirely, so there is no *tgbotapi.BotAPI to read from).
func tokenOf(api *tgbotapi.BotAPI) string {
	if api == nil {
		return ""
	}
	return api.Token
}

// RunTelegramSetup connects Telegram to an EXISTING config — the path back in
// for someone who started web-chat-only via RunSetup and later wants
// Telegram too. It merges just the bot token + allowed user id into the
// config already on disk, instead of RunSetup's full rewrite, which would
// silently wipe any aglink-*/screen-control settings configured since
// (writeConfigFile always constructs a fresh Config from scratch).
func RunTelegramSetup(cfgPath string) error {
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("기존 설정을 불러오지 못했습니다 (%s): %w — 설정이 아예 없다면 `teleclaude setup`을 먼저 실행하세요", cfgPath, err)
	}
	in := bufio.NewReader(os.Stdin)
	fmt.Println("================ Telegram 연결 ================")
	if cfg.TelegramBotToken != "" {
		ok, cerr := confirm(in, "   이미 텔레그램 봇이 연결되어 있습니다. 새 봇으로 바꿀까요? [Y/n]: ")
		if cerr != nil {
			return cerr
		}
		if !ok {
			fmt.Println("   건너뜀. 기존 연결을 그대로 둡니다.")
			return nil
		}
	}

	printBotFatherGuide()
	api, err := promptToken(in)
	if err != nil {
		return err
	}
	fmt.Println("\n   내 Telegram 계정 연결")
	userID, err := promptUserID(in, api)
	if err != nil {
		return err
	}

	cfg.TelegramBotToken = api.Token
	cfg.AllowedUserIDs = mergeTelegramUserID(cfg.AllowedUserIDs, userID)

	out, merr := marshalConfigYAML(cfg)
	if merr != nil {
		return merr
	}
	if werr := os.WriteFile(cfgPath, out, 0o600); werr != nil {
		return werr
	}
	fmt.Printf("\n✅ 텔레그램 연결 완료: 봇 @%s, 허용 ID %d\n", api.Self.UserName, userID)
	fmt.Println("   teleclaude를 재시작하면(또는 `!update`) 텔레그램에서도 대화할 수 있습니다.")
	fmt.Println("===================================================")
	return nil
}

// mergeTelegramUserID folds a newly-linked Telegram id into an existing
// AllowedUserIDs list. A web-only setup's placeholder id is meaningless once
// a real Telegram user is linked, so it is replaced outright rather than
// left sitting alongside a real id; otherwise the new id is appended (unless
// already present).
func mergeTelegramUserID(existing []int64, userID int64) []int64 {
	if len(existing) == 1 && existing[0] == webOnlyPlaceholderUserID {
		return []int64{userID}
	}
	if slices.Contains(existing, userID) {
		return existing
	}
	return append(existing, userID)
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

// writeConfigOpts bundles writeConfigFile's inputs — grouped into a struct
// once a plain positional call (token, userID, claudeToken, backend) grew a
// second, unrelated concern (which aglink-* features to default on).
type writeConfigOpts struct {
	token         string // Telegram bot token; "" for a web-chat-only setup
	userID        int64
	claudeToken   string // optional, persisted as claude.oauth_token for headless auth
	backend       string // "claude" or "codex", picked by which CLI setup found installed
	aglinkChat    bool   // web chat frontend (implies chat_control)
	webControl    bool   // aglink-web browser control
	screenControl bool   // aglink-screen desktop control (opt-in even when installed)
}

// writeConfigFile writes a complete config.yaml with sensible defaults.
func writeConfigFile(path string, o writeConfigOpts) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	cfg := &Config{
		TelegramBotToken: o.token,
		AllowedUserIDs:   []int64{o.userID},
		ManagerModel:     "haiku",
		TimeoutMinutes:   10,
		ManagerAlways:    true,
		MaxWorkers:       3,
		RateLimitPerMin:  20,
		ClaudeOauthToken: o.claudeToken,
		DefaultBackend:   o.backend,
		AglinkChat:       o.aglinkChat,
		ChatControl:      o.aglinkChat, // AglinkChat implies ChatControl on load anyway; set both so the on-disk file isn't misleading
		WebControl:       o.webControl,
		ScreenControl:    o.screenControl,
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
