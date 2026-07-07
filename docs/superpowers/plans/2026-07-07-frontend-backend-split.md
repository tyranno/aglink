# 프론트/백엔드 분리 — Phase 1 (병행 검증) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development 또는 executing-plans. 체크박스(`- [ ]`) 추적.

**Goal:** teleclaude가 aglink-chat.exe serve를 자식 프로세스로 자동 기동해 `127.0.0.1:1718`에서 임베디드(1717)와 **병행** 운영하고, control API 관리 RPC 릴레이로 aglink-chat이 채팅+관리자 패널까지 완전 동작함을 검증한다. **임베디드 1717은 절대 비활성/제거하지 않는다.**

**Architecture:** [specs/2026-07-07-frontend-backend-split-design.md](../specs/2026-07-07-frontend-backend-split-design.md). teleclaude=백엔드, aglink-chat=주 프론트(이번엔 병행). Phase 2(1717 승격/임베디드 비활성)는 별도 확인.

**Tech Stack:** Go 1.25, coder/websocket, go:embed, Vanilla JS (두 web/app.js 동일 자매).

## Global Constraints

- **절대 금지: webchat.go(임베디드 1717) 비활성/제거 없이** — 이 대화가 1717로 오간다. Phase 1은 순수 additive.
- 각 단계 `go build ./... && go vet ./... && go test ./...` 그린(Teleclaude, aglink-chat). Windows gofmt CRLF 오탐 → `tr -d '\r' | gofmt -l`.
- 신규 aglink-chat 관리 엔드포인트는 aglink-chat authOK로 보호. config 기록은 teleclaude 고정 cfgPath에만. 비밀 마스킹/복원은 teleclaude 쪽.
- 두 web/app.js `node --check` + 바이트 동일. push 금지, 커밋 trailer 유지.

---

## Task 1: config `aglink_chat` 섹션

**Files:** `config.go`(Config 필드), `config_yaml.go`(yamlConfig + 매핑), `config_yaml_test.go`(라운드트립).

**Interfaces (Produces):** `Config.AglinkChat bool`, `AglinkChatAddr string`, `AglinkChatBinaryPath string`, `AglinkChatToken string`. YAML `aglink_chat.{enabled,addr,binary_path,token}`. 기본 addr `127.0.0.1:1718`.

- [ ] Config 구조체 필드 추가.
- [ ] yamlConfig에 `AglinkChat` 구조체 + yamlToConfig/configToYAML 매핑(빈 addr → 1718 기본).
- [ ] 기존 config_yaml_test 라운드트립에 aglink_chat 포함 확인(또는 신규 테스트).
- [ ] `go test -run Config .` 그린. 커밋.

## Task 2: 공통 payload 함수 추출 (webchat.go 리팩터)

**Files:** `webchat.go`(versionPayload를 free func으로, config 헬퍼 추출).

**Interfaces (Produces):**
- `func versionPayload(backend string) map[string]any` (free func; os.Executable+git). webchat.go handleVersion/handleCapabilities가 호출.
- `func readMaskedConfig(cfgPath string, cfg *Config) ([]byte, error)`.
- `func writeValidatedConfig(cfgPath string, cfg *Config, body []byte) error` (검증 실패 시 error 반환; 성공 시 파일 기록).

- [ ] `s.versionPayload()` → free `versionPayload(backend)` 이동. handleVersion/handleCapabilities에서 backend 넘겨 호출.
- [ ] handleConfig의 GET/PUT 로직을 `readMaskedConfig`/`writeValidatedConfig`로 추출, handleConfig는 이를 호출.
- [ ] 기존 테스트(TestHandleVersion/Capabilities/Config) 그린 유지. 커밋.

## Task 3: control API 관리 RPC 릴레이

**Files:** `chatcontrol.go`(controlIn 케이스 + cfgPath 필드), `main.go`(cfgPath 주입), `chatcontrol_test.go`.

**Interfaces (Produces):** `controlIn.Type` ∈ 추가 `get_version|get_aux|get_config|set_config`. `chatControlServer.cfgPath string`. `controlIn.Body string`(set_config 본문).
- get_version reply Data = `versionPayload(bot.manager.Backend())`.
- get_aux reply Data = `{features: buildAuxFeatures(connCount, cfg.ChatControl, cfg.ChatControlAddr)}`.
- get_config reply Data = `{config: string(readMaskedConfig(cfgPath, cfg))}`.
- set_config: `writeValidatedConfig(cfgPath, cfg, []byte(m.Body))`; reply `{ok:bool, error?:string}`.

- [ ] `controlIn`에 `Body` 필드, 타입 주석 갱신.
- [ ] `chatControlServer`에 `cfgPath`, main.go에서 주입.
- [ ] `handleInbound` switch에 4개 케이스(reply 헬퍼로 ch.push(controlOut{Kind:"reply",...})).
- [ ] 테스트: get_version/get_aux reply가 유효 JSON. set_config 잘못된 YAML → ok:false. 커밋.

