# 제어권(Control Ownership) 관리 — 설계 및 MCP 규약

> 대상 독자: **teleclaude 개발자** (호출측) 및 **aglink-screen 개발자** (구현측).
> 이 문서는 여러 teleclaude 대화가 동시에 같은 화면을 조작하지 않도록 하는
> 제어권 조율의 **계약(contract)** 을 정의한다. 코드에서는 `Design Ref:
> docs/control-ownership.md §N` 형식으로 참조한다.
>
> 상태: **구현 완료** (2026-07-14). aglink-screen 쪽 구현·테스트 끝(§8 참고).
> 아래 §3~§5가 구현 스펙, §4가 teleclaude(호출측)가 지켜야 할 MCP 계약이다.

---

## 1. 배경 — 왜 필요한가

teleclaude는 **대화(worker)마다 별도의 aglink-screen 프로세스**를 띄운다. 프로세스
트리는 다음과 같다:

```
teleclaude(감독)
 ├─ claude worker #A ─ aglink-screen.exe  (프로세스 A, stdio MCP)
 ├─ claude worker #B ─ aglink-screen.exe  (프로세스 B, stdio MCP)
 └─ ...
```

프로세스 A와 B는 **서로 다른 프로세스**이면서 **같은 물리 화면 하나**를 조작한다.
따라서 프로세스 내부 뮤텍스로는 조율이 불가능하고, 두 대화가 동시에 마우스/키보드를
합성하면 클릭이 뒤섞여 엉뚱한 동작을 한다.

> 참고: 화면을 조작하려면 반드시 **인터랙티브 세션(대화형 데스크톱)** 안에 있어야
> 한다. 즉 모든 화면-구동 aglink-screen 프로세스는 같은 Windows 세션에 존재한다.
> 이 전제가 §3의 잠금 설계(세션-로컬 뮤텍스)를 정당화한다.

## 2. 정책 결정

| 항목 | 결정 |
|---|---|
| 경합 시 동작 | **홀딩 없음. fail-fast** — 다른 세션이 제어 중이면 즉시 "busy"를 MCP 응답으로 알리고, 대기/재시도 판단은 **호출측(teleclaude/worker)** 이 한다. |
| 조율 프리미티브 | **리스 파일 + 세션-로컬 네임드 뮤텍스** (크로스-프로세스). |
| 해제 | **자동** — 시간 기반 TTL + 소유자 PID 생존 검사. 명시적 release 불필요(크래시 안전). |
| 적용 범위 | 합성 입력을 내보내는 제어 op만. **읽기 전용 툴은 제어권과 무관**(§4.3). |

## 3. 메커니즘 (aglink-screen 구현 스펙)

### 3.1 리스 파일

경로: `~/.teleclaude/screen-control.lock` (`dataDir()` 하위, `presets.json`과 동일 위치)

JSON 스키마:

```json
{
  "owner_pid": 12345,
  "owner_label": "tg:CHAT/turn:7",   // 선택. AGLINK_OWNER_LABEL(§5)에서. 없으면 ""
  "since": 1752480000000,            // 이 소유권이 시작된 시각 (UnixMillis)
  "last_activity": 1752480002300     // 마지막 제어 op 시각 (UnixMillis) — TTL 판정 기준
}
```

### 3.2 원자성 — 세션-로컬 네임드 뮤텍스

이름: **`Local\aglink-screen-control`**

- 리스 파일의 **읽기 → 판정 → 쓰기(RMW)** 임계 구역만 아주 짧게 잡는다(마이크로초).
  이것은 "제어 중 대기"가 아니라 파일 갱신의 원자성만 보장한다.
- `Global\`이 아니라 `Local\`을 쓰는 이유: 화면-구동 프로세스는 모두 같은 인터랙티브
  세션에 있으므로 세션-로컬 네임스페이스로 충분하고, `Global\`이 요구하는
  `SeCreateGlobalPrivilege` 없이 동작한다.
- `WaitForSingleObject`는 짧은 타임아웃(예: 2s)으로 잡아 가드 소유 프로세스가 죽어도
  무한 대기하지 않는다. 반환이 `WAIT_ABANDONED`(이전 소유자가 가드를 쥔 채 사망)면
  우리가 소유한 것으로 간주하고 진행한다.

### 3.3 획득 규칙 — `acquireControlLease()`

```
1. 뮤텍스 잠금 (bounded)
2. 리스 파일 읽기 (없으면 free로 간주)
3. 다음 중 하나면 → 내 것으로 갱신(owner_pid=나, last_activity=now, since는 신규 소유면 now)
     · 리스가 비어 있음(free)
     · owner_pid == 내 pid            (내 세션의 연속 op → 갱신)
     · now - last_activity > TTL      (상대가 idle → 만료)
     · owner_pid 프로세스가 죽음       (OpenProcess 실패 / 종료됨)
   → 뮤텍스 해제 → nil 반환 (진행 ✅)
