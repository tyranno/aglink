package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// aglink — Telegram ↔ Claude agent for Windows (MVP).
// Design Ref: §11 — wiring/assembly + claude health check.

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 {
		cmd = args[0]
	}

	switch cmd {
	case "run":
		var configPath, handoffFile, notifyChat string
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--handoff-ready":
				if i+1 < len(args) {
					handoffFile = args[i+1]
					i++
				}
			case "--notify-chat":
				if i+1 < len(args) {
					notifyChat = args[i+1]
					i++
				}
			default:
				configPath = args[i]
			}
		}
		if err := run(configPath, handoffFile, notifyChat); err != nil {
			log.Fatalf("fatal: %v", err)
		}
	case "setup":
		// `setup telegram [config-path]` connects Telegram to an existing
		// config without the full wizard rewrite — the "add it later" path
		// for someone who started web-chat-only. Any other second arg is the
		// config path override, same as before.
		if len(args) > 1 && args[1] == "telegram" {
			var override string
			if len(args) > 2 {
				override = args[2]
			}
			path := override
			if path == "" {
				p, e := defaultYAMLPath()
				if e != nil {
					log.Fatal(e)
				}
				path = p
			}
			if err := RunTelegramSetup(path); err != nil {
				log.Fatalf("텔레그램 연결 중단: %v", err)
			}
			return
		}
		var override string
		if len(args) > 1 {
			override = args[1]
		}
		path := override
		if path == "" {
			p, e := defaultYAMLPath()
			if e != nil {
				log.Fatal(e)
			}
			path = p
		}
		if err := RunSetup(path); err != nil {
			log.Fatalf("설정 마법사 중단: %v", err)
		}
	case "version", "--version", "-v":
		line := "aglink " + runningVersion()
		if buildCommit != "" {
			line += " (" + buildCommit + ")"
		}
		if buildTime != "" {
			line += " " + buildTime
		}
		fmt.Println(line)
	default:
		fmt.Println("usage: aglink [run [config-path]] | setup [config-path] | setup telegram [config-path] | version")
	}
}

// pidFilePath returns the path to the PID file (<data dir>/aglink.pid). It goes
// through dataDir so it lands beside the rest of the state even on a
// pre-rename install still rooted at ~/.teleclaude.
func pidFilePath() string {
	dir, err := dataDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".aglink")
	}
	return filepath.Join(dir, "aglink.pid")
}

