# 웹 채팅 UI 개선 Design (2026-07-07)

## 목적

teleclaude/aglink-chat 웹 채팅 UI를 다음 7개 축으로 개선한다.

1. **레이아웃** — 사이드바 대화 주제 항목 아이콘/어포던스 개선, 인라인 하단 작성창을 **플로팅 버튼 → 모달 작성창** 구조로 변경.
2. **버전 표시** — 웹 화면에 현재 실행 중인 teleclaude 버전 표시.
3. **업데이트 버튼** — 웹에서 클릭으로 기존 `!update` 무중단 자기교체 배포 트리거.
4. **aglink 연동 상태** — aglink-chat 연동(연결 여부/주소) 확인 화면.
5. **config.yaml 편집** — 서버 설정 YAML을 웹에서 조회/편집, 저장 시 핫리로드 반영.
6. **연결 관리** — 웹이 붙는 WebSocket 주소/포트 등 연결 설정 확인/변경.
7. **대화 관리** — 대화 목록 이름변경/삭제/작업폴더 변경 UX 다듬기.

## 아키텍처 배경

- **Teleclaude** = 봇 본체. [webchat.go](../../../webchat.go)가 봇 내부(store/hub/`handleCommand`/`handleUpdate`)에 직접 연결된 **내장 웹 서버**(기본 `127.0.0.1:1717`)를 제공. 또한 **control API**([chatcontrol.go](../../../chatcontrol.go), `127.0.0.1:17170`)를 노출.
- **aglink-chat** = 얇은 **릴레이 프런트엔드**. [server.go](../../../../aglink-chat/server.go)가 Teleclaude control API로 붙어(controlClient) 브라우저 메시지를 전달·프레임을 브로드캐스트. 그래서 두 `web/app.js`는 자매 파일이다.
- 관리 기능(버전/업데이트/config/연결/aglink 상태)은 본질적으로 **Teleclaude 서버 자원**이다.

## 설계 결정 (사용자 확정)

- **A. 관리 기능 범위 = Teleclaude 내장 UI 전용.** 관리 패널은 webchat.go의 새 `/api/*` 엔드포인트로 Teleclaude 내장 UI에만 노출. `app.js`는 **동일하게 유지**하되 `GET /api/capabilities` **capability 감지**로 aglink-chat에서는 관리 UI가 자동으로 숨겨진다. aglink-chat은 공유 UI(레이아웃·대화관리)+ 릴레이 보완만 상속.
- **B. config.yaml 편집 = 원문 YAML 편집 + 비밀값 마스킹.** GET은 파일 원문을 주되 비밀값을 마스킹, PUT은 마스킹 복원 → 검증 → 고정 경로에만 기록 → fsnotify 핫리로드.
- **C. 작성창 = 플로팅 ✎ 버튼 → 모달 작성창** (텍스트 + 📎 첨부 + 전송).
- **D. 관리 화면 배치 = 기능별 개별 패널** (통합 모달 1개가 아니라 버전/업데이트는 헤더, config·연결은 각각 별도 패널).

## 컴포넌트 & 데이터 흐름

### 공유 UI (두 저장소, `index.html`/`app.js`/`style.css` 동일 자매 파일)

- **Task 1 레이아웃:** 대화 주제 행 아이콘/어포던스 개선. 하단 인라인 작성창 제거 → 플로팅 ✎ 버튼이 모달 작성창을 연다(전송 시 닫힘, Esc/배경 클릭 닫기). 로그 영역이 세로 공간을 회수. 관리 마크업은 공유 HTML에 들어가되 capability 없으면 숨김.
- **Task 7 대화 관리:** 각 주제 행에 **⋯ 메뉴**(이름변경/작업폴더 변경/삭제)로 기존 단일 ⚙ 대체. 신규 `web_delete` 메시지 → `bot.webDelete` → 기존 `store.DeleteWebConv`. aglink-chat 백엔드에 누락된 릴레이(`web_new`/`web_setdir`/`web_rename`/`web_delete`) + `/api/history` 보완.
- **capability 감지:** `app.js`가 로드시 `GET /api/capabilities` 1회 호출. Teleclaude 내장 → `{admin:true, version:…}`; aglink-chat → 404 → 관리 UI 숨김. `app.js`를 자매 파일로 완전 동일하게 유지.

### 관리 기능 (Teleclaude 내장 UI 전용, 개별 패널)

- **Task 2 버전:** 패키지 변수 `buildVersion`/`buildTime`, `handleUpdate`의 `go build`에 `-ldflags`로 주입(+ `git rev-parse --short HEAD` 커밋). `GET /api/version`으로 헤더 배지 표시. 일반 dev 빌드는 `"dev"`.
- **Task 3 업데이트 버튼:** 헤더 **⟳ 업데이트** → 확인 다이얼로그 → 기존 인증 명령 경로로 리터럴 `!update` 전송. **신규 백엔드 없음.** 기존 가드(토큰 인증, 작업 중 업데이트 금지, `cmdMu` 직렬화)를 그대로 상속.
- **Task 5 config.yaml 패널:** `GET /api/config` = 파일 원문 + 비밀값 마스킹(`bot_token`/`oauth_token`/`web_chat.token`/`chat_control.token` 값을 정확값 치환으로 `●●●` 센티넬화, 주석/포맷 보존). `PUT /api/config` = 센티넬 복원 → `unmarshalConfigYAML` 검증 → **`cfgPath`에만** 기록 → fsnotify 핫리로드. 파싱/검증 실패 시 거부, 원본 불변.
- **Task 4·6 연결 패널:** `GET /api/status` = web_chat addr, chat_control addr/enabled, **aglink-chat control 클라이언트 현재 연결 여부**(`chatControlServer`의 라이브 카운터). addr/port 변경은 config.yaml 편집(Task 5) + "재시작 필요" 안내(리스너는 시작 시 바인딩).