4. 그 외(다른 pid가 살아있고 신선함) → 뮤텍스 해제 → busy 에러 반환 (❌ §4.1)
```

- **TTL**: 기본 **8000ms** (`controlNoticeGap`과 동일 — "세션이 끝났다"고 보는 유휴
  기준과 일치). `AGLINK_CONTROL_LEASE_TTL_MS`로 재정의(§5).
- **PID 생존 검사**: `OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION)` +
  `GetExitCodeProcess == STILL_ACTIVE`. PID 재사용 오탐은 TTL이 backstop.
- **갱신**: 활동 중인 세션은 매 제어 op가 `last_activity`를 갱신 → 그 동안 다른
  세션은 계속 busy를 받는다. 세션이 8s 멈추면 리스가 만료되어 다른 세션이 회수.

### 3.4 획득 지점 — `beginSyntheticInput` 최상단

`beginSyntheticInput`은 모든 합성-입력 함수(click/type/key/drag/scroll/invoke/…)가
입력 직전 반드시 통과하는 단일 게이트다. 리스 획득을 **여기 최상단, 사람 양보(user
yield)보다 먼저** 둔다:

```
func beginSyntheticInput() error {
    if err := acquireControlLease(); err != nil {   // ← 신규: 다른 자동화가 쥐고 있으면 여기서 fail-fast
        return err
    }
    installUserInputWatcher()
    // ... 기존 user-yield (사람에게 양보) ...
    ensureControlNotice()
    return nil
}
```

순서 근거: 다른 **자동화**가 화면을 쥐고 있으면 사람 양보를 기다릴 이유도 없이 즉시
반환해야 한다. 리스 실패 시 `ensureControlNotice`에 도달하지 않으므로 시작/완료 토스트도
뜨지 않고 제어 카운터도 오르지 않는다(= 제어하지 않았음).

`run_sequence`는 첫 스텝의 `beginSyntheticInput`에서 리스를 잡고, 실패하면 step 0에서
busy로 중단(부분 적용 없음). 이후 스텝은 `last_activity`를 갱신.

## 4. MCP 계약 (teleclaude/worker가 참조)

### 4.1 제어 시도 시 busy 응답 (reactive)

어떤 제어 툴(`click`, `double_click`, `triple_click`, `drag`, `move`, `type`, `key`,
`scroll`, `invoke`, `set_value`, `click_control`, `focus_window`, `run_sequence`, …)이든,
다른 세션이 리스를 소유 중이면 **MCP 에러 결과**(`isError: true`)를 반환한다.

메시지에 안정적인 마커 `SCREEN_BUSY:` 를 포함하여 호출측이 프로그램적으로도 인식할 수
있게 한다. 각 툴 핸들러가 자기 컨텍스트를 앞에 붙이므로(예: `move failed: `) 마커는
문자열 **어딘가**에 등장한다 — 접두사가 아니라 **부분 문자열**로 매칭할 것:

```
move failed: SCREEN_BUSY: another teleclaude session is controlling the screen
(owner_pid=9364, label="SESSION-A", active 0.6s ago); refused to avoid colliding
with its input — wait a few seconds and retry, or ensure only one session drives
the screen at a time
```

MCP 응답은 `isError: true`. 마커 `SCREEN_BUSY:` 는 **불변 계약**, 라벨/pid/경과시간은 참고용.
- **호출측 권장 처리**:
  - worker LLM: 에러를 읽고 몇 초 백오프 후 재시도하거나, 사용자에게 "다른 대화가
    화면을 사용 중"이라고 안내.
  - teleclaude 감독: 필요하면 워커 프롬프트/시스템 메시지로 이 규약을 알려 재시도
    정책을 유도.

### 4.2 사전 확인 툴 — `control_status` (proactive, 읽기 전용)

화면을 구동하기 **전에** "지금 누가 제어 중인지"를 확인할 수 있는 읽기 전용 MCP 툴을
제공한다(리스를 잡지 않음, 부작용 없음).

- 입력: 없음.
- 출력(텍스트):
  - free: `control: free`
  - 내가 소유: `control: held by me (pid=<나>, since 4.1s ago)`
  - 남이 소유: `control: held by another (owner_pid=12345, label="...", last_activity 0.7s ago, ttl 8000ms)`

teleclaude는 이 툴로 디스패치 전에 폴링하거나, busy 에러 후 상태를 재확인할 수 있다.

### 4.3 읽기 전용 툴은 제어권 무관

`screenshot`, `capture_window`, `capture_region`, `snapshot`, `element_at`,
`get_value`, `get_cursor_position`, `get_window_rect`, `list_windows`,
`win_controls`, `wait_for_window`, `wait_for_control`, `preset_list`,
`control_status` 는 합성 입력을 내보내지 않으므로 **리스와 무관하게 항상 동작**한다.
즉 다른 세션이 제어 중이어도 화면을 "보는" 것은 언제나 가능하다.

## 5. 환경변수 (teleclaude가 설정)

| 변수 | 의미 | 기본 |
|---|---|---|
| `AGLINK_OWNER_LABEL` | 이 프로세스(대화)의 사람이 읽을 소유자 라벨. busy 메시지와 `control_status`에 노출. 예: `tg:<chat_id>/turn:<n>`. | `""`(라벨 없음, pid만) |
| `AGLINK_NO_CONTROL_LEASE` | `1`이면 리스 전체 비활성(단일 세션 배포/디버그). | (미설정=활성) |
| `AGLINK_CONTROL_LEASE_TTL_MS` | 리스 만료 TTL(ms). | `8000` |

> **teleclaude 통합 권장**: 워커 기동 시 `AGLINK_OWNER_LABEL`에 대화 식별자를 넘기면
> busy 메시지/상태가 사람이 읽기 좋게 나온다(어느 대화가 잡고 있는지). 없어도 pid로
> 동작한다.

## 6. 동작 예시 (타임라인)

```
t=0.0  A: click        → 리스 free → A가 획득(pid_A, since=0.0) → 클릭 실행
t=0.3  A: type         → owner=A → 갱신(last=0.3) → 실행
t=0.5  B: click        → owner=A, 신선(0.2s 전) → SCREEN_BUSY 반환 ❌ (B의 LLM: 백오프)
t=1.0  A: run_sequence → owner=A → 갱신 → 실행
t=1.2  B: control_status→ "held by another (owner_pid=A, last 0.2s ago)"  (읽기 OK)
...    A: (더 이상 제어 op 없음, 대화 종료/유휴)
t=9.5  B: click        → now-last(=1.0) > 8000ms → 만료 → B가 획득 → 실행 ✅
```

크래시 시나리오:
```
A가 리스 보유 중 프로세스 강제 종료 → B: click →
  PID 생존 검사에서 A 죽음 확인(또는 8s TTL 만료) → B가 즉시 회수 → 실행 ✅
