package main

import (
	"encoding/json"
	"os"
	"strings"
)

// applySettingsUpdate parses a JSON updates map, applies the whitelisted fields
// to a copy of cfg (keeping secrets/allowlists/other fields intact), and — only
// if the result validates — writes it to cfgPath (fsnotify hot-reloads). Returns
// the control-reply JSON: {"ok":true} or {"ok":false,"error":...}. The structured
// save regenerates the file via marshalConfigYAML (comments not preserved — use
// the raw editor for those).
func applySettingsUpdate(cfgPath string, cfg *Config, body []byte) json.RawMessage {
	fail := func(msg string) json.RawMessage {
		d, _ := json.Marshal(map[string]any{"ok": false, "error": msg})
		return d
	}
	var updates map[string]any
	if err := json.Unmarshal(body, &updates); err != nil {
		return fail("잘못된 요청: " + err.Error())
	}
	newCfg := *cfg // shallow copy; applySettings only touches scalar fields
	if err := applySettings(&newCfg, updates); err != nil {
		return fail(err.Error())
	}
	raw, err := marshalConfigYAML(&newCfg)
	if err != nil {
		return fail("직렬화 실패: " + err.Error())
	}
	if _, verr := unmarshalConfigYAML(raw); verr != nil {
		return fail(verr.Error())
	}
	if werr := os.WriteFile(cfgPath, raw, 0o600); werr != nil {
		return fail("기록 실패: " + werr.Error())
	}
	d, _ := json.Marshal(map[string]any{"ok": true})
	return d
}

// settingField is one editable config field surfaced to the structured settings
// UI, with the metadata the UI needs to render it (label, human description,
// input type) plus its current value. Secrets and allowlists are deliberately
// NOT exposed here — those stay in the raw config editor.
type settingField struct {
	Key     string   `json:"key"`   // dotted key, e.g. "runtime.max_workers"
	Label   string   `json:"label"` // short field name
	Desc    string   `json:"desc"`  // what it means / effect
	Type    string   `json:"type"`  // "string" | "int" | "bool" | "select"
	Value   any      `json:"value"`
	Options []string `json:"options,omitempty"` // for type "select"
}

type settingSection struct {
	Title  string         `json:"title"`
	Fields []settingField `json:"fields"`
}

// codexModelOptionsFor returns the live-detected codex model catalog for the
// manager's active codex client, or nil if codex isn't configured/installed
// or the installed codex-cli is too old to support `debug models`. Uses a
// small optional interface (rather than depending on the concrete
// *codexRunner type) matching how the manager already treats codex-specific
// capabilities as optional, e.g. backendReadiness.
func codexModelOptionsFor(m *Manager) []string {
	if m == nil || m.codexClient == nil {
		return nil
	}
	type modelCataloger interface{ modelCatalog() []string }
	if mc, ok := m.codexClient.(modelCataloger); ok {
		return mc.modelCatalog()
	}
	return nil
}

// claudeModelAliases are Claude Code's own documented `--model` aliases
// (`claude --help`: "Provide an alias for the latest model (e.g. 'fable',
// 'opus', or 'sonnet')"), plus "haiku" which the CLI resolves the same way
// and which this project already uses as the default manager model. Claude
// Code has no subcommand that dumps a live model catalog (unlike codex's
// `debug models`), so this is a static-but-authoritative list: each alias
// always resolves to that tier's current model, so it doesn't go stale the
// way a hardcoded full model ID would.
var claudeModelAliases = []string{"haiku", "sonnet", "opus", "fable"}

// selectOptionsWithBlank prefixes an options list with "" (rendered as
// "기본값" by the UI) so a field can be cleared back to "use the backend's
// default" without leaving the select stuck on an unrelated first option.
func selectOptionsWithBlank(options []string) []string {
	return append([]string{""}, options...)
}

// modelField builds a models.* / backend.codex_*_model setting field. When
// detectedOptions is non-empty it renders as a "select" populated with those
// options (plus a blank "기본값" entry); otherwise it falls back to a plain
// "string" field so a codex install too old for `debug models` (or simply
// not installed) still lets the user type a model name by hand instead of
// showing a broken, optionless dropdown.
func modelField(key, label, desc string, value string, detectedOptions []string) settingField {
	if len(detectedOptions) == 0 {
		return settingField{Key: key, Label: label, Desc: desc, Type: "string", Value: value}
	}
	return settingField{Key: key, Label: label, Desc: desc, Type: "select", Value: value, Options: selectOptionsWithBlank(detectedOptions)}
}

