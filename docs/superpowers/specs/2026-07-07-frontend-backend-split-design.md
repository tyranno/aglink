# 프론트/백엔드 분리 아키텍처 Design (2026-07-07)

## 배경 & 결정 (원래 방향, 이제야 명문화)

teleclaude는 [2026-07-02 web-chat-transport](../plans/2026-07-02-web-chat-transport.md) 계획부터 자체 임베디드 웹서버([webchat.go](../../../webchat.go), `127.0.0.1:1717`)를 "메인 트랜스포트"로 갖고 있고, aglink-chat은 [2026-07-06](../plans/) chat_control API([chatcontrol.go](../../../chatcontrol.go):19 주석)에 붙는 "선택적 별도 클라이언트"로만 추가됐다.

**결정: 이 관계를 뒤집는다.**
- **teleclaude = 백엔드 전용** — 봇 로직/스토어/러너/스케줄러 + control API. 웹 UI 운영 주체가 아니다.
- **aglink-chat = 주(primary) 프론트엔드** — 항상 켜져 있는 웹서버. control API로 teleclaude에 붙어 채팅과 관리 기능 전부를 제공.
- **aglink-screen / aglink-web = 플러그인**(MCP) — 기존과 동일.
- aglink-* 계열을 **확장 가능한 프론트/플러그인 생태계**로 가져간다.

## 목표 아키텍처

```
┌─────────────── teleclaude (backend) ───────────────┐
│  bot · store · runners · scheduler                  │
│  control API (chatcontrol.go, ws://127.0.0.1:17170) │◄──┐ control (frames + admin RPC)
│  [webchat.go 임베디드 웹서버 — 최종엔 비활성/보조]   │   │
│  aglink-chat 자식 프로세스 감시(spawn/restart)       │   │
└─────────────────────────────────────────────────────┘   │
                                                            │
┌─────────────── aglink-chat (primary frontend) ─────┐     │
│  browser 웹서버 (http://127.0.0.1:1717 최종)        │─────┘
│  /ws · /api/conversations · /api/history · /api/*   │
│  관리자 패널(version/aux/config)도 control 릴레이    │
└─────────────────────────────────────────────────────┘
        ▲ browser
```

## 마이그레이션 순서 (단계별, 각 단계 독립 검증)

- **Phase 0 (완료):** 관리 기능이 teleclaude 임베디드 UI 전용. aglink-chat은 채팅만 릴레이.
- **Phase 1 (이번 작업, steps 2–3):** teleclaude가 aglink-chat.exe serve를 **자식 프로세스로 자동 기동**, **다른 포트(127.0.0.1:1718)에서 임베디드(1717)와 병행**. control API에 **관리 RPC 릴레이(get_version/get_aux/get_config/set_config)** 추가로 aglink-chat이 **관리자 패널까지 완전 동작**. `!update`가 aglink-chat.exe도 재빌드·배포. **임베디드 1717은 절대 안 건드림.** 1718에서 대화목록/히스토리/전송/관리자 패널 전부 검증.
- **Phase 2 (별도, 명시적 확인 필수):** aglink-chat을 1717로 승격, 임베디드 webchat.go 서빙을 config 플래그로 비활성(코드는 유지). 이 전환은 사용자 확인 후에만.

## Phase 1 설계 결정 (사용자 확정)

- **A. 자동 기동 = teleclaude 자식 프로세스.** config `aglink_chat.enabled`로 teleclaude가 `aglink-chat.exe serve`를 spawn·감시(종료 시 backoff 재기동, teleclaude 종료 시 kill). 설정 한 곳(config.yaml), 생명주기 teleclaude와 동일. **시작 시점 바인딩**(web_chat/chat_control과 동일) — enabled 토글 반영은 재시작 필요.
- **B. 관리 기능 = control API로 전부 릴레이.** `get_version`/`get_aux`/`get_config`/`set_config` 제어 메시지 추가. teleclaude는 webchat.go가 쓰는 것과 **동일한 공통 함수**로 payload 생성(코드 일관). aglink-chat은 이를 프록시하는 `/api/version`·`/api/aux`·`/api/config`(GET/PUT)·`/api/capabilities`(admin:true) 제공.
- **C. !update = aglink-chat 포함.** `pluginNames`에 `aglink-chat` 추가 → aglink-screen/web처럼 재빌드 후 teleclaude 옆(srcDir)에 배포. 빌드 전 실행 중 aglink-chat.exe를 kill(락 해제), teleclaude 재기동 시 감시자가 새 바이너리로 재spawn.