```

## 7. 미해결 / 향후 결정

- **`cmd` fast-path (해결됨)**: 텔레그램 `!screen`의 `aglink-screen cmd click <preset>`은
  `mouseClick`을 호출하고, `mouseClick`은 `beginSyntheticInput`을 통과하므로 **이미 같은
  리스에 참여한다** — 별도 작업 불필요. 즉 MCP 세션이 화면을 쥐고 있는 동안 `!screen`
  즉시 클릭을 하면 `cmd`의 JSON 출력 `error` 필드에 `SCREEN_BUSY:`가 담긴다(§9). 그 외
  `cmd`(list/shot/region/preset)은 읽기 전용/비입력이라 무관.
- **명시적 handoff**: 스냅한 인계가 필요하면 `release_control` 툴을 추가해 세션 종료
  시 TTL을 기다리지 않고 즉시 놓아주게 할 수 있다(현재는 TTL 자동 만료로 충분하다고
  판단, 보류).
- **owner_label 표준화**: teleclaude 쪽 대화 식별자 포맷을 확정하면 여기 §5 예시를
  갱신한다.

## 8. 구현 체크리스트 (aglink-screen)

- [x] `screen_lease_windows.go` — 리스 파일 R/W, `Local\` 뮤텍스, PID 생존, 획득 규칙.
- [x] `beginSyntheticInput` 최상단에 `acquireControlLease()` 호출(§3.4).
- [x] `control_status` MCP 툴 등록(§4.2).
- [x] 환경변수 처리(§5).
- [x] 단위 테스트 `screen_lease_windows_test.go` — free/mine/stale/dead-pid/busy 판정
      (주입식 clock·pid-liveness·temp 파일) + `control_status` 문구.
- [x] 통합 테스트(수동) — 빌드 바이너리 2개 동시 실행: A가 `move`로 리스 획득 →
      B의 `move`가 `SCREEN_BUSY`(isError) 수신, B의 `control_status`가 "held by another",
      A 종료 후 죽은 pid를 생존검사로 즉시 회수 확인.

> 비-Windows 스텁은 생략했다: 리스는 `beginSyntheticInput`(Windows 전용)과 `mcpscreen.go`
> (Windows 전용)에서만 참조되므로 비-Windows 코드에는 심볼 참조가 없다. 다른 OS는 기존
> 스텁이 화면제어 자체를 막는다.

## 9. `cmd` fast-path와 제어권

`aglink-screen cmd click <preset>`은 `mouseClick`→`beginSyntheticInput`→`acquireControlLease`를
그대로 거치므로 MCP 경로와 동일하게 리스를 존중한다(별도 구현 없음). 짧게 뜨는 `cmd`
프로세스가 리스를 잠깐 쥐었다 종료하면, 다음 획득자는 죽은 pid를 생존검사로 즉시 회수한다.

- 다른 세션이 제어 중일 때 `cmd click`을 하면: fast-path의 stdout JSON이
  `{"error":"click failed: SCREEN_BUSY: ..."}` 형태가 된다. teleclaude의 `!screen`
  핸들러는 이 `error`를 사용자에게 그대로 전달하면 된다.
- 정책상 사용자의 수동 `!screen` 클릭에 우선권을 주고 싶다면(진행 중인 자동화보다 앞서게),
  그건 별도 정책 결정이다 — 현재는 "먼저 쥔 세션 우선"으로 일관되게 동작한다.
