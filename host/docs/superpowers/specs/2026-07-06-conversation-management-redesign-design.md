# 대화 관리 재설계 (Telegram 단일 스트림 / Web 토픽) — Design

**작성일:** 2026-07-06
**상태:** 설계 확정 (사용자 검토 대기)

## 목표 (Goal)

대화 관리를 두 채널로 명확히 분리한다. **텔레그램은 프로젝트를 넘나드는 단일 연속 대화 1개**, **웹은 프로젝트별 주제(토픽) 다수**. 기존의 "프로젝트별 대화 + origin 태깅" 혼합 구조를 폐기하고, 모델을 데이터 구조에 1급으로 새겨 라우팅·웹 노출·크로스채널을 단순·예측가능하게 만든다.

## 배경 / 문제

현재 구조는 모든 대화가 `Project.Conversations` 아래에 있고 텔레그램/웹을 `Conversation.Origin` 필드로만 구분한다. `Manager.Handle`의 LLM 라우팅(`decide()`)과 `!chat` 명령이 두 채널 공용이라 동작이 뒤엉킨다. 사용자 관점 문제:

- 텔레그램은 "하나의 연속 대화"여야 하는데 내부적으로 프로젝트별 대화로 쪼개져 관리가 샌다.
- 웹에서 이전 대화를 골라도 과거 내용이 안 보이고(히스토리 미표시), 텔레그램에서 웹 토픽까지 건드릴 수 있어 경계가 모호하다.

## 확정된 모델

- **텔레그램**: 프로젝트 무관 **단일 연속 대화 1개**. 사용자는 대화 목록/전환 UI를 보지 않는다. "이제 프로젝트 B 하자" 같은 자연어로 **맥락(활성 프로젝트=작업 디렉토리)만** 바꾸고 히스토리는 하나로 계속 이어진다. 컨텍스트 길이 초과 시 기존 auto-continuation 체인으로 내부적으로 이어지되 사용자에겐 여전히 하나.
- **웹**: 프로젝트별 사용자 생성 **토픽 다수**. 각 토픽이 독립 맥락. (토픽 생성 기능은 이미 구현됨.)
- **방향 비대칭**:
  - 웹 → 텔레그램 대화를 **보기+이어가기** 가능 + 웹 토픽 생성/이름변경/전환 가능.
  - 텔레그램 → **웹 토픽을 건드릴 수 없음**. 오직 자기 스트림의 활성 프로젝트만 자연어로 변경.

## 아키텍처

### A. 데이터 모델 (`types.go` / `store.go`)

`StoreData`를 새 스키마로 교체한다:

```go
type StoreData struct {
    Projects      map[string]*Project // 이제 "웹 토픽"만 보유
    Active        ActiveRef           // 웹의 활성 토픽 (project + web topic id)
    ActiveBackend string              // "claude"|"codex"; ""=claude

    // 전역 단일 텔레그램 대화 (프로젝트 무관, 1급 필드)
    TelegramConv          *Conversation // 텔레그램 턴은 항상 이 하나를 resume
    TelegramActiveProject string        // 다음 텔레그램 턴의 작업 디렉토리 = Projects[이 값].Path
}
```

- `Project.Conversations`는 **웹 토픽 전용**. 텔레그램 대화는 여기 들어가지 않는다.
- `Conversation.Origin` 필드는 신규 로직에서 불필요(텔레그램=별도 필드, 프로젝트 대화=전부 웹). 필드는 하위호환/직렬화 안전상 남기되, 분기는 **구조**로 한다(origin 문자열 분기 제거).
- 두 개의 독립 "active": 웹은 `Active`(토픽), 텔레그램은 `TelegramActiveProject`(+암묵적 단일 `TelegramConv`). 더 이상 단일 `Active` 포인터를 두 채널이 다투지 않는다.

새 store 메서드(예시 인터페이스):
- `TelegramConversation() *Conversation` — 없으면 생성(빈 히스토리, 새 SessionID, Backend=현재 기본).
- `SetTelegramActiveProject(name string) error`
- `TelegramActiveProject() string`
- `UpdateTelegramConversation(c *Conversation) error`
- 기존 웹 토픽 메서드(`NewConversation`, `GetConversation`, `UpdateConversation`, `ListProjects`, `GetActive/SetActive`)는 **웹 토픽 전용**으로 의미가 좁혀진다.

### B. origin 기준 라우팅 분리 (`manager.go`)

`Manager.Handle(ctx, chatID, text, origin, sender)`를 origin에서 완전히 가른다:

- **origin == telegram** → `handleTelegram`:
  - LLM 프로젝트/대화 라우팅 제거.
  - **프로젝트 전환 의도 감지**: (i) 등록된 프로젝트명 키워드 매칭(결정적) → 실패/모호 시 (ii) "프로젝트만 고르는" 경량 LLM 호출 폴백. 전환이면 `SetTelegramActiveProject` + 짧은 확인 메시지("📂 이제 <B>에서 진행합니다"). 전환이 아니면 조용히 진행.
  - 항상 `TelegramConv` resume, WorkDir = `Projects[TelegramActiveProject].Path`. `TelegramActiveProject`가 비었거나 미등록이면: 프로젝트 0개면 등록 안내, 1개면 자동 선택, 다수면 어느 프로젝트에서 시작할지 1회 질문.
  - resume backend = `TelegramConv.Backend`(자기 backend). (5f9e2a0 원칙 유지: 대화는 자기 backend로 resume.)