## Task 4: aglink-chat 자식 감시자 (teleclaude)

**Files:** `aglinkchat_supervisor.go`(신규), `aglinkchat_supervisor_test.go`, `main.go`(기동), `pluginupdate.go`(kill 전처리), `platform_windows.go`/`platform_linux.go`(resolve 헬퍼 필요 시).

**Interfaces (Produces):**
- `var aglinkChatUpdating atomic.Bool` — !update 중 재spawn 억제.
- `func resolveAglinkChatBinary(cfg *Config, selfExe string) string` — cfg.AglinkChatBinaryPath > srcDir/aglink-chat.exe > ../aglink-chat/aglink-chat.exe > "".
- `func startAglinkChat(ctx context.Context, binPath, addr, controlAddr, controlToken, browserToken string)` — spawn `binPath serve --addr <addr> --control-addr <controlAddr> --control-token <controlToken> --token <browserToken>`; cmd.Wait 후 ctx 안 끝났고 !updating 아니면 backoff 재spawn.

- [ ] `resolveAglinkChatBinary` + 단위 테스트(경로 우선순위).
- [ ] `startAglinkChat` 감시 루프(context 취소로 종료, backoff, updating 체크).
- [ ] main.go: chat_control 활성 + cfg.AglinkChat 시 browserToken=`loadOrCreateToken(cfg.AglinkChatToken,"aglink_chat.token")`, addr 기본 1718, `go startAglinkChat(...)`. 로그에 `http://<addr>/?token=<tok>`.
- [ ] teleclaude 종료 시 자식 종료(context + cmd.Process kill). 커밋.

## Task 5: !update가 aglink-chat 빌드·배포

**Files:** `pluginupdate.go`(pluginNames + kill 전처리), `pluginupdate_test.go`.

- [ ] `pluginNames = []string{"aglink-screen", "aglink-web", "aglink-chat"}`.
- [ ] updatePlugins에서 `name=="aglink-chat"`일 때 빌드 전 `aglinkChatUpdating.Store(true)` + `killByImageName("aglink-chat"+exeSuffix)` (락 해제), 빌드 후 그대로(새 teleclaude가 재spawn). (aglink-web의 restartAglinkWebDaemon 패턴과 유사.)
- [ ] 기존 pluginupdate_test 그린 유지/보완. 커밋.

## Task 6: aglink-chat 관리 엔드포인트 (프록시)

**Files:** `../aglink-chat/control.go`(controlIn Body), `../aglink-chat/server.go`(핸들러 + 라우팅).

**Interfaces (Produces):** aglink-chat `/api/version`·`/api/aux`·`/api/config`(GET/PUT)·`/api/capabilities`·`/api/status`.
- version/aux: `control.request(controlIn{Type:"get_version"|"get_aux"})` → 바디 그대로 프록시.
- config GET: `get_config` → `{config}`에서 텍스트 추출해 text/plain. PUT: 본문을 `set_config`(Body)로, reply `ok`면 204, 아니면 400+error.
- capabilities: get_version 결과 + `admin:true` 병합.
- status: 로컬 `{webChatAddr: s.addr}`.

- [ ] control.go controlIn에 `Body string`.
- [ ] server.go 핸들러 5개 + mux 등록. authOK 필수.
- [ ] `go build ./... && go vet ./...` 그린. 커밋(aglink-chat).

## Task 7: 통합 검증 (1718 병행)

- [ ] 두 repo build/vet/test 그린, 두 app.js node --check + 바이트 동일(app.js는 이번엔 변경 없음 — 확인만).
- [ ] Teleclaude !update 배포(임베디드 1717 정상 유지 확인).
- [ ] config.yaml에 `aglink_chat.enabled: true` 설정 후 재시작(!update) → teleclaude가 aglink-chat.exe를 srcDir에 빌드·spawn, 1718 리슨 확인(netstat).
- [ ] 1718에서 검증(aglink_chat.token 사용):
  - `/api/conversations`(대화목록), `/api/history`(히스토리), `/ws` 전송.
  - 관리자 패널: `/api/capabilities`(admin:true), `/api/version`, `/api/aux`, `/api/config` GET/PUT.
- [ ] **1717 임베디드 무영향 재확인**(현재 대화 유지). Phase 2 전환은 별도 확인 요청.

---

## Self-Review (스펙 대비)
- 자동 기동(자식) → Task 4. ✓
- 관리 릴레이 → Task 2(공통) + Task 3(control) + Task 6(aglink-chat). ✓
- !update aglink-chat → Task 5. ✓
- config → Task 1. ✓
- 1718 병행 검증 → Task 7. ✓
- 1717 불가침 → Global Constraints + Task 7. ✓
