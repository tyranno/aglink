# Web Chat Transport 설계

**작성일:** 2026-07-02
**상태:** 승인됨 (구현 계획 대기)

## 1. 목표

teleclaude를 텔레그램 봇으로만 조작하던 것을, **로컬 웹 페이지 채팅**으로도 동일 기능을
쓸 수 있게 한다. 별도 봇 없이 브라우저에서 메시지·이미지를 주고받으며, 텔레그램과
**같은 대화/프로젝트 상태를 공유**한다. 텔레그램 채널은 그대로 유지하고 웹은 두 번째
transport로 추가한다.

### 확정된 요구사항 (브레인스토밍 결과)

| 항목 | 결정 |
|---|---|
| 접속 범위 | **로컬호스트 전용** (127.0.0.1) |
| 텔레그램 공존 | **둘 다 유지** (텔레그램 + 웹 동시, 상태 공유) |
| 기능 범위 | **전체 패리티** (모든 `!`명령 + 채팅 + 이미지 + 첨부) |
| 접근법 | **① 기존 Sender 추상화 재사용 + 브로드캐스트 Hub** |
| 응답 미러링 | **(a) 양쪽 동기화** — assistant 응답은 텔레그램·웹 모두에 전달 |
| 전송 프로토콜 | **WebSocket** (순수 Go 라이브러리, 확장성) |

## 2. 아키텍처

```
[텔레그램 getUpdates]                      [브라우저(127.0.0.1)]
        │ 입력                                   │ 입력(WS {type:"send"} / POST /api/upload)
        ▼                                        ▼
   Bot.handleCommand / dispatchText  ◄───────────┘   (같은 ownerChatID로 주입)
        │
        ▼
   Manager.Handle(ctx, chatID, text, Hub)     ← 기존 라우팅/워커 로직 그대로
        │ 출력(Send / Typing / SendPhoto / 진행이벤트)
        ▼
      ┌──────── Hub (팬아웃) ────────┐
      ▼                              ▼
 telegramChannel                 webChannel(들)   ← 브라우저 연결당 1개
 (tgbotapi)                      (WebSocket)
```

### 왜 이 접근인가

- `Manager.Handle(ctx, chatID, text, s MessageSender)`는 이미 **출력이 인터페이스로 추상화**되어
  있다(`relay.go`의 `MessageSender`). Bot은 그 한 구현체일 뿐이다.
- `store.SetActive(project, conv)`는 **전역 포인터**(단일 사용자)라, active 프로젝트/대화는
  채널과 무관하게 이미 공유된다. 따라서 상태 공유는 거의 자동이다.
- 모든 `!`명령이 `handleCommand(chatID, text)`의 텍스트 in/out이므로, transport만 추가하면
  전체 패리티가 사실상 공짜로 따라온다.
- `chatID`는 실질적으로 "전달 주소 + 스케줄러 소유자 + 레이트리밋 키"일 뿐이므로, 웹은
  ownerChatID를 공유해 하나의 논리적 사용자로 동작한다.

## 3. 컴포넌트

각 컴포넌트는 단일 책임을 가지며 독립적으로 테스트 가능하다.

### 3.1 Hub (`hub.go`, 신규)

chatID별 채널 레지스트리 + 출력 팬아웃. 기존 `MessageSender`를 확장한 인터페이스를 구현한다.

```go
// ChannelSender is the full output surface a transport channel must provide.
// Extends MessageSender (Send/Typing) with photo delivery so images fan out too.
type ChannelSender interface {
    Send(chatID int64, text string) error
    SendPhoto(chatID int64, png []byte, caption string) error
    Typing(chatID int64)
}

type Hub struct {
    mu       sync.RWMutex
    channels map[int64][]ChannelSender // keyed by chatID
}

func NewHub() *Hub
func (h *Hub) Register(chatID int64, ch ChannelSender)
func (h *Hub) Unregister(chatID int64, ch ChannelSender)
// Send/SendPhoto/Typing: fan out to every channel registered for chatID.
// A per-channel error is logged and isolated — it never blocks other channels.
func (h *Hub) Send(chatID int64, text string) error
func (h *Hub) SendPhoto(chatID int64, png []byte, caption string) error
func (h *Hub) Typing(chatID int64)
```