- **origin == web** → `handleWeb`:
  - 웹 UI가 고른 토픽(`Active` = project+id)으로 **명시 라우팅**. LLM 프로젝트 추론 없음.
  - resume backend = 그 토픽의 `Backend`.
  - 활성 토픽이 없으면 안내(웹 UI에서 토픽 선택/생성 유도).

`runWorker`는 그대로 재사용(이미 `client`+`backend` 명시 파라미터). 전환 의도 감지 유틸은 `detectBackendSwitchIntent` 패턴을 참고한 `detectProjectSwitchIntent(text, projectNames) (proj string, ok bool)`로 추가.

### C. 웹 노출 & 클릭 시 히스토리

- 웹 대화 리스트 최상단에 고정된 **"📱 텔레그램 대화" 전역 항목 1개**. 그 아래 프로젝트별 웹 토픽.
- **항목/토픽 클릭 시 그 대화의 `History`(prompt/response 쌍)를 받아 로그 영역을 채우고**, 이후 실시간 프레임을 수신한다.
- 히스토리 조회 프로토콜(두 UI 동일):
  - chat_control(aglink-chat용): 요청 타입 `get_history { target }` 추가. 응답은 turn 배열.
  - 임베디드 웹: `GET /api/history?target=...`.
  - `target`: 텔레그램은 전역 지시자(예 `{"kind":"telegram"}`), 웹 토픽은 `{"kind":"web","project":..,"id":..}`.
- 전송 동작:
  - 텔레그램 항목 클릭 후 전송 → `TelegramConv`를 이어감(origin은 web으로 태깅되지만 대상은 전역 텔레그램 대화 — 크로스채널).
  - 웹 토픽 클릭 후 전송 → 그 토픽을 이어감.
- 웹 리스트 응답(`webConversationsResponse`)에 텔레그램 전역 항목을 별도 필드로 싣는다(예 `telegram: { title, id, active, backend }`), 프로젝트 배열과 분리.

### D. 명령 체계 재배치 (`bot.go`)

- **텔레그램**: `!chat new|list|use` **제거**. 텔레그램은 웹 토픽을 못 건드린다. 프로젝트 전환은 자연어. 유틸 명령(`!project`, `!status`, `!backend`, `!task`, `!screen`, `!help` 등)은 유지.
- **웹 전용**: `!chat new|list|use|rename`(토픽 관리). 웹 `#current-topic` 헤더의 편집 아이콘 → `!chat rename <새 제목>`.
- `handleChat`은 origin==web에서만 동작하고, origin==telegram이면 "텔레그램에서는 대화 주제를 관리하지 않습니다(웹에서 관리)" 안내 후 무시.

### E. 크로스채널 echo

- 기존 origin-gated echo 유지. 텔레그램 대화가 전역이 되었으므로, 웹에서 텔레그램 항목을 보는 중이면 텔레그램 입력도 그 로그에 반영. (Hub 팬아웃은 유지, 대상 식별만 전역 텔레그램 대화 기준으로 조정.)

## 데이터 처리 (마이그레이션 없음)

- **기존 구조/데이터 전부 폐기.** 마이그레이션 함수 없음.
- store 로드 시 새 스키마로 시작. 실행 시 기존 `store.json`이 있으면 **`store.json.bak`로 1회 백업**(안전장치)한 뒤 새 스키마로 초기화한다.
- 사용자 확인 완료: 옛 텔레그램/웹 대화 보존 불필요.

## 에러 처리

- 텔레그램 전환 대상 프로젝트가 미등록 → 전환 무시하고 현재 맥락 유지 + 안내.
- `TelegramActiveProject`가 삭제된 프로젝트를 가리키면 → 재선택 유도(프로젝트 1개면 자동).
- 대화의 backend가 미설치면 → 5f9e2a0과 동일하게 "X로 만들어졌는데 X 미설치" 명확 안내(포크 금지).
- 웹 히스토리 조회 실패 → 로그 영역에 "이전 내용을 불러오지 못했습니다" 표시하되 실시간 대화는 계속 가능.

## 테스트

- store: 새 스키마 직렬화/역직렬화, 기존 store.json → .bak 백업 + 새 초기화, 텔레그램 대화 get-or-create.
- 라우팅: telegram 경로가 항상 `TelegramConv` resume(프로젝트 전환 의도 시 `TelegramActiveProject`만 변경, 대화 불변), web 경로가 명시 토픽 resume. `detectProjectSwitchIntent` 단위테스트.
- 프로토콜: `get_history`(control) / `/api/history`(임베디드) 텔레그램·웹 타깃 각각.
- 명령: telegram에서 `!chat *` 무력화, web에서 `!chat rename` 동작.
- 각 단계 `go build/vet/test` + `node --check web/app.js`(임베디드/aglink-chat 양쪽).

## 범위 / 규모

store 모델·매니저 라우팅·bot 명령·webchat 프로토콜·양쪽 웹 UI를 건드리는 큰 변경. 하나의 구현 plan에서 다중 태스크로 분해한다. 앞서 커밋한 resume-backend 수정(5f9e2a0)의 원칙("각 대화는 자기 backend로 resume")은 새 구조에서도 유지된다.

## YAGNI (의도적으로 제외)

- 옛 대화 마이그레이션/아카이브: 제외(전부 폐기).
- 텔레그램 다중 스트림/서브토픽: 제외(단일 스트림).
- 웹에서 텔레그램 스트림을 토픽으로 분할/이름변경: 제외(보기+이어가기만).
