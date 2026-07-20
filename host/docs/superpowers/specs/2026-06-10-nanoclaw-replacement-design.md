# Teleclaude — nanoclaw 대체 설계

**날짜:** 2026-06-10  
**목표:** Docker 없이 Go 단일 바이너리로 nanoclaw 핵심 기능 포괄  
**배포 환경:** Windows PC (개발) + NanoPi ARM64 Linux (운영) 크로스플랫폼

---

## 1. 범위

| 기능 | 포함 여부 |
|------|-----------|
| 표준 cron expression (`0 9 * * 1-5`) | ✅ |
| Script pre-check (wakeAgent 패턴) | ✅ |
| Task pause / resume / update / cancel | ✅ |
| 실제 Claude 작업 실행 (isTask 버그 수정) | ✅ |
| Telegram 첨부파일 수신 (사진/파일) | ✅ |
| 대화 히스토리 날짜별 저장 | ✅ |
| Linux 크로스플랫폼 지원 | ✅ |
| `!task` 명령 통합 인터페이스 | ✅ |
| 멀티채널 (WhatsApp 등) | ❌ 다음 단계 |
| 컨테이너 격리 | ❌ 의도적 미포함 |

---

## 2. 핵심 타입

### Task (Reminder + CronJob 통합)

```go
type Task struct {
    ID        string    `json:"id"`
    ChatID    int64     `json:"chatId"`
    Prompt    string    `json:"prompt"`             // 알림 텍스트 or Claude 프롬프트
    Script    string    `json:"script,omitempty"`   // bash pre-check 내용 (빈 문자열 = 없음)
    CronExpr  string    `json:"cronExpr,omitempty"` // "0 9 * * 1-5" — 반복
    FireAt    time.Time `json:"fireAt,omitempty"`   // 일회성 실행 시각
    Status    string    `json:"status"`             // "pending"|"paused"|"cancelled"
    IsTask    bool      `json:"isTask"`             // true=Claude 실행, false=알림
    Label     string    `json:"label"`
    CreatedAt time.Time `json:"createdAt"`
    LastFired time.Time `json:"lastFired,omitempty"`
}
```

- `CronExpr != ""` → 반복 작업 (`robfig/cron/v3` 파싱)
- `CronExpr == ""` → 일회성 (FireAt 기준)
- `Status == "paused"` → tick에서 skip
- `IsTask == true` → Claude Worker로 dispatch

---

## 3. Script Pre-check 흐름

```
cron tick 도달
  └─ Script 필드가 비어있지 않음?
       ├─ YES: bash -c <script> 실행 (30초 timeout)
       │         stdout → JSON 파싱: { "wakeAgent": bool, "data": {...} }
       │         wakeAgent == false → 이번 turn skip, 다음 fire 예약
       │         wakeAgent == true  → data를 prompt에 append 후 Claude 실행
       └─ NO: 바로 Claude 실행 (isTask=true) 또는 알림 전송 (isTask=false)
```

**script 예시 (장 개장일 체크):**
```bash
#!/bin/bash
DOW=$(date +%u)  # 1=Mon ... 7=Sun
if [ "$DOW" -ge 6 ]; then
  echo '{"wakeAgent": false}'
else
  echo '{"wakeAgent": true}'
fi
```

script stdout이 유효한 JSON이 아니면 `wakeAgent: true`로 fallback (안전하게 실행).

---

## 4. 스케줄러 아키텍처

```
Scheduler
  ├─ tasks []Task          (인메모리, JSON 영속)
  ├─ cronRunner *cron.Cron (robfig/cron/v3)
  ├─ send func(int64,str)  (단순 알림)
  └─ dispatch func(int64,str) (Claude 작업)

시작 시:
  1. tasks.json 로드
  2. 기존 schedule.json 마이그레이션 (Reminder→Task, CronJob→Task)
  3. 각 Task를 cronRunner에 등록

Task 등록:
  - CronExpr 있음 → cron.AddFunc(expr, handler)
  - CronExpr 없음 → 별도 goroutine에서 FireAt까지 sleep 후 실행
```

**cron Entry와 Task의 연결:**
`cronEntries map[string]cron.EntryID` — Task.ID → cron EntryID 매핑  
pause/resume/update 시 해당 Entry를 제거 후 재등록.

---

## 5. 봇 명령어

### !task (새 통합 명령)

```
!task add <cron식> <프롬프트>              반복 알림
!task add <cron식> task <프롬프트>         반복 Claude 작업
!task add <cron식> --script <인라인스크립트> task <프롬프트>
!task once <시각> <메시지>                 일회성 (예: "2026-06-11 09:00")
!task list [pending|paused|all]
!task pause <id>
!task resume <id>
!task cancel <id>
!task update <id> --cron <식>
!task update <id> --prompt <텍스트>
!task update <id> --script <스크립트>
```

### 기존 명령 호환 유지

