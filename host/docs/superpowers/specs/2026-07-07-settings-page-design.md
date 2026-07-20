# 설정 페이지 (/setting) Design (2026-07-07)

## 목적

현재 팝업(모달)으로 뜨는 config 편집 / aglink 연동 상태를, 메인에서 연결되는 **`/setting` 탭 페이지**로 이전한다. config 탭은 (1) 개별 설정을 **의미 설명과 함께 UI로** 조정하는 구조화 폼과 (2) 설정파일을 직접 고치는 **raw 편집**을 둘 다 제공한다.

## 결정 (사용자 확정)

- **A. `/setting` = SPA 내 클라이언트 라우팅.** aglink-chat Go 서버가 `/setting`에도 `index.html` 서빙(SPA fallback). `app.js`가 `location.pathname`으로 채팅 뷰 ↔ 설정 뷰 전환. URL은 `/setting`. `history.pushState`/`popstate` 사용. 기존 `#config-overlay`/`#conn-overlay` 팝업 제거.
- **B. 구조화 폼 데이터 = 서버 구조화 API.** teleclaude가 필드 메타데이터(라벨/설명/타입/값)의 단일 소스. control API로 릴레이.
- **C. 범위 = 안전·주요 부분집합 + raw로 나머지.** 비밀값/허용목록은 구조화 폼 제외, raw 탭에서만.

## 아키텍처

```
브라우저 ──/setting── aglink-chat(SPA) ──/api/settings── control API ── teleclaude
                                         ──/api/config(raw, 기존)──┘
```

### teleclaude (백엔드)

- `settings.go` (신규):
  - `settingField{Key,Label,Desc,Type,Value,Options?}`, `settingSection{Title,Fields}`.
  - `buildSettings(cfg *Config) []settingSection` — 큐레이트된 필드의 현재값 + 메타.
  - `applySettings(cfg *Config, updates map[string]any) error` — **화이트리스트 키만** cfg에 반영(타입 강제).
- control RPC (`chatcontrol.go`): `get_settings`(reply=sections), `set_settings`(Body=JSON updates map → 현재 Config 복사본에 applySettings → `marshalConfigYAML` → `unmarshalConfigYAML` 검증 → `cfgPath` 기록 → fsnotify 핫리로드). reply `{ok,error?}`.
- **비밀값 보존**: set은 현재 `holder.Get()`(실 비밀 포함) 복사본에서 시작하므로 재직렬화해도 비밀·비노출 필드 유지. (구조화 폼은 비밀을 애초에 안 보냄.)
- **주의(trade-off)**: 구조화 저장은 `marshalConfigYAML`로 파일을 재생성 → 주석/수동 포맷 손실. 주석 보존이 필요하면 raw 탭 사용(파일 텍스트 직접 편집, 기존 `/api/config`).

### 노출 필드 (안전 부분집합, 설명 포함)

| 섹션 | key | type |
|---|---|---|
| 모델 | models.manager, models.worker | string |
| 모델 | models.manager_always | bool |
| 백엔드 | backend.default | select(claude,codex) |
| 백엔드 | backend.codex_model, backend.codex_manager_model | string |
| 런타임 | runtime.timeout_minutes, max_workers, rate_limit_per_min, conversation_ttl_days | int |
| 실행/보안 | scripts.allow, screen_control.enabled, screen_control.keep_awake, screen_control.elevated | bool |
| 연결 | web_chat.enabled, chat_control.enabled, aglink_chat.enabled | bool |
| 연결 | web_chat.addr, chat_control.addr, aglink_chat.addr | string |

비밀값(bot_token/oauth_token/토큰), 허용자 목록 → **raw 탭에서만**.

### aglink-chat (프론트 서버)

- `server.go`: `/setting`(및 필요 시 하위 경로) → `handleIndex` 서빙(SPA fallback). `/api/settings` GET→`get_settings`, PUT→`set_settings`(body) 프록시.
- `control.go`: 기존 `Body` 필드 재사용.

### 프론트 (app.js, index.html, style.css)

- **라우터**: `renderRoute()` — `location.pathname==="/setting"`이면 `#settings-view` 표시(+`#shell` 숨김), 아니면 채팅. `navigate(path)`=`history.pushState`+renderRoute. `popstate`→renderRoute.
- **설정 뷰**(`#settings-view`, 기본 hidden): 상단 바(← 채팅, 제목, 탭 [설정][연결/aglink]), 본문.
  - **설정 탭**: 구조화 폼(`GET /api/settings`로 렌더; 섹션→필드→라벨+설명+입력) + 저장(`PUT /api/settings` 변경분만) + 접이식 raw 편집(기존 `/api/config`).
  - **연결/aglink 탭**: 기존 `openConnections` 내용(버전/이 웹서버/aux) 이식.
- 헤더 ⚙ → `navigate('/setting')`(설정 탭), 🔌 → `navigate('/setting')`(연결 탭). 팝업 제거.
- 관리 UI는 `bootstrapCapabilities`의 admin 게이트 유지(비admin이면 설정 진입 숨김).

## 보안/검증

- 모든 신규 엔드포인트 토큰 인증. set_settings는 **화이트리스트 키만** 반영(임의 필드 주입 불가), 기록 전 `unmarshalConfigYAML` 검증, teleclaude 고정 `cfgPath`에만 기록.
- 비밀값은 구조화 API로 노출/편집 안 함.

## 검증

- teleclaude `buildSettings`/`applySettings` 단위 테스트(스키마 반환, 화이트리스트/타입 강제, 미지 키 무시, set→get 라운드트립, 검증 실패 거부).
- aglink-chat build/vet. `node --check` app.js. 두 저장소 커밋 후 `!update` 배포.

## 범위 밖 (YAGNI)

- 비밀값/허용목록 구조화 폼(→ raw).
- 구조화 저장의 주석 보존(→ raw).
- 필드별 실시간 유효성(저장 시 서버 검증으로 충분).
