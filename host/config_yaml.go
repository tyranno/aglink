package main

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlConfig is the on-disk YAML shape. Pointers/omitempty keep output tidy and
// let us detect "unset" so defaults apply.
type yamlConfig struct {
	HomeDir  string `yaml:"home_dir"`
	Telegram struct {
		BotToken         string   `yaml:"bot_token"`
		AllowedUserIDs   []int64  `yaml:"allowed_user_ids"`
		AllowedUsernames []string `yaml:"allowed_usernames"`
	} `yaml:"telegram"`
	Models struct {
		Manager       string `yaml:"manager"`
		Worker        string `yaml:"worker"`
		ManagerAlways *bool  `yaml:"manager_always"`
	} `yaml:"models"`
	Claude struct {
		Path       string `yaml:"path"`
		OauthToken string `yaml:"oauth_token"`
	} `yaml:"claude"`
	Backend struct {
		Default           string `yaml:"default"`
		CodexPath         string `yaml:"codex_path"`
		CodexModel        string `yaml:"codex_model"`
		CodexManagerModel string `yaml:"codex_manager_model"`
	} `yaml:"backend"`
	// Opencode is its own top-level section (not nested under backend) so the
	// opencode CLI's settings stay clearly separated from claude/codex. aglink
	// only stores how to *invoke* opencode here; provider baseURL/apiKey live in
	// opencode's own opencode.json (pointed at by ConfigPath).
	Opencode struct {
		Path         string `yaml:"path"`
		Model        string `yaml:"model"`
		ManagerModel string `yaml:"manager_model"`
		ConfigPath   string `yaml:"config_path"`
	} `yaml:"opencode"`
	Runtime struct {
		TimeoutMinutes      *int `yaml:"timeout_minutes"`
		MaxWorkers          *int `yaml:"max_workers"`
		RateLimitPerMin     *int `yaml:"rate_limit_per_min"`
		ConversationTTLDays *int `yaml:"conversation_ttl_days"`
	} `yaml:"runtime"`
	Scripts struct {
		Allow           bool     `yaml:"allow"`
		AllowedCommands []string `yaml:"allowed_commands"`
	} `yaml:"scripts"`
	ScreenControl struct {
		Enabled     bool   `yaml:"enabled"`
		PresetsFile string `yaml:"presets_file"`
		Elevated    bool   `yaml:"elevated"`
		KeepAwake   bool   `yaml:"keep_awake"`
		BinaryPath  string `yaml:"binary_path"`
	} `yaml:"screen_control"`
	WebControl struct {
		Enabled    bool   `yaml:"enabled"`
		BinaryPath string `yaml:"binary_path"`
	} `yaml:"web_control"`
	WebChat struct {
		Enabled     bool   `yaml:"enabled"`
		Addr        string `yaml:"addr"`
		Token       string `yaml:"token"`
		OwnerChatID int64  `yaml:"owner_chat_id"`
	} `yaml:"web_chat"`
	ChatControl struct {
		Enabled     bool   `yaml:"enabled"`
		Addr        string `yaml:"addr"`
		Token       string `yaml:"token"`
		OwnerChatID int64  `yaml:"owner_chat_id"`
	} `yaml:"chat_control"`
	AglinkChat struct {
		Enabled    bool   `yaml:"enabled"`
		Addr       string `yaml:"addr"`
		BinaryPath string `yaml:"binary_path"`
		Token      string `yaml:"token"`
	} `yaml:"aglink_chat"`
	InteractiveClaude struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"interactive_claude"`
	// Tools is a name→path registry for external executables (ssh, sshpass, …).
	// Empty/absent → resolve from PATH. See resolveToolPath.
	Tools map[string]string `yaml:"tools,omitempty"`
	// VLLM lists OpenAI-compatible local inference servers; the first is primary,
	// the rest are added as capacity grows. See renderVLLMOpencodeConfig.
	VLLM struct {
		Servers []VLLMServer `yaml:"servers,omitempty"`
	} `yaml:"vllm,omitempty"`
	// Providers stores creds for the built-in free-remote catalog, keyed by
	// catalog id (groq/cerebras/…). See providers.go.
	Providers map[string]ProviderCred `yaml:"providers,omitempty"`
	// CustomProviders are user-defined OpenAI-compatible backends added from the
	// settings UI (same shape as a providers.d drop-in), merged into the effective
	// catalog. See catalogProviders.
	CustomProviders []FreeProvider `yaml:"custom_providers,omitempty"`
	// SSH is the remote-control host registry the !ssh command reaches.
	SSH struct {
		Enabled bool      `yaml:"enabled"`
		Hosts   []SSHHost `yaml:"hosts,omitempty"`
	} `yaml:"ssh,omitempty"`
}

// defaults mirror config.go LoadConfig defaults.
func yamlToConfig(y *yamlConfig) *Config {
	c := &Config{
		ManagerModel:        "haiku",
		TimeoutMinutes:      10,
		ManagerAlways:       true,
		MaxWorkers:          3,
		RateLimitPerMin:     20,
		AllowScripts:        false,
		ConversationTTLDays: 30,
	}
	c.HomeDir = y.HomeDir
	c.TelegramBotToken = y.Telegram.BotToken
	c.AllowedUserIDs = y.Telegram.AllowedUserIDs
	for _, u := range y.Telegram.AllowedUsernames {
		if name := strings.TrimPrefix(strings.TrimSpace(u), "@"); name != "" {
			c.AllowedUsernames = append(c.AllowedUsernames, name)
		}
	}
	if y.Models.Manager != "" {
		c.ManagerModel = y.Models.Manager
	}
	c.WorkerModel = y.Models.Worker
	if y.Models.ManagerAlways != nil {
		c.ManagerAlways = *y.Models.ManagerAlways
	}
	c.ClaudePath = y.Claude.Path
	c.ClaudeOauthToken = y.Claude.OauthToken
	c.DefaultBackend = strings.ToLower(y.Backend.Default)
	c.CodexPath = y.Backend.CodexPath
	c.CodexModel = y.Backend.CodexModel
	c.CodexManagerModel = y.Backend.CodexManagerModel
	c.OpencodePath = y.Opencode.Path
	c.OpencodeModel = y.Opencode.Model
	c.OpencodeManagerModel = y.Opencode.ManagerModel
	c.OpencodeConfigPath = y.Opencode.ConfigPath
	if y.Runtime.TimeoutMinutes != nil {
		c.TimeoutMinutes = *y.Runtime.TimeoutMinutes
	}
	if y.Runtime.MaxWorkers != nil {
		c.MaxWorkers = *y.Runtime.MaxWorkers
	}
	if y.Runtime.RateLimitPerMin != nil {
		c.RateLimitPerMin = *y.Runtime.RateLimitPerMin
	}
	if y.Runtime.ConversationTTLDays != nil {
		c.ConversationTTLDays = *y.Runtime.ConversationTTLDays
	}
	c.AllowScripts = y.Scripts.Allow
	for _, cmd := range y.Scripts.AllowedCommands {
		if s := strings.TrimSpace(cmd); s != "" {
			c.AllowedScriptCommands = append(c.AllowedScriptCommands, s)
		}
	}
	c.ScreenControl = y.ScreenControl.Enabled
	c.ScreenPresetsFile = y.ScreenControl.PresetsFile
	c.ScreenElevated = y.ScreenControl.Elevated
	c.ScreenKeepAwake = y.ScreenControl.KeepAwake
	c.ScreenBinaryPath = y.ScreenControl.BinaryPath
	c.WebControl = y.WebControl.Enabled
	c.WebBinaryPath = y.WebControl.BinaryPath
	c.WebChat = y.WebChat.Enabled
	c.WebChatAddr = y.WebChat.Addr
	if c.WebChatAddr == "" {
		c.WebChatAddr = "127.0.0.1:27271"
	}
	c.WebChatToken = y.WebChat.Token
	c.WebChatOwnerChatID = y.WebChat.OwnerChatID
	c.ChatControl = y.ChatControl.Enabled
	c.ChatControlAddr = y.ChatControl.Addr
	if c.ChatControlAddr == "" {
		c.ChatControlAddr = "127.0.0.1:27270"
	}
	c.ChatControlToken = y.ChatControl.Token
	c.ChatControlOwnerChatID = y.ChatControl.OwnerChatID
	c.AglinkChat = y.AglinkChat.Enabled
	c.AglinkChatAddr = y.AglinkChat.Addr
	if c.AglinkChatAddr == "" {
		c.AglinkChatAddr = "127.0.0.1:27271" // Phase 2: aglink-chat is the primary frontend on 27271
	}
	c.AglinkChatBinaryPath = y.AglinkChat.BinaryPath
	c.AglinkChatToken = y.AglinkChat.Token
	// aglink-chat is a frontend for the control API and cannot work without it.
	// Enabling the frontend therefore implies chat_control — one switch, not two
	// (previously both aglink_chat.enabled AND chat_control.enabled were required,
	// and enabling only the former silently started nothing).
	if c.AglinkChat {
		c.ChatControl = true
	}
	c.InteractiveClaude = y.InteractiveClaude.Enabled
	if len(y.Tools) > 0 {
		c.ToolPaths = y.Tools
	}
	c.VLLMServers = y.VLLM.Servers
	if len(y.Providers) > 0 {
		c.Providers = y.Providers
	}
	c.CustomProviders = y.CustomProviders
	c.SSHEnabled = y.SSH.Enabled
	c.SSHHosts = y.SSH.Hosts
	return c
}

func configToYAML(c *Config) *yamlConfig {
	y := &yamlConfig{}
	y.HomeDir = c.HomeDir
	y.Telegram.BotToken = c.TelegramBotToken
	y.Telegram.AllowedUserIDs = c.AllowedUserIDs
	y.Telegram.AllowedUsernames = c.AllowedUsernames
	y.Models.Manager = c.ManagerModel
	y.Models.Worker = c.WorkerModel
	ma := c.ManagerAlways
	y.Models.ManagerAlways = &ma
	y.Claude.Path = c.ClaudePath
	y.Claude.OauthToken = c.ClaudeOauthToken
	y.Backend.Default = c.DefaultBackend
	y.Backend.CodexPath = c.CodexPath
	y.Backend.CodexModel = c.CodexModel
	y.Backend.CodexManagerModel = c.CodexManagerModel
	y.Opencode.Path = c.OpencodePath
	y.Opencode.Model = c.OpencodeModel
	y.Opencode.ManagerModel = c.OpencodeManagerModel
	y.Opencode.ConfigPath = c.OpencodeConfigPath
	tm, mw, rl, ttl := c.TimeoutMinutes, c.MaxWorkers, c.RateLimitPerMin, c.ConversationTTLDays
	y.Runtime.TimeoutMinutes = &tm
	y.Runtime.MaxWorkers = &mw
	y.Runtime.RateLimitPerMin = &rl
	y.Runtime.ConversationTTLDays = &ttl
	y.Scripts.Allow = c.AllowScripts
	y.Scripts.AllowedCommands = c.AllowedScriptCommands
	y.ScreenControl.Enabled = c.ScreenControl
	y.ScreenControl.PresetsFile = c.ScreenPresetsFile
	y.ScreenControl.Elevated = c.ScreenElevated
	y.ScreenControl.KeepAwake = c.ScreenKeepAwake
	y.ScreenControl.BinaryPath = c.ScreenBinaryPath
	y.WebControl.Enabled = c.WebControl
	y.WebControl.BinaryPath = c.WebBinaryPath
	y.WebChat.Enabled = c.WebChat
	y.WebChat.Addr = c.WebChatAddr
	y.WebChat.Token = c.WebChatToken
	y.WebChat.OwnerChatID = c.WebChatOwnerChatID
	y.ChatControl.Enabled = c.ChatControl
	y.ChatControl.Addr = c.ChatControlAddr
	y.ChatControl.Token = c.ChatControlToken
	y.ChatControl.OwnerChatID = c.ChatControlOwnerChatID
	y.AglinkChat.Enabled = c.AglinkChat
	y.AglinkChat.Addr = c.AglinkChatAddr
	y.AglinkChat.BinaryPath = c.AglinkChatBinaryPath
	y.AglinkChat.Token = c.AglinkChatToken
	y.InteractiveClaude.Enabled = c.InteractiveClaude
	if len(c.ToolPaths) > 0 {
		y.Tools = c.ToolPaths
	}
	y.VLLM.Servers = c.VLLMServers
	if len(c.Providers) > 0 {
		y.Providers = c.Providers
	}
	y.CustomProviders = c.CustomProviders
	y.SSH.Enabled = c.SSHEnabled
	y.SSH.Hosts = c.SSHHosts
	return y
}

func marshalConfigYAML(c *Config) ([]byte, error) {
	return yaml.Marshal(configToYAML(c))
}

// unmarshalConfigYAML parses YAML, applies defaults, and validates.
func unmarshalConfigYAML(b []byte) (*Config, error) {
	var y yamlConfig
	if err := yaml.Unmarshal(b, &y); err != nil {
		return nil, fmt.Errorf("config.yaml 파싱 실패: %w", err)
	}
	c := yamlToConfig(&y)
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}