- `telegramChannel` (`hub.go` 또는 `bot.go`): 현재 `Bot.Send/SendPhoto/Typing`의 tgbotapi 본문을
  이 타입으로 이동. 4096자 청킹은 여기(telegram 전용)에 유지.
- `webChannel` (`webchat.go`): WebSocket 연결당 1개. 출력을 JSON 프레임으로 직렬화해 전송.
  느린 클라이언트 대비 채널별 버퍼드 send goroutine; 버퍼 초과 시 그 연결만 종료.

### 3.2 웹 서버 (`webchat.go`, 신규)

`net/http` 서버, **127.0.0.1 바인딩만**. 라우트:

| 라우트 | 용도 |
|---|---|
| `GET /` | 임베드 SPA (index.html) |
| `GET /static/*` | 임베드 정적 자산 (app.js, style.css) |
| `GET /ws` | WebSocket 업그레이드 → webChannel 생성·Hub 등록 |
| `POST /api/upload` | multipart 파일 업로드 → `ingestAttachment` |

- WebSocket 라이브러리: `github.com/coder/websocket` (순수 Go, CGO 없음).
- 토큰 + Origin 검증(§5). 서버 기동 실패(포트 사용 중 등)는 로그만 남기고 웹만 비활성화 —
  텔레그램·봇 본체는 계속 동작.

### 3.3 입력 주입

- WS 프레임 `{type:"send", text}`:
  - `text`가 `!`로 시작 → `bot.handleCommand(ownerChatID, text)`
  - 아니면 → `bot.dispatchText(ownerChatID, text)`
  - 기존 큐/레이트리밋/dispatch를 그대로 탄다.
- `POST /api/upload`: 파일 저장 후 `ingestAttachment(ownerChatID, localPath, caption)` 호출.
  - **작은 리팩터**: 현재 `handleAttachment(chatID, *tgbotapi.Message)`에서 다운로드 이후의
    "파일→프롬프트→dispatch" 코어를 transport-중립 `ingestAttachment(chatID, path, caption)`로
    분리하고, 텔레그램 경로도 이를 호출하도록 변경.

### 3.4 임베드 UI (`web/`, 신규, `go:embed`)

- `index.html`, `app.js`, `style.css` — vanilla JS, 빌드 단계 없음.
- 기능: 메시지 목록(사용자 버블 + assistant 텍스트 + 인라인 이미지), 입력창(`!명령` 포함),
  파일 업로드 버튼, 프로젝트/대화 선택, 연결상태 표시, 백오프 자동 재연결.
- 사용자가 웹에서 보낸 메시지는 즉시 자기 버블로 리스트에 표시(일반 채팅 UX).

### 3.5 설정 (`types.go`, `config_yaml.go`)

`web_control`(=aglink-web 브라우저 제어)과 **혼동 금지**. 새 섹션 이름은 `web_chat`.

```yaml
web_chat:
  enabled: false          # 기본 off
  addr: "127.0.0.1:1717"  # 로컬 전용
  token: ""               # 비면 최초 실행 시 자동 생성 후 파일 저장
  owner_chat_id: 0        # 0이면 allowed_user_ids[0] 사용
```

Config 구조체 필드: `WebChat bool`, `WebChatAddr string`, `WebChatToken string`,
`WebChatOwnerChatID int64`. YAML 왕복(`config_yaml.go`) + 라운드트립 테스트에 반영.

## 4. 데이터 흐름

1. **시작** (`main.go`): `Hub` 생성 → `telegramChannel`을 ownerChatID로 등록 → Bot의 출력 경로를
   Hub로 연결 → `web_chat.enabled`면 웹서버 goroutine 기동.
2. **웹 접속**: 브라우저가 `/` 로드(토큰 필요) → `GET /ws?token=…` → 토큰+Origin 검증 →
   `webChannel` 생성·Hub 등록 → 초기 상태(현재 프로젝트/대화 + 최근 히스토리) 푸시.
3. **웹 전송**: WS `{type:"send", text}` → §3.3 분기 → 기존 dispatch.
4. **응답 팬아웃**: Manager 출력(Send/Typing/SendPhoto/진행이벤트)이 Hub → 텔레그램 + 모든 웹 탭에
   동시 전달. 이미지는 base64 프레임으로 인라인 렌더.
5. **파일 업로드**: `POST /api/upload` → 임시저장 → `ingestAttachment`.

### 사용자 입력 표시 규칙 (v1)

