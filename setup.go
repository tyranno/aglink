package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

	// Prerequisite (hard constraint): claude CLI must be present + logged in.
	claudePath, err := findClaude("")
	if err != nil {
		fmt.Println("❌ claude CLI를 찾을 수 없습니다.")
		fmt.Println("   먼저 claude를 설치하고 로그인하세요:")
		fmt.Println("     1) claude Code 설치  2) `claude` 실행 후 로그인  3) `claude --version` 확인")
		fmt.Println("   그 다음 다시 실행해 주세요.")
		return err
	}
	fmt.Printf("✅ claude 발견: %s\n", claudePath)
	fmt.Println("   ⚠️ claude가 로그인되어 있어야 합니다. (안 되어 있으면 먼저 `claude` 실행해 로그인)")

	// [1/3] Create bot (guided) + token → validate via getMe.
	fmt.Println("\n[1/3] Telegram 봇 만들기 + 토큰")
	printBotFatherGuide()
	api, err := promptToken(in)
	if err != nil {
		return err
	}

	// [2/3] My Telegram user ID → auto-detect, manual fallback.
	fmt.Println("\n[2/3] 내 Telegram 계정 연결")
	userID, err := promptUserID(in, api)
	if err != nil {
		return err
	}

	// [3/3] First project (optional).
	fmt.Println("\n[3/3] 첫 프로젝트 등록 (선택, 나중에 /project add 가능)")
	if err := promptFirstProject(in); err != nil {
		return err
	}

	// Save config.
	if err := writeConfigFile(cfgPath, api.Token, userID); err != nil {
		return fmt.Errorf("설정 저장 실패: %w", err)
	}
	fmt.Printf("\n✅ 설정 저장됨: %s\n", cfgPath)
	fmt.Printf("   봇 @%s, 허용 ID %d. 이제 바로 사용할 수 있어요!\n", api.Self.UserName, userID)
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

// writeConfigFile writes a complete config.txt with sensible defaults.
func writeConfigFile(path, token string, userID int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	content := fmt.Sprintf(
		"TELEGRAM_BOT_TOKEN=%s\nALLOWED_USER_IDS=%d\nMANAGER_MODEL=haiku\nWORKER_MODEL=\nTIMEOUT_MINUTES=10\nMANAGER_ALWAYS=true\n",
		token, userID)
	return os.WriteFile(path, []byte(content), 0o600)
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