## 컴포넌트 & 인터페이스

### teleclaude (Go)

- **config**: `Config`에 `AglinkChat*` 필드, `config_yaml.go`에 `aglink_chat` 섹션(`enabled`, `addr`(기본 `127.0.0.1:1718`), `binary_path`, `token`). `binary_path` 미설정 시 `srcDir/aglink-chat.exe` → 없으면 `../aglink-chat/aglink-chat.exe`.
- **자식 감시자** `aglinkchat_supervisor.go`: `startAglinkChat(ctx, cfg, chatControlAddr, chatControlToken, browserToken)` — spawn/Wait/backoff-respawn, shutdown(ctx), `updating` atomic로 !update 중 재spawn 억제.
- **control 릴레이** `chatcontrol.go` `handleInbound`에 `get_version`/`get_aux`/`get_config`/`set_config` 케이스. `chatControlServer`에 `cfgPath` 주입.
- **공통 payload 추출**: `versionPayload(backend string)`(free func), `readMaskedConfig(cfgPath, *Config)`, `writeValidatedConfig(cfgPath, *Config, body)` — webchat.go와 chatcontrol.go가 공유. `buildAuxFeatures`는 이미 공용.
- **!update**: `pluginNames`에 `aglink-chat` 추가. `updatePlugins`에서 aglink-chat 빌드 전 `killByImageName("aglink-chat.exe")`.
- **main.go**: chat_control 활성 시(+aglink_chat.enabled) `go startAglinkChat(...)`. `chatControlServer.cfgPath = cfgPath`.

### aglink-chat (Go)

- **control.go** `controlIn`에 `Body string` 필드(set_config 본문). request() 재사용.
- **server.go**: `/api/version`·`/api/aux` → `control.request(get_version/get_aux)` 프록시. `/api/config` GET → `get_config`, PUT → `set_config`(Body). `/api/capabilities` → admin:true + get_version 병합. `/api/status` → 자기 addr 로컬 응답.
- 공유 `web/app.js`는 그대로(동일 자매) — `/api/capabilities`가 이제 aglink-chat에서 admin:true 반환 → 관리 UI 표시.

### 관리 RPC 페이로드 계약

- `get_version` reply: `versionPayload` (version/commit/buildTime/commitCount/latest*/updateAvailable) + backend.
- `get_aux` reply: `{features:[auxFeature...]}`.
- `get_config` reply: `{config:"<masked yaml>"}`.
- `set_config` req: `Body=<edited yaml>`; reply `{ok:bool, error?:string}`.

## 보안

- aglink-chat의 관리 엔드포인트는 aglink-chat 브라우저 토큰(authOK)으로 보호. control API 릴레이는 chat_control 토큰으로 보호(기존).
- config 기록은 여전히 **teleclaude가 자기 고정 cfgPath에만** 수행(aglink-chat은 본문만 전달, 경로 지정 불가). 비밀 마스킹/복원도 teleclaude 쪽에서.
- 자식 spawn 인자에 control 토큰이 커맨드라인으로 노출됨(로컬 전용, 기존 chat_control.token 파일도 로컬 가독). 허용 범위로 간주.

## 롤백

- **Phase 1 롤백**: `aglink_chat.enabled: false` → 자식 미기동, 임베디드 1717 무영향. 빌드 문제 시 `pluginNames`에서 `aglink-chat` 제거. control 릴레이 케이스는 미사용이면 무해(남겨둬도 됨).
- **Phase 2는 이번 범위 아님** — 임베디드 비활성/1717 승격은 별도 확인 후.

## 이번 범위 밖 (다음/cutover)

- 임베디드 webchat.go 서빙 비활성화 + aglink-chat 1717 승격 (Phase 2, 확인 필요).
- aglink-chat `/api/upload` 릴레이(현재 501) — 첨부 업로드 파리티는 cutover 때.
- enabled 토글 hot-reload spawn(현재 재시작 필요).
