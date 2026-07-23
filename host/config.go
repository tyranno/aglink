package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Design Ref: §4.2, §8.3 — config.txt (key=value) parsing + claude path auto-detect.

// legacyDataDirName is the pre-rename data dir (project was called teleclaude).
// Installs created before the aglink rename keep all their state — config.yaml,
// store.json, tasks.json, history/ — under this name.
const legacyDataDirName = ".teleclaude"

// dataDirEnv overrides the data directory outright. Set it to run a second
// instance (different config, ports, store) alongside an existing install
// without the two fighting over one directory.
const dataDirEnv = "AGLINK_HOME"

// isolatedDataDir reports whether this process was pointed at an explicit data
// directory, i.e. it is a deliberate parallel instance rather than the machine's
// main install. Startup cleanup that reaches other processes is suppressed for
// these (see killPreviousInstance).
func isolatedDataDir() bool {
	return strings.TrimSpace(os.Getenv(dataDirEnv)) != ""
}

// dataDir returns %USERPROFILE%\.aglink (created if missing). If this is a
// first aglink start on a machine that still has the pre-rename ~/.teleclaude
// directory, the legacy state is copied into ~/.aglink and the copied config is
// normalized for the post-rename desktop/chat ports.
func dataDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv(dataDirEnv)); v != "" {
		if err := os.MkdirAll(v, 0o700); err != nil {
			return "", err
		}
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".aglink")
	if st, err := os.Stat(dir); err == nil {
		if !st.IsDir() {
			return "", fmt.Errorf("%s exists but is not a directory", dir)
		}
		return dir, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	legacy := filepath.Join(home, legacyDataDirName)
	if st, err := os.Stat(legacy); err == nil && st.IsDir() {
		if err := migrateLegacyDataDir(legacy, dir); err != nil {
			return "", err
		}
		return dir, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func migrateLegacyDataDir(legacy, dir string) error {
	tmp := filepath.Join(filepath.Dir(dir), ".aglink.migrating-"+strconv.Itoa(os.Getpid()))
	_ = os.RemoveAll(tmp)
	defer os.RemoveAll(tmp)

	if err := copyLegacyDataDir(legacy, tmp); err != nil {
		return fmt.Errorf("migrate %s to %s: %w", legacy, dir, err)
	}
	if err := normalizeMigratedConfig(tmp); err != nil {
		return fmt.Errorf("normalize migrated config: %w", err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		if st, serr := os.Stat(dir); serr == nil && st.IsDir() {
			return nil
		}
		return fmt.Errorf("activate migrated data dir: %w", err)
	}
	log.Printf("[config] migrated legacy data dir %s -> %s", legacy, dir)
	return nil
}

func copyLegacyDataDir(legacy, dst string) error {
	return filepath.WalkDir(legacy, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(legacy, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o700)
		}
		if skipLegacyRuntimeFile(rel, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyRegularFile(path, target, info.Mode().Perm())
	})
}

func skipLegacyRuntimeFile(rel string, d os.DirEntry) bool {
	if d.IsDir() {
		return false
	}
	base := strings.ToLower(filepath.Base(rel))
	if strings.HasSuffix(base, ".pid") || strings.HasSuffix(base, ".log") || strings.Contains(base, ".log.") {
		return true
	}
	switch base {
	case "screen-control.lock", "aglink-web.port":
		return true
	default:
		return false
	}
}

func copyRegularFile(src, dst string, perm os.FileMode) error {
	if perm == 0 {
		perm = 0o600
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func normalizeMigratedConfig(dir string) error {
	cfg, cfgPath, err := LoadOrMigrate(dir)
	if err != nil {
		if _, yerr := os.Stat(filepath.Join(dir, "config.yaml")); os.IsNotExist(yerr) {
			if _, terr := os.Stat(filepath.Join(dir, "config.txt")); os.IsNotExist(terr) {
				return nil
			}
		}
		return err
	}

	if cfg.WebChat || cfg.ChatControl || cfg.AglinkChat {
		cfg.WebChat = false
		cfg.WebChatAddr = "127.0.0.1:27271"
		cfg.ChatControl = true
		cfg.ChatControlAddr = "127.0.0.1:27270"
		cfg.AglinkChat = true
		cfg.AglinkChatAddr = "127.0.0.1:27271"
	}
	out, err := marshalConfigYAML(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, out, 0o600)
}

// defaultConfigPath returns the standard config.txt location.
func defaultConfigPath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.txt"), nil
}

// defaultYAMLPath returns <data dir>/config.yaml.
func defaultYAMLPath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// LoadOrMigrate loads config.yaml from dir; if absent but config.txt exists,
// migrates it to config.yaml (and renames the txt to .bak). Returns the path used.
func LoadOrMigrate(dir string) (*Config, string, error) {
	yamlPath := filepath.Join(dir, "config.yaml")
	txtPath := filepath.Join(dir, "config.txt")

	if b, err := os.ReadFile(yamlPath); err == nil {
		cfg, perr := unmarshalConfigYAML(b)
		return cfg, yamlPath, perr
	}
	// No YAML — try migrating from txt.
	if _, err := os.Stat(txtPath); err == nil {
		cfg, lerr := LoadConfig(txtPath)
		if lerr != nil {
			return nil, txtPath, lerr
		}
		out, merr := marshalConfigYAML(cfg)
		if merr != nil {
			return nil, txtPath, merr
		}
		if werr := os.WriteFile(yamlPath, out, 0o600); werr != nil {
			return nil, txtPath, werr
		}
		if rerr := os.Rename(txtPath, txtPath+".bak"); rerr != nil {
			log.Printf("[config] config.txt → .bak 이름변경 실패(무시): %v", rerr)
		}
		log.Printf("[config] config.txt → config.yaml 마이그레이션 완료 (txt는 .bak로 보존)")
		return cfg, yamlPath, nil
	}
	return nil, yamlPath, fmt.Errorf("설정 파일이 없습니다: %s 또는 %s", yamlPath, txtPath)
}

// LoadConfig parses a key=value config file. Lines starting with # are comments.
// If path ends in .yaml/.yml it is parsed as YAML instead.
func LoadConfig(path string) (*Config, error) {
	if strings.HasSuffix(strings.ToLower(path), ".yaml") || strings.HasSuffix(strings.ToLower(path), ".yml") {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("설정 파일 열기 실패 (%s): %w", path, err)
		}
		return unmarshalConfigYAML(b)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("설정 파일 열기 실패 (%s): %w", path, err)
	}
	defer f.Close()

	cfg := &Config{
		ManagerModel:        "haiku",
		TimeoutMinutes:      10,
		ManagerAlways:       true,
		MaxWorkers:          3,
		RateLimitPerMin:     20,
		AllowScripts:        false,
		ConversationTTLDays: 30,
	}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if err := applyConfigKV(cfg, key, val); err != nil {
			return nil, err
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyConfigKV(cfg *Config, key, val string) error {
	switch strings.ToUpper(key) {
	case "TELEGRAM_BOT_TOKEN":
		cfg.TelegramBotToken = val
	case "ALLOWED_USER_IDS":
		ids, err := parseUserIDs(val)
		if err != nil {
			return err
		}
		cfg.AllowedUserIDs = ids
	case "MANAGER_MODEL":
		if val != "" {
			cfg.ManagerModel = val
		}
	case "WORKER_MODEL":
		cfg.WorkerModel = val
	case "CLAUDE_PATH":
		cfg.ClaudePath = val
	case "CLAUDE_CODE_OAUTH_TOKEN":
		cfg.ClaudeOauthToken = val
	case "TIMEOUT_MINUTES":
		if val != "" {
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return fmt.Errorf("TIMEOUT_MINUTES는 양의 정수여야 합니다: %q", val)
			}
			cfg.TimeoutMinutes = n
		}
	case "MANAGER_ALWAYS":
		cfg.ManagerAlways = parseBool(val, true)
	case "CODEX_PATH":
		cfg.CodexPath = val
	case "CODEX_MODEL":
		cfg.CodexModel = val
	case "CODEX_MANAGER_MODEL":
		cfg.CodexManagerModel = val
	case "DEFAULT_BACKEND":
		cfg.DefaultBackend = strings.ToLower(val)
	case "MAX_WORKERS":
		if val != "" {
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return fmt.Errorf("MAX_WORKERS는 양의 정수여야 합니다: %q", val)
			}
			cfg.MaxWorkers = n
		}
	case "RATE_LIMIT_PER_MIN":
		if val != "" {
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 {
				return fmt.Errorf("RATE_LIMIT_PER_MIN는 0 이상 정수여야 합니다: %q", val)
			}
			cfg.RateLimitPerMin = n
		}
	case "ALLOW_SCRIPTS":
		cfg.AllowScripts = parseBool(val, false)
	case "CONVERSATION_TTL_DAYS":
		if val != "" {
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 {
				return fmt.Errorf("CONVERSATION_TTL_DAYS는 0 이상 정수여야 합니다: %q", val)
			}
			cfg.ConversationTTLDays = n
		}
	case "ALLOWED_SCRIPT_COMMANDS":
		for _, cmd := range strings.Split(val, ",") {
			if c := strings.TrimSpace(cmd); c != "" {
				cfg.AllowedScriptCommands = append(cfg.AllowedScriptCommands, c)
			}
		}
	case "ALLOWED_USERNAMES":
		for _, u := range strings.Split(val, ",") {
			if name := strings.TrimPrefix(strings.TrimSpace(u), "@"); name != "" {
				cfg.AllowedUsernames = append(cfg.AllowedUsernames, name)
			}
		}
	}
	return nil
}

func parseUserIDs(val string) ([]int64, error) {
	var ids []int64
	for _, p := range strings.Split(val, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("ALLOWED_USER_IDS에 잘못된 값: %q", p)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func parseBool(val string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		return def
	}
}

const maxAllowedWorkers = 50

func (c *Config) validate() error {
	// Web-chat-only mode: aglink can boot without a Telegram bot token as long
	// as a web frontend is enabled (chat_control/aglink_chat, or the legacy web_chat).
	// In that mode Telegram polling is skipped and the browser UI is the only surface.
	webFrontendEnabled := c.ChatControl || c.AglinkChat || c.WebChat
	if c.TelegramBotToken == "" && !webFrontendEnabled {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN이 설정되지 않았습니다 (웹채팅만 쓰려면 chat_control 또는 aglink_chat을 활성화하세요)")
	}
	if len(c.AllowedUserIDs) == 0 {
		return fmt.Errorf("ALLOWED_USER_IDS가 비어 있습니다 (보안상 최소 1개 필요)")
	}
	if c.DefaultBackend != "" && c.DefaultBackend != "claude" && c.DefaultBackend != "codex" && c.DefaultBackend != "opencode" {
		return fmt.Errorf("DEFAULT_BACKEND는 'claude', 'codex', 'opencode' 중 하나여야 합니다: %q", c.DefaultBackend)
	}
	if c.MaxWorkers > maxAllowedWorkers {
		return fmt.Errorf("MAX_WORKERS는 최대 %d까지 허용됩니다 (입력값: %d)", maxAllowedWorkers, c.MaxWorkers)
	}
	return nil
}

// IsAllowed reports whether the given Telegram user ID may use the bot.
func (c *Config) IsAllowed(userID int64) bool {
	return slices.Contains(c.AllowedUserIDs, userID)
}

// IsAllowedByUsername reports whether the given Telegram username (without @) may use the bot.
// Returns false when username is empty.
func (c *Config) IsAllowedByUsername(username string) bool {
	if username == "" {
		return false
	}
	return slices.Contains(c.AllowedUsernames, username)
}

// findClaude resolves the claude CLI path: explicit > PATH > platform-specific locations.
func findClaude(explicit string) (string, error) {
	// preferNativeClaude unwraps a Windows claude.cmd/.ps1 shim to the native
	// bin\claude.exe (no-op elsewhere) so workers exec claude directly rather
	// than through cmd.exe, which would mangle plugin MCP args. Applied to every
	// resolution path — explicit config, PATH lookup, and OS candidates.
	if explicit != "" {
		if _, err := os.Stat(explicit); err == nil {
			return preferNativeClaude(explicit), nil
		}
		return "", fmt.Errorf("CLAUDE_PATH가 존재하지 않습니다: %s", explicit)
	}
	if p, err := exec.LookPath("claude"); err == nil {
		return preferNativeClaude(p), nil
	}
	home, _ := os.UserHomeDir()
	for _, c := range findClaudeOS(home) {
		if _, err := os.Stat(c); err == nil {
			return preferNativeClaude(c), nil
		}
	}
	return "", fmt.Errorf("claude CLI를 찾을 수 없습니다. PATH에 추가하거나 CLAUDE_PATH를 설정하세요")
}

// findCodex returns the codex CLI path (explicit override or PATH lookup).
// Returns ("", nil) if not installed — codex is optional.
func findCodex(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("codex 경로 없음: %s", explicit)
		}
		return explicit, nil
	}
	p, err := exec.LookPath("codex")
	if err != nil {
		return "", nil // not installed — not an error
	}
	return p, nil
}

// findOpencode returns the opencode CLI path (explicit override or PATH lookup).
// Returns ("", nil) if not installed — opencode is optional, exactly like codex,
// so a machine without it still boots and simply can't select the opencode
// backend.
func findOpencode(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("opencode 경로 없음: %s", explicit)
		}
		return explicit, nil
	}
	p, err := exec.LookPath("opencode")
	if err != nil {
		return "", nil // not installed — not an error
	}
	return p, nil
}
