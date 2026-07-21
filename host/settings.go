package main

import (
	"encoding/json"
	"fmt"
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
	newCfg := *cfg // shallow copy
	// applySettings now also edits the ToolPaths map and VLLMServers slice, which a
	// shallow copy shares with the live cfg. Clone them so a rejected/invalid save
	// never mutates the running config in place (the other fields are scalars).
	if cfg.ToolPaths != nil {
		newCfg.ToolPaths = make(map[string]string, len(cfg.ToolPaths))
		for k, v := range cfg.ToolPaths {
			newCfg.ToolPaths[k] = v
		}
	}
	if len(cfg.VLLMServers) > 0 {
		newCfg.VLLMServers = append([]VLLMServer(nil), cfg.VLLMServers...)
	}
	// applySettings also grows/rewrites CustomProviders in place (setCustomProviderField),
	// which the shallow copy shares — clone it too so a rejected save leaves the live one intact.
	if len(cfg.CustomProviders) > 0 {
		newCfg.CustomProviders = append([]FreeProvider(nil), cfg.CustomProviders...)
	}
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

// settingVisibility gates a whole section on the *live* value of another field
// in the same form (evaluated client-side as the user edits, before saving). It
// lets opencode-only sections stay hidden until the user actually picks the
// opencode backend, instead of cluttering the form for claude/codex users.
type settingVisibility struct {
	Key    string `json:"key"`    // dotted key of the controlling field, e.g. "backend.default"
	Equals string `json:"equals"` // section shows only when that field's value equals this
}

type settingSection struct {
	Title string `json:"title"`
	// Desc is a one-line, plain-language explanation of what this whole group is
	// for and when a user would touch it — shown under the section title so a
	// non-developer knows whether this section is even relevant to them. Optional.
	Desc string `json:"desc,omitempty"`
	// Group is the settings tab this section lives under. The UI renders one tab
	// per distinct group (in first-seen order) and shows only the active tab's
	// sections, so a form with many sections stays tidy instead of one long scroll.
	Group string `json:"group,omitempty"`
	// VisibleWhen, when set, hides this section until another field's live value
	// matches — e.g. opencode provider settings appear only once "사용할 AI" is
	// opencode. A tab whose sections are all hidden hides its tab button too.
	VisibleWhen *settingVisibility `json:"visibleWhen,omitempty"`
	// Advanced marks sections that most users never need (runtime tuning, raw
	// tool paths, legacy toggles). Retained for metadata; the tabbed UI groups by
	// Group rather than collapsing a single "고급 설정" disclosure.
	Advanced bool           `json:"advanced,omitempty"`
	Fields   []settingField `json:"fields"`
}

// Settings tab (Group) names. Sections are bucketed into these tabs so the form
// reads as a few tidy pages instead of one long scroll.
const (
	settingsGroupAI       = "AI 선택"
	settingsGroupFreeAI   = "무료·로컬 AI"
	settingsGroupLimits   = "동작·한도"
	settingsGroupSecurity = "보안·원격"
	settingsGroupNetwork  = "연결·기타"
)

// whenOpencode gates a section on the opencode backend being selected, so its
// provider/model/vLLM settings stay hidden for claude/codex users.
var whenOpencode = &settingVisibility{Key: "backend.default", Equals: "opencode"}

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

// freeProviderFields renders the built-in free-remote catalog as settings rows:
// per provider an API-key field (paste a key to mount it) and an optional model
// override. The section is fully data-driven from freeProviderCatalog, so adding
// a catalog entry surfaces it here with no further UI wiring. A provider mounts
// only when its key is non-empty; then reference it from the worker model as
// "<id>/<model>" (e.g. groq/llama-3.3-70b-versatile) to try one at a time.
func freeProviderFields(cfg *Config) []settingField {
	cat := catalogProviders(cfg)
	fields := make([]settingField, 0, len(cat)*2)
	for _, p := range cat {
		cred := providerCred(cfg, p.ID)
		mounted := "미설정"
		if strings.TrimSpace(cred.APIKey) != "" {
			mounted = "활성"
		}
		fields = append(fields,
			settingField{
				Key:   "provider." + p.ID + ".key",
				Label: p.Name + " API 키",
				Desc: fmt.Sprintf("%s 붙여넣으면 활성(%s). %s 키 발급: %s · 워커 모델을 '%s/%s'로 지정해 사용.",
					p.FreeNote, mounted, p.Name, p.SignupURL, p.ID, p.DefaultModel),
				Type:  "string",
				Value: cred.APIKey,
			},
			settingField{
				Key:   "provider." + p.ID + ".model",
				Label: p.Name + " 모델(선택)",
				Desc:  fmt.Sprintf("비우면 기본값 '%s' 사용. 참조 id는 %s/<모델>.", p.DefaultModel, p.ID),
				Type:  "string",
				Value: cred.Model,
			},
		)
	}
	return fields
}

// customProviderSlots is how many UI-defined custom-provider slots the scalar
// settings form exposes. Like vLLM's primary/secondary, a small fixed count
// keeps the form manageable; heavier fleets can still be added via providers.d.
const customProviderSlots = 2

// customProviderFields renders the "add a brand-new provider from the UI" slots.
// Each slot defines an OpenAI-compatible backend (id/base_url/default_model,
// optional display name). Once saved, catalogProviders() picks the slot up and
// the "무료 원격 프로바이더" section grows a key row for it — so this section is
// only the *definition*; the API key is entered there. Mirrors the vLLM scalar
// slots (see vllm.go) rather than a dynamic list, which the scalar form can't do.
func customProviderFields(cfg *Config) []settingField {
	fields := make([]settingField, 0, customProviderSlots*4)
	for i := 0; i < customProviderSlots; i++ {
		p := customProviderAt(cfg, i)
		n := i + 1
		prefix := fmt.Sprintf("custom_provider.%d.", n)
		defined := ""
		if strings.TrimSpace(p.ID) != "" {
			defined = fmt.Sprintf(" 저장하면 아래 '무료 원격 프로바이더'에 '%s' API 키 입력란이 생깁니다.", p.ID)
		}
		fields = append(fields,
			settingField{Key: prefix + "id", Label: fmt.Sprintf("커스텀 %d — id", n),
				Desc:  "opencode provider id(영문/숫자). 워커 모델을 '<id>/<모델>'로 참조." + defined,
				Type:  "string", Value: p.ID},
			settingField{Key: prefix + "base_url", Label: fmt.Sprintf("커스텀 %d — base_url", n),
				Desc:  "OpenAI 호환 엔드포인트 루트. 예: https://api.together.xyz/v1",
				Type:  "string", Value: p.BaseURL},
			settingField{Key: prefix + "model", Label: fmt.Sprintf("커스텀 %d — 기본 모델", n),
				Desc:  "기본(권장) 모델 id. 키 행에서 모델을 비우면 이 값 사용.",
				Type:  "string", Value: p.DefaultModel},
			settingField{Key: prefix + "name", Label: fmt.Sprintf("커스텀 %d — 이름(선택)", n),
				Desc:  "UI 표시용 이름. 비우면 id를 사용.",
				Type:  "string", Value: p.Name},
		)
	}
	return fields
}

// buildSettings returns the curated, safe subset of config fields (with their
// current values) that the structured settings form can edit. The keys here MUST
// match applySettings' whitelist. codexModels is the live-detected codex model
// catalog (nil if codex isn't installed or detection failed) — see
// codexRunner.modelCatalog.
func buildSettings(cfg *Config, codexModels []string) []settingSection {
	return []settingSection{
		// ── AI 선택 탭 ─────────────────────────────────────────────────
		{Title: "① 어떤 AI를 쓸까요", Group: settingsGroupAI, Desc: "봇이 답할 때 사용할 AI를 고릅니다. claude·codex는 각 프로그램이 설치돼 있어야 하고, opencode는 아래 '무료 AI 연결'에 키만 넣으면 무료로 쓸 수 있습니다. 채팅 중 !backend로도 바꿀 수 있어요.", Fields: []settingField{
			{Key: "backend.default", Label: "사용할 AI", Desc: "claude = Claude Code, codex = OpenAI Codex, opencode = 무료/로컬 모델 연결용. opencode를 고르면 아래 '무료·로컬 AI' 탭이 열립니다.", Type: "select", Value: cfg.DefaultBackend, Options: []string{"claude", "codex", "opencode"}},
		}},
		{Title: "모델 (claude·codex용)", Group: settingsGroupAI, Desc: "claude나 codex를 쓸 때 어느 모델로 답할지 정합니다. 비워두면 각 백엔드의 기본값을 씁니다 — 잘 모르면 그대로 두세요.", Fields: []settingField{
			modelField("models.manager", "매니저 모델", "메시지를 어디로 보낼지 판단하는 가벼운 모델. 비우면 기본값.", cfg.ManagerModel, claudeModelAliases),
			modelField("models.worker", "작업 모델", "실제 답을 만드는 모델. 비우면 기본값.", cfg.WorkerModel, claudeModelAliases),
			modelField("backend.codex_model", "Codex 작업 모델", "codex를 쓸 때의 작업 모델. 설치된 codex에서 실제 확인한 목록입니다.", cfg.CodexModel, codexModels),
			modelField("backend.codex_manager_model", "Codex 매니저 모델", "codex를 쓸 때의 매니저 모델.", cfg.CodexManagerModel, codexModels),
			{Key: "models.manager_always", Label: "항상 매니저 먼저", Desc: "켜면 모든 메시지를 매니저 모델이 먼저 훑어 분배합니다. 끄면 간단한 메시지는 바로 작업 모델로 갑니다.", Type: "bool", Value: cfg.ManagerAlways},
		}},

		// ── 무료·로컬 AI 탭 (opencode 선택 시에만 표시) ────────────────
		{Title: "② 무료 AI 연결 (opencode용)", Group: settingsGroupFreeAI, VisibleWhen: whenOpencode, Desc: "opencode에 쓸 무료 서비스입니다. 아래 서비스 중 하나에 가입해 받은 API 키를 붙여넣으세요. 키를 넣은 서비스만 '활성'이 됩니다. 그다음 ③에서 그 모델을 고르면 끝입니다.", Fields: freeProviderFields(cfg)},
		{Title: "③ opencode 세부 (모델 선택)", Group: settingsGroupFreeAI, VisibleWhen: whenOpencode, Desc: "opencode 백엔드가 실제로 쓸 모델을 고릅니다. ②에서 키를 넣은 서비스를 'provider/model' 형태로 적으면 됩니다(예: gemini/gemini-flash-latest). 나머지 칸은 대부분 비워둬도 됩니다.", Fields: []settingField{
			{Key: "opencode.model", Label: "사용할 모델", Desc: "형식은 'provider/모델'. 예: gemini/gemini-flash-latest, groq/llama-3.3-70b-versatile, cerebras/gpt-oss-120b, vllm/<모델>. ②에서 키를 넣은 서비스 이름을 앞에 씁니다. 비우면 opencode 기본값.", Type: "string", Value: cfg.OpencodeModel},
			{Key: "opencode.manager_model", Label: "매니저 모델(선택)", Desc: "메시지 분배 판단에만 쓰는 가벼운 모델. 비우면 위 '사용할 모델'과 같은 걸 씁니다. 대부분 비워둡니다.", Type: "string", Value: cfg.OpencodeManagerModel},
			{Key: "opencode.path", Label: "opencode 실행파일 경로(선택)", Desc: "보통 비워두면 자동으로 찾습니다. 특별한 위치에 설치했을 때만 지정. (변경 시 재시작 필요)", Type: "string", Value: cfg.OpencodePath},
			{Key: "opencode.config_path", Label: "opencode.json 경로(선택)", Desc: "직접 만든 opencode 설정 파일이 있을 때만 지정. 비워두면 자동 처리됩니다.", Type: "string", Value: cfg.OpencodeConfigPath},
		}},
		{Title: "직접 추가한 AI 서비스", Group: settingsGroupFreeAI, VisibleWhen: whenOpencode, Advanced: true, Desc: "목록에 없는 OpenAI 호환 서비스를 직접 등록합니다. 여기에 id·주소·모델을 저장하면 위 '무료 AI 연결'에 그 서비스의 키 입력란이 생깁니다. 개발자용 고급 기능입니다.", Fields: customProviderFields(cfg)},
		{Title: "내 서버의 AI (vLLM/로컬)", Group: settingsGroupFreeAI, VisibleWhen: whenOpencode, Advanced: true, Desc: "직접 운영하는 GPU 서버(vLLM 등)의 주소를 넣으면 opencode가 그 서버를 씁니다. 로컬 모델을 돌릴 때만 필요합니다.", Fields: []settingField{
			{Key: "vllm.primary_url", Label: "1번 서버 주소", Desc: "OpenAI 호환 엔드포인트. 예: http://10.0.0.5:8000/v1. 채우면 opencode가 자동으로 이 서버를 씁니다.", Type: "string", Value: vllmServerAt(cfg, 0).BaseURL},
			{Key: "vllm.primary_model", Label: "1번 서버 모델", Desc: "이 서버가 서빙하는 모델 이름. 모델 참조는 vllm/<모델>.", Type: "string", Value: vllmServerAt(cfg, 0).Model},
			{Key: "vllm.secondary_url", Label: "2번 서버 주소", Desc: "서버를 하나 더 붙일 때만. 참조 id는 vllm-2. 비우면 미사용.", Type: "string", Value: vllmServerAt(cfg, 1).BaseURL},
			{Key: "vllm.secondary_model", Label: "2번 서버 모델", Desc: "2번 서버가 서빙하는 모델. 모델 참조는 vllm-2/<모델>.", Type: "string", Value: vllmServerAt(cfg, 1).Model},
		}},
		{Title: "외부 프로그램 경로", Group: settingsGroupSecurity, Advanced: true, Desc: "ssh 같은 외부 도구가 특별한 위치에 있을 때만 지정합니다. 보통은 비워두면 자동으로 찾습니다.", Fields: []settingField{
			{Key: "tools.ssh", Label: "ssh 경로", Desc: "비우면 자동 탐지. 원격 제어(!ssh)에 사용.", Type: "string", Value: toolPathValue(cfg, "ssh")},
			{Key: "tools.sshpass", Label: "sshpass 경로", Desc: "비밀번호 인증용. 예: C:\\cygwin\\bin\\sshpass.exe. 키 인증만 쓰면 불필요.", Type: "string", Value: toolPathValue(cfg, "sshpass")},
		}},
		{Title: "원격 제어(SSH)", Group: settingsGroupSecurity, Advanced: true, Desc: "봇이 !ssh 명령으로 다른 컴퓨터를 제어하게 허용합니다. 호스트 목록(주소·계정·비밀번호)은 원본 설정편집기의 ssh.hosts에서 관리합니다.", Fields: []settingField{
			{Key: "ssh.enabled", Label: "SSH 원격 제어 허용", Desc: "켜면 !ssh <호스트> <명령>으로 등록된 원격 호스트를 제어합니다.", Type: "bool", Value: cfg.SSHEnabled},
		}},
		{Title: "고급: 실험적 백엔드", Group: settingsGroupNetwork, Advanced: true, Desc: "일반 사용에는 필요 없는 실험적 기능입니다.", Fields: []settingField{
			{Key: "interactive_claude.enabled", Label: "Interactive Claude (실험적)", Desc: "상주 ConPTY 세션 백엔드. 대화별로 \"!interactive on\"으로 켜야 실제 사용됨. Windows 전용. (변경 시 재시작 필요)", Type: "bool", Value: cfg.InteractiveClaude},
		}},
		{Title: "동작 한도", Group: settingsGroupLimits, Advanced: true, Desc: "봇이 얼마나 많이·오래 일할지 제한합니다. 기본값으로 두어도 잘 동작합니다.", Fields: []settingField{
			{Key: "runtime.timeout_minutes", Label: "작업 제한 시간(분)", Desc: "한 작업이 이 시간을 넘기면 취소합니다.", Type: "int", Value: cfg.TimeoutMinutes},
			{Key: "runtime.max_workers", Label: "동시 작업 수", Desc: "한 번에 돌릴 수 있는 작업 개수.", Type: "int", Value: cfg.MaxWorkers},
			{Key: "runtime.rate_limit_per_min", Label: "분당 메시지 제한", Desc: "사용자당 1분에 허용하는 일반 메시지 수.", Type: "int", Value: cfg.RateLimitPerMin},
			{Key: "runtime.conversation_ttl_days", Label: "대화 보관 기간(일)", Desc: "이 기간 동안 활동 없는 대화를 자동 정리. 0이면 정리 안 함.", Type: "int", Value: cfg.ConversationTTLDays},
		}},
		{Title: "보안 / 권한", Group: settingsGroupSecurity, Advanced: true, Desc: "봇에게 얼마나 큰 권한을 줄지 정합니다. 잘 모르면 끈 채로 두는 것이 안전합니다.", Fields: []settingField{
			{Key: "scripts.allow", Label: "명령·스크립트 실행 허용", Desc: "봇이 컴퓨터에서 명령을 실행하도록 허용합니다. 신중히 켜세요.", Type: "bool", Value: cfg.AllowScripts},
			{Key: "screen_control.enabled", Label: "화면 제어 허용", Desc: "봇이 스크린샷·마우스·키보드로 화면을 제어하게 합니다.", Type: "bool", Value: cfg.ScreenControl},
			{Key: "screen_control.keep_awake", Label: "화면 잠금 방지", Desc: "화면 제어 중 화면보호기/잠금을 막습니다.", Type: "bool", Value: cfg.ScreenKeepAwake},
			{Key: "screen_control.elevated", Label: "관리자 권한으로 화면 제어", Desc: "관리자 권한 창까지 제어해야 할 때만 켭니다.", Type: "bool", Value: cfg.ScreenElevated},
		}},
		{Title: "연결 / 네트워크", Group: settingsGroupNetwork, Advanced: true, Desc: "웹 화면·제어 API가 어느 주소에서 열릴지 정합니다. 기본값으로 두면 됩니다. (대부분 변경 시 재시작 필요)", Fields: []settingField{
			{Key: "aglink_chat.enabled", Label: "웹 채팅 화면 사용", Desc: "aglink가 웹 채팅 프론트를 함께 띄웁니다.", Type: "bool", Value: cfg.AglinkChat},
			{Key: "aglink_chat.addr", Label: "웹 채팅 주소", Desc: "예: 127.0.0.1:27271", Type: "string", Value: cfg.AglinkChatAddr},
			{Key: "chat_control.enabled", Label: "제어 API 사용", Desc: "웹 화면이 설정을 읽고 쓰는 통로입니다.", Type: "bool", Value: cfg.ChatControl},
			{Key: "chat_control.addr", Label: "제어 API 주소", Desc: "예: 127.0.0.1:27270", Type: "string", Value: cfg.ChatControlAddr},
			{Key: "web_chat.enabled", Label: "임베디드 웹챗(레거시)", Desc: "구버전 호환용. 지금은 동작하지 않습니다.", Type: "bool", Value: cfg.WebChat},
			{Key: "web_chat.addr", Label: "임베디드 웹챗 주소(레거시)", Desc: "동작하지 않습니다.", Type: "string", Value: cfg.WebChatAddr},
		}},
	}
}

// applySettings mutates cfg with the whitelisted updates map (dotted key →
// value). Unknown keys are ignored (the raw editor covers everything else), so a
// client can never inject an arbitrary field. Values arrive as JSON scalars
// (float64 / bool / string); asInt/asBool/asString coerce them.
func applySettings(cfg *Config, updates map[string]any) error {
	for k, v := range updates {
		// Free-catalog provider creds use dynamic keys (provider.<id>.key /
		// .model) that a fixed switch can't enumerate; route them by prefix.
		if strings.HasPrefix(k, "provider.") {
			applyProviderSetting(cfg, k, asString(v))
			continue
		}
		// UI-defined custom providers use dynamic slot keys
		// (custom_provider.<n>.<field>) a fixed switch can't enumerate.
		if strings.HasPrefix(k, "custom_provider.") {
			applyCustomProviderSetting(cfg, k, asString(v))
			continue
		}
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
		case "opencode.path":
			cfg.OpencodePath = asString(v)
		case "opencode.model":
			cfg.OpencodeModel = asString(v)
		case "opencode.manager_model":
			cfg.OpencodeManagerModel = asString(v)
		case "opencode.config_path":
			cfg.OpencodeConfigPath = asString(v)
		case "interactive_claude.enabled":
			cfg.InteractiveClaude = asBool(v)
		case "vllm.primary_url":
			setVLLMField(cfg, 0, "url", asString(v))
		case "vllm.primary_model":
			setVLLMField(cfg, 0, "model", asString(v))
		case "vllm.secondary_url":
			setVLLMField(cfg, 1, "url", asString(v))
		case "vllm.secondary_model":
			setVLLMField(cfg, 1, "model", asString(v))
		case "tools.ssh":
			setToolPath(cfg, "ssh", asString(v))
		case "tools.sshpass":
			setToolPath(cfg, "sshpass", asString(v))
		case "ssh.enabled":
			cfg.SSHEnabled = asBool(v)
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
	// Trailing all-blank vLLM slots (e.g. a cleared "secondary") shouldn't linger
	// and get rendered into the generated opencode provider config.
	normalizeVLLMServers(cfg)
	// Same for trailing all-blank custom-provider slots.
	normalizeCustomProviders(cfg)
	return nil
}

// applyCustomProviderSetting routes a dynamic "custom_provider.<n>.<field>"
// settings key to the matching slot (1-based n in the UI → 0-based slice index).
// Unknown fields are ignored (the raw editor covers anything the form can't).
func applyCustomProviderSetting(cfg *Config, key, val string) {
	rest := strings.TrimPrefix(key, "custom_provider.")
	dot := strings.Index(rest, ".")
	if dot <= 0 {
		return
	}
	n := atoiOr(rest[:dot], 0)
	if n < 1 {
		return
	}
	field := rest[dot+1:]
	switch field {
	case "id", "base_url", "model", "name":
		setCustomProviderField(cfg, n-1, field, val)
	}
}

// applyProviderSetting routes a dynamic "provider.<id>.<field>" settings key to
// the matching catalog cred. Unknown ids or fields are ignored (the raw editor
// covers anything the form can't), so a client can't inject a provider outside
// the built-in catalog.
func applyProviderSetting(cfg *Config, key, val string) {
	rest := strings.TrimPrefix(key, "provider.")
	dot := strings.LastIndex(rest, ".")
	if dot <= 0 {
		return
	}
	id, field := rest[:dot], rest[dot+1:]
	if _, ok := freeProviderByID(cfg, id); !ok {
		return
	}
	if field != "key" && field != "model" {
		return
	}
	setProviderCredField(cfg, id, field, val)
}

// toolPathValue reads a tool's configured path for display in the settings UI
// ("" when unset → resolved from PATH at runtime).
func toolPathValue(cfg *Config, name string) string {
	if cfg == nil || cfg.ToolPaths == nil {
		return ""
	}
	return cfg.ToolPaths[name]
}

// setToolPath sets (or clears) a tool's path in the registry, allocating the map
// on first use. A blank value clears the entry so it falls back to PATH lookup.
func setToolPath(cfg *Config, name, val string) {
	val = strings.TrimSpace(val)
	if val == "" {
		delete(cfg.ToolPaths, name)
		return
	}
	if cfg.ToolPaths == nil {
		cfg.ToolPaths = map[string]string{}
	}
	cfg.ToolPaths[name] = val
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