```
!remind <시간> <메시지>    → 내부적으로 일회성 Task 생성
!cron add <식> [task] <내용> → 내부적으로 반복 Task 생성
!cron list / remove <id>   → !task list / cancel로 위임
```

---

## 6. Telegram 첨부파일 처리

**처리 대상:** Photo, Document, Video, Audio, Voice

```
update.Message.Photo/Document 감지
  → Telegram API로 파일 다운로드 → ~/.teleclaude/attachments/<timestamp>.<ext>
  → 기존 캡션 텍스트 + "\n[첨부파일: /path/to/file]" 조합
  → dispatchText(chatID, combinedText)
```

Claude Worker가 파일 경로를 받아 Read 도구로 직접 읽을 수 있음.  
임시 파일은 Worker 완료 후 정리.

---

## 7. 대화 히스토리

**저장 경로:** `~/.teleclaude/history/<project>/<YYYY-MM-DD>.md`

**형식:**
```markdown
## 14:32 — <대화 제목>

**요청:** <user prompt>

**응답:** <Claude response (최대 500자, 이후 생략)>

---
```

**접근 명령:**
```
!history [project] [YYYY-MM-DD]   — 특정 날짜 히스토리 조회
!history list                     — 저장된 날짜 목록
```

히스토리는 Worker 완료 시 `manager.go`에서 자동 기록.

---

## 8. Linux 크로스플랫폼

### 빌드 태그 분리

```
platform_windows.go  // go:build windows
platform_linux.go    // go:build linux
platform_darwin.go   // go:build darwin (선택)
```

### 구현 내용

| 기능 | Windows | Linux |
|------|---------|-------|
| `killTree(pid)` | `taskkill /F /T /PID` | `syscall.Kill(-pgid, SIGKILL)` |
| `killPreviousInstance()` | tasklist 스캔 | pgrep + kill |
| `waitForProcessExit(pid)` | tasklist 폴링 | `/proc/<pid>/status` 폴링 |
| exe 확장자 | `.exe` | 없음 |
| `findClaude()` 경로 | AppData/Roaming/npm | ~/.local/bin, /usr/local/bin 등 |

### Linux claude 경로 탐색 순서

```
1. CLAUDE_PATH 환경변수 / config.txt
2. $PATH (exec.LookPath)
3. ~/.local/bin/claude
4. /usr/local/bin/claude
5. ~/.npm-global/bin/claude
6. ~/.nvm/versions/node/*/bin/claude (glob)
7. /usr/bin/claude
```

---

## 9. 파일 변경 목록

| 파일 | 변경 유형 | 설명 |
|------|-----------|------|
| `scheduler.go` | 전면 재작성 | Task 통합, robfig/cron/v3, script precheck |
| `types.go` | 수정 | Task 타입 추가 |
| `bot.go` | 수정 | !task 명령 + 첨부파일 처리 |
| `manager.go` | 수정 | 히스토리 저장 호출 |
| `config.go` | 수정 | findClaude Linux 경로 |
| `history.go` | 신규 | 히스토리 저장/조회 |
| `platform_windows.go` | 신규 | Windows 프로세스 관리 |
| `platform_linux.go` | 신규 | Linux 프로세스 관리 |
| `main.go` | 수정 | platform 함수 호출로 교체 |
| `runner.go` | 수정 | killTree → platform 함수 호출 |
| `go.mod` | 수정 | robfig/cron/v3 추가 |

---

## 10. 데이터 마이그레이션

시작 시 `schedule.json`이 존재하면 자동 변환:

```
Reminder → Task{CronExpr:"", FireAt:r.FireAt, IsTask:false, Status:"pending"}
CronJob  → Task{CronExpr: durationToCron(c.Interval), IsTask:c.IsTask, Status:"pending"}
```

`durationToCron` 변환:
- 1h → `0 * * * *`
- 24h → `0 0 * * *`
- 7d → `0 0 * * 0`
- 기타 → `@every Xm` (robfig 확장 문법)

마이그레이션 완료 후 `schedule.json` → `schedule.json.bak` 이름 변경.

---

## 11. 의존성 추가

```
github.com/robfig/cron/v3 v3.0.1
```

표준 5필드 cron (`분 시 일 월 요일`) + `@every`, `@daily` 등 확장 문법 지원.

---

## 12. 검증 기준

- [ ] `!task add "30 7 * * 1-5" task 주식 스크리너 실행` 등록 → 평일 7:30에 Claude 작업 실행
- [ ] `!task pause <id>` → 해당 작업 skip
- [ ] `!task resume <id>` → 재개
- [ ] `!task update <id> --cron "0 8 * * *"` → 다음 실행 시각 변경
- [ ] Script pre-check: `wakeAgent: false` 반환 시 Claude 미호출 확인
- [ ] 사진 전송 → Claude에게 파일 경로 포함 프롬프트 전달
- [ ] `!history` 명령으로 과거 대화 조회
- [ ] Linux에서 빌드 및 실행 (`GOOS=linux GOARCH=arm64 go build`)
- [ ] 기존 schedule.json 자동 마이그레이션