// buildSettings returns the curated, safe subset of config fields (with their
// current values) that the structured settings form can edit. The keys here MUST
// match applySettings' whitelist. codexModels is the live-detected codex model
// catalog (nil if codex isn't installed or detection failed) — see
// codexRunner.modelCatalog.
func buildSettings(cfg *Config, codexModels []string) []settingSection {
	return []settingSection{
		{Title: "모델", Fields: []settingField{
			modelField("models.manager", "매니저 모델", "메시지 라우팅·스케줄 판단에 쓰는 가벼운 모델. 비우면 백엔드 기본값 사용.", cfg.ManagerModel, claudeModelAliases),
			modelField("models.worker", "워커 모델", "실제 작업을 수행하는 모델. 비우면 백엔드 기본값 사용.", cfg.WorkerModel, claudeModelAliases),
			{Key: "models.manager_always", Label: "항상 매니저 경유", Desc: "켜면 모든 메시지를 매니저 모델로 먼저 라우팅. 끄면 단순 메시지는 바로 워커로.", Type: "bool", Value: cfg.ManagerAlways},
		}},
		{Title: "백엔드", Fields: []settingField{
			{Key: "backend.default", Label: "기본 백엔드", Desc: "부팅 시 사용할 AI 백엔드. 채팅 중 !backend로도 전환 가능.", Type: "select", Value: cfg.DefaultBackend, Options: []string{"claude", "codex"}},
			modelField("backend.codex_model", "Codex 워커 모델", "Codex 백엔드일 때 워커 모델. 설치된 codex-cli에서 실제 검사한 목록입니다.", cfg.CodexModel, codexModels),
			modelField("backend.codex_manager_model", "Codex 매니저 모델", "Codex 백엔드일 때 매니저 모델. 설치된 codex-cli에서 실제 검사한 목록입니다.", cfg.CodexManagerModel, codexModels),
			{Key: "interactive_claude.enabled", Label: "Interactive Claude (실험적)", Desc: "상주 ConPTY 세션 백엔드 구성. 웹 대화별로 \"!interactive on\"으로 켜야 실제 사용됨. Windows 전용. (변경 시 재시작 필요)", Type: "bool", Value: cfg.InteractiveClaude},
		}},
		{Title: "런타임", Fields: []settingField{
			{Key: "runtime.timeout_minutes", Label: "작업 타임아웃(분)", Desc: "한 턴이 이 시간을 넘기면 취소.", Type: "int", Value: cfg.TimeoutMinutes},
			{Key: "runtime.max_workers", Label: "동시 작업 수", Desc: "동시에 실행할 수 있는 워커(작업) 개수.", Type: "int", Value: cfg.MaxWorkers},
			{Key: "runtime.rate_limit_per_min", Label: "분당 요청 제한", Desc: "사용자당 1분에 허용하는 비-명령 메시지 수.", Type: "int", Value: cfg.RateLimitPerMin},
			{Key: "runtime.conversation_ttl_days", Label: "대화 보관(일)", Desc: "이 기간 동안 활동 없는 대화/히스토리를 자동 정리. 0=비활성.", Type: "int", Value: cfg.ConversationTTLDays},
		}},
		{Title: "실행 / 보안", Fields: []settingField{
			{Key: "scripts.allow", Label: "스크립트 실행 허용", Desc: "봇이 셸 스크립트/명령을 실행하도록 허용. 신중히.", Type: "bool", Value: cfg.AllowScripts},
			{Key: "screen_control.enabled", Label: "화면 제어(aglink-screen)", Desc: "스크린샷·화면 제어 MCP 사용.", Type: "bool", Value: cfg.ScreenControl},
			{Key: "screen_control.keep_awake", Label: "화면 잠금 방지", Desc: "화면 제어 중 화면보호기/잠금 방지.", Type: "bool", Value: cfg.ScreenKeepAwake},
			{Key: "screen_control.elevated", Label: "관리자 권한 실행", Desc: "화면 제어를 관리자 권한으로 실행.", Type: "bool", Value: cfg.ScreenElevated},
		}},
		{Title: "연결", Fields: []settingField{
			{Key: "aglink_chat.enabled", Label: "aglink-chat 프론트", Desc: "teleclaude가 aglink-chat 웹 프론트를 자식으로 기동. (변경 시 재시작 필요)", Type: "bool", Value: cfg.AglinkChat},
			{Key: "aglink_chat.addr", Label: "aglink-chat 주소", Desc: "웹 프론트 bind 주소:포트 (예: 127.0.0.1:1717). (변경 시 재시작 필요)", Type: "string", Value: cfg.AglinkChatAddr},
			{Key: "chat_control.enabled", Label: "제어 API", Desc: "aglink-chat이 붙는 제어 API 서버. (변경 시 재시작 필요)", Type: "bool", Value: cfg.ChatControl},
			{Key: "chat_control.addr", Label: "제어 API 주소", Desc: "제어 API bind 주소:포트 (예: 127.0.0.1:17170). (변경 시 재시작 필요)", Type: "string", Value: cfg.ChatControlAddr},
			{Key: "web_chat.enabled", Label: "임베디드 웹챗(레거시)", Desc: "구 임베디드 웹 서버. Phase 2 이후 무동작(호환용).", Type: "bool", Value: cfg.WebChat},
			{Key: "web_chat.addr", Label: "임베디드 웹챗 주소(레거시)", Desc: "구 임베디드 웹 서버 주소. 무동작.", Type: "string", Value: cfg.WebChatAddr},
		}},
	}
}