- 웹에서 친 메시지: 웹 UI에 자기 버블로 표시(로컬).
- assistant 응답(텍스트/이미지/진행): 텔레그램·웹 모두에 브로드캐스트.
- **웹에서 친 메시지를 텔레그램에 봇이 대리 게시하지 않음**(봇이 사용자 행세하는 어색함 회피).
- **텔레그램에서 친 메시지를 웹 히스토리에 미러하지 않음**(v1 범위 밖; 응답 동기화만).

## 5. 인증 (로컬 전용)

- **127.0.0.1 바인딩만** (0.0.0.0 금지).
- **토큰**: 최초 실행 시 생성(암호학적 난수) → `~/.teleclaude/web_chat.token`(0600) 저장 +
  로그에 접속 URL `http://127.0.0.1:1717/?token=…` 출력. 브라우저는 localStorage에 보관하고
  WS/POST에 `?token=` 또는 헤더로 첨부. 서버는 상수시간 비교(`subtle.ConstantTimeCompare`).
- **Origin 검증**: WS/POST의 Origin 헤더가 `http://127.0.0.1:<port>` 또는 `http://localhost:<port>`가
  아니면 거부 → 다른 사이트 JS의 로컬 WS 탈취(CSWSH)·DNS 리바인딩 차단.
- **owner 매핑**: 웹 동작은 ownerChatID 권한으로 처리. 로컬 + 토큰이 신뢰경계.

## 6. 에러 처리

- **WS 끊김**: 해당 webChannel만 Hub에서 해제, 서버·텔레그램 정상. 브라우저 백오프 재연결 후
  상태 재수신.
- **채널 격리**: 한 채널 전송 실패가 다른 채널을 막지 않음(Hub가 채널별 에러 격리·로그).
- **백프레셔**: webChannel별 버퍼드 send; 초과 시 그 연결만 종료.
- **포트 사용 중**: 명확한 로그 + 웹만 비활성, 봇 전체는 죽지 않음.
- **긴 응답**: 4096 청킹은 telegramChannel에만 유지, 웹은 전체를 한 버블로 표시.
- **인증 실패**: 401, 정보 노출 없음.

## 7. 테스트 전략

- `hub_test.go`: 다채널 팬아웃, 채널별 에러 격리, register/unregister, chatID 라우팅.
- `webchat_test.go`(httptest): 토큰 검증(정상/불량), Origin 검증(localhost 허용·타 origin 거부),
  `/api/send` 분기(`!`→handleCommand, 일반→dispatchText), `/api/upload` → ingestAttachment.
- WS 왕복: httptest 서버 + `coder/websocket` 클라이언트로 "전송→브로드캐스트 프레임" 검증.
- 회귀: `MessageSender`/`ChannelSender` 리팩터 후 기존 manager·bot 테스트 그린 유지
  (`telegramChannel`이 `ChannelSender` 충족). `config_yaml_test.go`에 `web_chat` 라운드트립 추가.
- 크로스컴파일(Windows/Linux) + `go vet` 그린.

## 8. 범위 경계 (v1, YAGNI)

**포함:** 로컬 웹 채팅, 텔레그램 공존, 전체 `!`명령 패리티(텍스트), 이미지 인라인, 파일 업로드,
응답 양방향 동기화, 토큰+Origin 인증.

**제외:** 멀티유저 웹 인증 · TLS · 프론트 빌드/프레임워크 · 명령별 전용 UI 폼(버튼은 후속) ·
텔레그램-origin 사용자 메시지의 웹 미러 · 외부/LAN 노출.

## 9. 파일 요약

| 파일 | 변경 |
|---|---|
| `hub.go` | 신규 — Hub, ChannelSender, telegramChannel |
| `webchat.go` | 신규 — http 서버, WS, 라우트, 인증, webChannel |
| `web/index.html`, `web/app.js`, `web/style.css` | 신규 — 임베드 UI |
| `bot.go` | 변경 — 출력 경로 Hub 경유, telegramChannel 분리, `ingestAttachment` 추출 |
| `main.go` | 변경 — Hub 생성·등록, 웹서버 기동 |
| `types.go`, `config_yaml.go` | 변경 — `web_chat` 설정 |
| `relay.go` | 변경 — `ChannelSender` 정의(또는 `MessageSender` 확장) |
| `hub_test.go`, `webchat_test.go`, `config_yaml_test.go` | 신규/변경 — 테스트 |