// writePIDFile records the current process PID so the next instance can kill it cleanly.
func writePIDFile() {
	_ = os.WriteFile(pidFilePath(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
}

func run(configOverride, handoffReadyFile, notifyChat string) error {
	dir, err := dataDir()
	if err != nil {
		return err
	}

	// Tee logs to a file before anything else can fail: the elevated instance
	// runs with a hidden console, so this file is the only record of startup,
	// elevation and supervised-child failures.
	if closer, lerr := setupFileLogging(dir); lerr != nil {
		log.Printf("[log] file logging disabled: %v", lerr)
	} else {
		defer closer.Close()
	}

	// cfgPath is the YAML path the wizard writes to (and the path we reload from).
	cfgPath := configOverride
	if cfgPath == "" {
		p, perr := defaultYAMLPath()
		if perr != nil {
			return perr
		}
		cfgPath = p
	}

	// Normal startup: kill competing instances before we connect to Telegram.
	// Handoff mode handles session release below (explicit wait for old process).
	if handoffReadyFile == "" {
		killPreviousInstance()
	}

	// Load config. With no explicit override, prefer LoadOrMigrate so an existing
	// config.txt is auto-migrated to config.yaml; otherwise honor the override path.
	var cfg *Config
	if configOverride == "" {
		var used string
		cfg, used, err = LoadOrMigrate(dir)
		if err == nil {
			cfgPath = used
		}
	} else {
		cfg, err = LoadConfig(cfgPath)
	}
	if err != nil {
		// No (or incomplete) config → run the interactive wizard, then reload.
		if !isInteractive() {
			return fmt.Errorf("%w\n대화형 터미널에서 `aglink setup`을 먼저 실행하세요 (%s)", err, cfgPath)
		}
		fmt.Println("⚙️  설정이 없거나 불완전합니다. 설정 마법사를 시작합니다.")
		if serr := RunSetup(cfgPath); serr != nil {
			return fmt.Errorf("설정 마법사 중단: %w", serr)
		}
		cfg, err = LoadConfig(cfgPath)
		if err != nil {
			return err
		}
	}

	// Elevation: when screen_control.elevated is set but we are not already
	// elevated, relaunch ourselves as administrator so synthetic input can drive
	// elevated target apps (Windows UIPI drops input from lower-integrity procs).
	// No-op on non-Windows and when already elevated.
	if cfg.ScreenControl && cfg.ScreenElevated && !isElevated() {
		log.Printf("[main] screen_control.elevated=true and process not elevated → relaunching as administrator (UAC)…")
		if rerr := relaunchElevated(); rerr != nil {
			log.Printf("[main] elevation relaunch failed: %v (continuing un-elevated; elevated target apps may not respond to clicks)", rerr)
		} else {
			log.Printf("[main] elevated instance launched; exiting un-elevated instance.")
			return nil
		}
	}

	store := NewFileStore(filepath.Join(dir, "store.json"))
	if err := store.Load(); err != nil {
		return fmt.Errorf("대화 저장소 로드 실패: %w", err)
	}

	holder := NewConfigHolder(cfg)

	// Resolve both backends; neither is individually required. aglink boots as
	// long as at least one of claude/codex is installed, so a claude-only machine
	// and a codex-only machine both work. The active backend's binary must exist;
	// the other is optional and simply can't be switched to.
	var claudeRunner ClaudeClient
	if claudePath, err := findClaude(cfg.ClaudePath); err == nil {
		if herr := claudeHealthCheck(claudePath); herr == nil {
			claudeRunner = NewClaudeRunner(claudePath, holder)
			log.Printf("[main] claude: %s", claudePath)
		} else {
			log.Printf("[main] claude 헬스체크 실패 → claude 백엔드 비활성화: %v", herr)
		}
	} else {
		log.Printf("[main] claude: 미설치 (선택적) — %v", err)
	}

	var codexRunner ClaudeClient
	if codexPath, err := findCodex(cfg.CodexPath); err == nil && codexPath != "" {
		codexRunner = NewCodexRunner(codexPath, holder)
		log.Printf("[main] codex: %s", codexPath)
	} else if err != nil {
		log.Printf("[main] codex not available: %v", err)
	} else {
		log.Printf("[main] codex: 미설치 (선택적)")
	}

	// opencode is a third optional backend. Like codex it is inert unless the CLI
	// is installed: findOpencode returns "" when it isn't, the runner is never
	// constructed, and the backend cannot be selected — so adding it can't regress
	// a claude/codex-only machine.
	var opencodeRunner ClaudeClient
	if opencodePath, err := findOpencode(cfg.OpencodePath); err == nil && opencodePath != "" {
		opencodeRunner = NewOpencodeRunner(opencodePath, holder)
		log.Printf("[main] opencode: %s", opencodePath)
	} else if err != nil {
		log.Printf("[main] opencode not available: %v", err)
	} else {
		log.Printf("[main] opencode: 미설치 (선택적)")
	}

	if claudeRunner == nil && codexRunner == nil && opencodeRunner == nil {
		return fmt.Errorf("claude, codex, opencode 중 하나는 설치되어야 합니다 (모두 찾지 못함)")
	}

	manager := NewManager(claudeRunner, codexRunner, store, holder)
	if opencodeRunner != nil {
		manager.SetOpencodeClient(opencodeRunner)
	}

	// Background opencode update checker: refreshes the installed-vs-latest cache
	// the version UI reads to surface an "opencode 업데이트 있음" notice. Inert when
	// opencode isn't installed (records an empty snapshot).
	startOpencodeUpdateChecker(holder)

	// Interactive (B안): a persistent ConPTY-backed claude session, opted into
	// per web conversation via "!interactive on" (see bot.go handleInteractive).
	// Gated behind its own config flag — off by default — since it is
	// experimental and Windows-only (see runner_conpty_stub.go for other OSes).
	if cfg.InteractiveClaude {
		if claudePath, err := findClaude(cfg.ClaudePath); err == nil {
			if ir := NewInteractiveClaudeRunner(claudePath, holder); ir != nil {
				manager.SetInteractiveClient(ir)
				log.Printf("[main] interactive claude session: enabled")
			} else {
				log.Printf("[main] interactive_claude.enabled=true but not supported on this OS — ignoring")
			}
		} else {
			log.Printf("[main] interactive_claude.enabled=true but claude not installed — ignoring: %v", err)
		}
	}

	// Choose the active backend: persisted choice first, then DEFAULT_BACKEND, then
	// fall back to whichever backend is actually installed. Startup selection does
	// not persist, so a temporary fallback never clobbers the saved preference.
	preferred := store.GetStoredBackend()
	if preferred == "" {
		preferred = cfg.DefaultBackend
	}
	backend, ok := chooseBackend(preferred, claudeRunner != nil, codexRunner != nil, opencodeRunner != nil)
	if !ok {
		return fmt.Errorf("사용 가능한 백엔드가 없습니다")
	}
	if err := manager.setBackend(backend, false); err != nil {
		return fmt.Errorf("백엔드 설정 실패: %w", err)
	}
	if preferred != "" && backend != preferred {
		log.Printf("[main] 선호 백엔드 %q 사용 불가 → %s로 대체", preferred, backend)
	} else {
		log.Printf("[main] backend: %s", backend)
	}

	// Web-chat-only mode: with no bot token, skip NewBotAPI entirely (it does a
	// network getMe that would fail) and boot with a nil api. Telegram polling is
	// disabled below; the web frontend (chat_control/aglink_chat) is the only surface.
	var api *tgbotapi.BotAPI
	if cfg.TelegramBotToken != "" {
		api, err = tgbotapi.NewBotAPI(cfg.TelegramBotToken)
		if err != nil {
			return fmt.Errorf("텔레그램 봇 초기화 실패: %w", err)
		}
	} else {
		log.Printf("[main] 텔레그램 봇 토큰 없음 → 웹채팅 전용 모드 (텔레그램 폴링 비활성)")
	}
	activeBackend := manager.Backend()
	var activeManagerModel, activeWorkerModel string
	if activeBackend == "codex" {
		activeWorkerModel = cfg.CodexModel
		activeManagerModel = cfg.CodexManagerModel
		if activeManagerModel == "" {
			activeManagerModel = cfg.CodexModel
		}
	} else {
		activeManagerModel = cfg.ManagerModel
		activeWorkerModel = cfg.WorkerModel
	}
	log.Printf("[main] allowlist: %v, backend=%s manager=%s worker=%q",
		cfg.AllowedUserIDs, activeBackend, activeManagerModel, activeWorkerModel)

	// Scheduler: reminders + cron jobs
	sched := NewScheduler(filepath.Join(dir, "tasks.json"))
	if err := sched.Load(); err != nil {
		log.Printf("[main] scheduler load warning: %v", err)
	}

	// UserStore: runtime-managed allowed user IDs (persist across restarts)
	userStore := NewUserStore(filepath.Join(dir, "extra_users.json"))
	if err := userStore.Load(); err != nil {
		log.Printf("[main] userstore load warning: %v", err)
	}

	bot := NewBot(api, holder, store, manager, sched, userStore)

	// Wire scheduler send/dispatch after bot is created
	sched.SetSend(func(chatID int64, text string) { _ = bot.Send(chatID, text) })
	sched.SetDispatch(func(chatID int64, text, project string) { bot.dispatchScheduledTask(chatID, text, project) })
	manager.SetScheduler(sched)
	go sched.Run()

	// Maintenance: prune conversations/history inactive for longer than
	// ConversationTTLDays (default 30d, 0 disables). Runs once at startup then daily.
	go func() {
		runPrune := func() {
			// A prune panic must not kill the daily ticker; contain it per run.
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[maintenance] prune panic recovered: %v", r)
				}
			}()
			ttl := holder.Get().ConversationTTLDays
			if n, perr := store.PruneOldConversations(ttl); perr != nil {
				log.Printf("[maintenance] conversation prune failed: %v", perr)
			} else if n > 0 {
				log.Printf("[maintenance] pruned %d old conversation(s) (ttl=%dd)", n, ttl)
			}
			if n, perr := PruneHistory(ttl); perr != nil {
				log.Printf("[maintenance] history prune failed: %v", perr)
			} else if n > 0 {
				log.Printf("[maintenance] pruned %d old history file(s) (ttl=%dd)", n, ttl)
			}
		}
		runPrune()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			runPrune()
		}
	}()

	// Keep-awake: while screen_control + screen_control.keep_awake are both on,
	// periodically assert ES_DISPLAY_REQUIRED so Windows' idle timer never fires
	// the screensaver/lock — a locked workstation blocks focus_window/click and
	// makes capture_window return a black image (see screen_keepawake_windows.go).
	var stopKeepAwake func()
	syncKeepAwake := func() {
		want := holder.Get().ScreenControl && holder.Get().ScreenKeepAwake
		if want == (stopKeepAwake != nil) {
			return
		}
		if want {
			stopKeepAwake = startKeepAwake()
			log.Printf("[keepawake] ON — 유휴 화면보호기/잠금 방지 중")
		} else {
			stopKeepAwake()
			stopKeepAwake = nil
			log.Printf("[keepawake] OFF")
		}
	}
	syncKeepAwake()
	defer func() {
		if stopKeepAwake != nil {
			stopKeepAwake()
		}
	}()

	// Config hot-reload: watch the YAML file and apply changes without restart.
	hooks := ReloadHooks{
		OnRateLimit:    func(n int) { bot.rateLimiter.SetLimit(n) },
		OnTokenChanged: func() { log.Printf("[config] 봇 토큰 변경 감지 — 적용하려면 재시작 필요") },
		OnScreenControl: func(on bool) {
			state := "OFF"
			if on {
				state = "ON"
			}
			log.Printf("[screen] screen_control %s", state)
			for _, id := range holder.Get().AllowedUserIDs {
				_ = bot.Send(id, "🖥 화면제어 "+state)
			}
			syncKeepAwake()
		},
		OnKeepAwake: func(bool) { syncKeepAwake() },
		OnDefaultBackend: func(name string) {
			if err := manager.SetBackend(name); err != nil {
				log.Printf("[config] backend.default → %q 적용 실패: %v", name, err)
				for _, id := range holder.Get().AllowedUserIDs {
					_ = bot.Send(id, "⚠️ 기본 백엔드 변경 적용 실패: "+err.Error())
				}
				return
			}
			log.Printf("[config] backend.default → %q 적용됨 (즉시 전환)", name)
		},
		Notify: func(msg string) {
			for _, id := range holder.Get().AllowedUserIDs {
				_ = bot.Send(id, msg)
			}
		},
	}
	if stop, werr := WatchConfig(cfgPath, holder, hooks); werr != nil {
		log.Printf("[config] hot-reload 비활성: %v", werr)
	} else {
		defer stop()
	}

	// Capture exe path now — before any rename — for selfRename closure.
	currentExe, _ := os.Executable()

	var notifyChatID int64
	if notifyChat != "" {
		notifyChatID, _ = strconv.ParseInt(notifyChat, 10, 64)
	}

	// ── Handoff mode ──────────────────────────────────────────────────────────
	// Signal old process to exit, then wait until it is fully gone BEFORE
	// starting Telegram polling. Without this wait, both old and new processes
	// poll Telegram simultaneously and kick each other out with 409 Conflict,
	// causing an infinite retry loop.
	if handoffReadyFile != "" {
		// Read old PID before we overwrite the PID file.
		var oldPID int
		if b, err2 := os.ReadFile(pidFilePath()); err2 == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			if pid > 0 && pid != os.Getpid() {
				oldPID = pid
			}
		}

		// Tell old process: we are initialized — exit now.
		if werr := os.WriteFile(handoffReadyFile, []byte("ready"), 0600); werr != nil {
			log.Printf("[main] handoff signal failed: %v", werr)
		} else {
			log.Printf("[main] handoff: signaled old process (PID %d) to exit", oldPID)
		}

		// Block until old process is gone (max 10s), then kill if still alive.
		if oldPID > 0 {
			waitForProcessExit(oldPID, 10*time.Second)
		} else {
			time.Sleep(4 * time.Second) // no PID file — conservative default
		}
		// Extra buffer: let Telegram close the previous polling session.
		time.Sleep(1 * time.Second)
		log.Printf("[main] handoff: old process gone, starting Telegram polling")
	}
	// ─────────────────────────────────────────────────────────────────────────

	// Write PID before bot.Run so the NEXT startup can find and kill us.
	writePIDFile()

	// Web chat transport (localhost only). Started here — AFTER the previous
	// instance is gone (killPreviousInstance for a normal start, or the handoff
	// wait above for --handoff-ready) — so it can actually bind the port. If it
	// started earlier (right after NewBot), a still-running old instance would
	// hold 127.0.0.1:27271 and bind would fail, silently disabling web chat on the
	// new process. Both channels share state via the same Hub + owner chatID.
	// Chat control API (loopback only) — lets a separate aglink-chat process serve
	// the browser UI. Off by default; enabling it does not affect the embedded
	// web_chat server below (both register their own channels with the same Hub).
	// Created before web_chat so the embedded server can reference it for
	// aglink-chat connection status (/api/status).
	var chatCtl *chatControlServer
	if cfg.ChatControl {
		owner, ownerOK := resolveWebOwner(cfg.ChatControlOwnerChatID, cfg.AllowedUserIDs)
		addr := cfg.ChatControlAddr
		if addr == "" {
			addr = "127.0.0.1:27270"
		}
		if tok, terr := loadOrCreateToken(cfg.ChatControlToken, "chat_control.token"); terr != nil {
			log.Printf("[chatcontrol] token init failed: %v — chat control disabled", terr)
		} else if !ownerOK {
			log.Printf("[chatcontrol] no owner chatID (set chat_control.owner_chat_id or allowed_user_ids) — chat control disabled")
		} else {
			chatCtl = &chatControlServer{addr: addr, token: tok, ownerChatID: owner, hub: bot.Hub(), bot: bot, cfgPath: cfgPath}
			go chatCtl.Start()
		}
	}

	// The embedded browser web server was removed in Phase 2 — aglink-chat is now
	// the primary frontend and serves the browser directly (below). aglink
	// keeps only the control API (chatCtl) that aglink-chat connects to.
	//
	// aglink-chat runs as a managed child frontend. Requires the control API
	// (chatCtl). The browser token is the web_chat token (loadOrCreateWebToken) so
	// a browser already connected to the old embedded server keeps authenticating
	// after the swap. Startup-bound — toggling needs a restart.
	if cfg.AglinkChat && chatCtl != nil {
		selfExe, _ := os.Executable()
		binPath := resolveAglinkChatBinary(cfg, selfExe)
		addr := cfg.AglinkChatAddr
		if addr == "" {
			addr = "127.0.0.1:27271"
		}
		btok, terr := loadOrCreateToken(cfg.AglinkChatToken, "web_chat.token")
		switch {
		case binPath == "":
			log.Printf("[aglinkchat] binary not found (set aglink_chat.binary_path or build aglink-chat) — not started")
		case terr != nil:
			log.Printf("[aglinkchat] token init failed: %v — not started", terr)
		default:
			log.Printf("[aglinkchat] http://%s/?token=%s", addr, btok)
			go startAglinkChat(context.Background(), binPath, addr, chatCtl.addr, chatCtl.token, btok)
		}
	}

	// onReady fires after GetUpdatesChan — polling is confirmed active.
	bot.onReady = func() {
		log.Printf("[main] polling active, PID %d", os.Getpid())
		if handoffReadyFile != "" {
			if notifyChatID != 0 {
				_ = bot.Send(notifyChatID, fmt.Sprintf("✅ 새 버전 활성화됨! (PID %d)", os.Getpid()))
			}
			// Rename aglink_new → aglink so the next !update
			// can build to a fresh file (can't overwrite a running exe on Windows).
			if filepath.Base(currentExe) == "aglink_new"+exeSuffix {
				go selfRename(currentExe, bot, notifyChatID)
			}
		}
	}

	bot.Run() // blocks
	return nil
}