## 인터페이스 (신규/변경)

### Teleclaude Go

- `webServer` 필드 추가: `cfgPath string`, `holder *ConfigHolder`. (main.go:390 생성부에서 주입)
- `chatControlServer`: 연결 카운터 `connCount atomic.Int64` (register/unregister시 증감).
- `var buildVersion = "dev"`, `var buildTime = ""` (main 패키지). `handleUpdate` 빌드에 `-ldflags "-X main.buildVersion=<git-short> -X main.buildTime=<RFC3339>"` 추가(git 실패시 생략, dev 유지).
- 신규 HTTP 핸들러(모두 `authOK` 통과 필수):
  - `GET /api/capabilities` → `{"admin":true,"version":"<v>","buildTime":"<t>"}`.
  - `GET /api/version` → `{"version","buildTime","backend"}`.
  - `GET /api/status` → `{"webChatAddr","chatControlEnabled","chatControlAddr","aglinkConnected":bool,"aglinkClients":int}`.
  - `GET /api/config` → `text/plain` 원문(비밀 마스킹).
  - `PUT /api/config` → 본문 원문 텍스트. 성공 204, 실패 400 + 에러 메시지.
- `bot.webDelete(chatID, id)` → `store.DeleteWebConv(id)` 래퍼(+ 활성 대화면 활성 해제).
- config 마스킹 헬퍼: `maskConfigSecrets(raw []byte, cfg *Config) []byte`, `restoreConfigSecrets(edited []byte, cfg *Config) []byte`. 정확값 문자열 치환(빈 값은 마스킹 안 함).

### 제어 API (chatcontrol.go / aglink-chat)

- `controlIn`에 `web_delete` 타입 추가(Teleclaude 이미 `web_new`/`web_setdir`/`web_rename`/`get_history` 지원).
- aglink-chat `controlIn`(control.go)에 누락 필드 `Target *json.RawMessage`(또는 동형 구조)/`ID`/`Title` 추가.
- aglink-chat `server.go` handleWS: `send` 외 `web_new`/`web_setdir`/`web_rename`/`web_delete` 브라우저 메시지를 control API로 릴레이. `/api/history` 핸들러 추가(`get_history` 요청).

### 웹 (app.js — 두 저장소 동일)

- `bootstrapCapabilities()` — `/api/capabilities` 호출 결과로 `admin` 플래그 설정, 헤더 관리 컨트롤/패널 표시 토글.
- 작성창: 플로팅 버튼 + 모달. `openComposer()`/`closeComposer()`. 전송 로직은 기존 `sendText`/업로드 재사용.
- 대화 행 ⋯ 메뉴: 이름변경(`web_rename`)/폴더(`web_setdir`)/삭제(`web_delete`, confirm).
- 관리 패널(admin일 때만): 버전 배지, 업데이트 버튼(confirm→`!update`), config 편집 패널(GET/PUT `/api/config`), 연결 패널(GET `/api/status`).

## 보안 (사용자 제약 반영)

- 모든 신규 엔드포인트는 `authOK`(origin 루프백 + 상수시간 토큰) 통과 필수 — 기존 관례 재사용.
- config 기록은 **고정 `cfgPath`에만**. 클라이언트가 경로를 절대 지정하지 못한다(임의 파일 쓰기 금지).
- 비밀값은 서버를 떠나지 않는다(마스킹). 저장시 사용자가 센티넬을 그대로 두면 기존 비밀 유지, 값을 바꾸면 그 값 사용.
- `!update`는 기존 명령 경로를 그대로 타서 모든 가드 유지. 웹 버튼도 확인 다이얼로그 1단계 추가.
- `PUT /api/config`는 `unmarshalConfigYAML` 검증 통과 전에는 파일을 건드리지 않는다(원자적 실패).

## 검증

- 각 task: Teleclaude에서 `go build ./... && go vet ./... && go test ./...` 그린. Windows gofmt CRLF 오탐 → `tr -d '\r' | gofmt -d`.
- 두 `web/app.js` `node --check` 통과. `index.html`/`style.css`/`app.js`를 동일 자매 파일로 수렴.
- 신규 Go 로직(마스킹/복원, 버전 주입, status/capabilities, web_delete)은 단위 테스트 추가.
- 완료 후 두 저장소 로컬 커밋(푸시 금지) → `!update`로 배포.

## 범위 밖 (YAGNI)

- addr/port **런타임** 재바인딩(리스너 재시작 없이) — config 편집 + 재시작 안내로 대체.
- aglink-chat 자체의 관리 UI(자기 addr/token/control-addr 편집) — 이번 범위 아님.
- config 구조화 폼 편집(원문 YAML로 대체).
- 비-Windows `!update`(기존과 동일 제약).