// applySettings mutates cfg with the whitelisted updates map (dotted key →
// value). Unknown keys are ignored (the raw editor covers everything else), so a
// client can never inject an arbitrary field. Values arrive as JSON scalars
// (float64 / bool / string); asInt/asBool/asString coerce them.
func applySettings(cfg *Config, updates map[string]any) error {
	for k, v := range updates {
		switch k {
		case "models.manager":
			cfg.ManagerModel = asString(v)
		case "models.worker":
			cfg.WorkerModel = asString(v)
		case "models.manager_always":
			cfg.ManagerAlways = asBool(v)
		case "backend.default":
			cfg.DefaultBackend = strings.ToLower(asString(v))
		case "backend.codex_model":
			cfg.CodexModel = asString(v)
		case "backend.codex_manager_model":
			cfg.CodexManagerModel = asString(v)
		case "interactive_claude.enabled":
			cfg.InteractiveClaude = asBool(v)
		case "runtime.timeout_minutes":
			cfg.TimeoutMinutes = asInt(v)
		case "runtime.max_workers":
			cfg.MaxWorkers = asInt(v)
		case "runtime.rate_limit_per_min":
			cfg.RateLimitPerMin = asInt(v)
		case "runtime.conversation_ttl_days":
			cfg.ConversationTTLDays = asInt(v)
		case "scripts.allow":
			cfg.AllowScripts = asBool(v)
		case "screen_control.enabled":
			cfg.ScreenControl = asBool(v)
		case "screen_control.keep_awake":
			cfg.ScreenKeepAwake = asBool(v)
		case "screen_control.elevated":
			cfg.ScreenElevated = asBool(v)
		case "web_chat.enabled":
			cfg.WebChat = asBool(v)
		case "web_chat.addr":
			cfg.WebChatAddr = asString(v)
		case "chat_control.enabled":
			cfg.ChatControl = asBool(v)
		case "chat_control.addr":
			cfg.ChatControlAddr = asString(v)
		case "aglink_chat.enabled":
			cfg.AglinkChat = asBool(v)
		case "aglink_chat.addr":
			cfg.AglinkChatAddr = asString(v)
		default:
			// Unknown/unsafe key — ignore (raw editor handles the rest).
		}
	}
	return nil
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1" || t == "on"
	default:
		return false
	}
}

func asInt(v any) int {
	switch t := v.(type) {
	case float64: // JSON numbers decode to float64
		return int(t)
	case int:
		return t
	case string:
		return atoiOr(t, 0)
	default:
		return 0
	}
}