// selfRename renames aglink_new → aglink.
// On Windows, renaming a running exe is allowed (kernel tracks by handle, not name).
func selfRename(currentExe string, bot *Bot, notifyChatID int64) {
	target := filepath.Join(filepath.Dir(currentExe), "aglink"+exeSuffix)
	var lastErr error
	for i := 0; i < 10; i++ {
		time.Sleep(time.Second)
		if err := os.Rename(currentExe, target); err == nil {
			log.Printf("[main] self-rename: aglink_new.exe → aglink.exe OK")
			return
		} else {
			lastErr = err
		}
	}
	log.Printf("[main] self-rename failed after 10 attempts: %v", lastErr)
	if notifyChatID != 0 {
		_ = bot.Send(notifyChatID, "⚠️ 이름 변경 실패 — 다음 !update 시 빌드 실패할 수 있습니다: "+lastErr.Error())
	}
}

// chooseBackend picks the effective startup backend. It honors the preferred
// backend (persisted choice or DEFAULT_BACKEND) when that backend is installed;
// otherwise it falls back to whichever backend is installed, in claude → codex →
// opencode order. An empty or unknown preference uses that same default order.
// Returns ok=false only when no backend is available.
func chooseBackend(preferred string, claudeAvail, codexAvail, opencodeAvail bool) (string, bool) {
	avail := map[string]bool{"claude": claudeAvail, "codex": codexAvail, "opencode": opencodeAvail}
	// Preferred backend wins when it's actually installed.
	if avail[preferred] {
		return preferred, true
	}
	// Otherwise fall back to the first installed backend in default order.
	for _, b := range []string{"claude", "codex", "opencode"} {
		if avail[b] {
			return b, true
		}
	}
	return "", false
}

// claudeHealthCheck verifies the claude CLI responds.
func claudeHealthCheck(claudePath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, claudePath, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w (%s)", err, string(out))
	}
	log.Printf("[main] claude version: %s", string(out))
	return nil
}
