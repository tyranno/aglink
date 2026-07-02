# teleclaude

Telegram 봇 1개로 **여러 프로젝트·여러 대화**를 **자연어로** 골라가며,
로컬에 설치된 `claude` CLI로 작업을 수행하고 결과를 받아보는 **Go 단일 바이너리** 에이전트.

- **Manager(경량 모델)** 가 프로젝트·대화를 자연어로 라우팅 (애매하면 되묻기)
- **Worker(claude `--resume`)** 가 해당 디렉터리에서 실제 작업 (대화별 맥락 분리)
- **단일 바이너리** — Node/Docker/tmux 불필요, `claude` CLI만 있으면 됨
- **크로스플랫폼** — Windows (x86-64) / Linux (ARM64, x86-64, Raspberry Pi 등)

> ⚠️ Worker는 `--dangerously-skip-permissions`로 실행되어 **로컬 파일·명령 실행이 가능**합니다.
> 반드시 **본인 Telegram user ID만** `ALLOWED_USER_IDS`에 등록하고, 봇 토큰을 안전하게 보관하세요.

---

## 빠른 시작

### Windows

```powershell
go build -o teleclaude.exe .
.\teleclaude.exe run        # 처음 실행 시 설정 마법사 자동 시작
```

상시화 (로그온 시 자동 시작):
```powershell
.\scripts\install-windows-task.ps1
```

hot-swap 업데이트(`!update`)를 쓰려면 `launcher.ps1`로 실행:
```powershell
.\launcher.ps1
```

### Linux / ARM64 (NanoPi, Raspberry Pi, 서버 등)

**Windows에서 크로스컴파일 후 배포:**
```powershell
# ARM64 빌드 + SSH 배포 + 서비스 재시작
.\scripts\deploy-linux.sh nanopi

# x86-64 서버
$env:GOARCH="amd64"; .\scripts\deploy-linux.sh user@192.168.1.100
```

**대상 머신에서 서비스 설치:**
```bash
# 바이너리를 ~/teleclaude 로 복사한 후:
bash scripts/install-linux-service.sh

# 또는 수동으로 설정 파일 먼저 작성:
cp config.example.txt ~/.teleclaude/config.txt
nano ~/.teleclaude/config.txt   # 토큰·user ID 편집
bash scripts/install-linux-service.sh
```

**Linux에서 직접 빌드:**
```bash
git clone https://github.com/tyranno/teleclaude
cd teleclaude
go build -o teleclaude .
./teleclaude run                # 설정 마법사
```

---

## 설정 (`~/.teleclaude/config.txt`)

```ini
# 필수
TELEGRAM_BOT_TOKEN=123456789:AAH...
ALLOWED_USER_IDS=123456789

# 모델 (기본값)
MANAGER_MODEL=claude-haiku-4-5-20251001
WORKER_MODEL=claude-sonnet-4-6

# 선택
TIMEOUT_MINUTES=10
MANAGER_ALWAYS=true
# CLAUDE_PATH=/usr/bin/claude
```

전체 항목은 [`config.example.txt`](config.example.txt) 참조.

처음 실행 시 설정 마법사가 자동으로 안내합니다 (`teleclaude run`):
1. **봇 만들기 + 토큰** — [@BotFather](https://t.me/BotFather) 5단계 안내 + 즉시 검증
2. **내 계정 연결** — 봇에게 메시지 한 번 보내면 user ID 자동 감지
3. **(선택) 첫 프로젝트 폴더** 등록

---

## 사용법

봇에게 **그냥 말하면** 됩니다:

```
나: myapp 로그인 버그 이어서 보자
봇: 📂 myapp · 💬 로그인 버그 (이어가기)
    <작업 결과...>

나: voice 서버에 헬스체크 엔드포인트 새로 만들자
봇: 📂 voicesvr · 💬 헬스체크 엔드포인트 (새 대화)
    <작업 결과...>

나: 그거 다시 보자
봇: 🤔 어느 대화일까요? 1) 로그인 버그  2) 헬스체크 엔드포인트
```

### 명령어

| 명령 | 설명 |
|------|------|
| `!project add <이름> <경로>` | 프로젝트 등록 |
| `!project list` | 프로젝트·대화 목록 |
| `!chat new [제목]` | 새 대화 |
| `!chat list` | 대화 목록 |
| `!status` | 현재 활성 대화 + 실행 중 작업 |
| `!cancel` | 진행 중 작업 취소 |
| `!remind <시간> <메시지>` | 알림 등록 (예: `!remind 30m 회의`) |
| `!remind <시간> task <프롬프트>` | Claude 작업 예약 |
| `!task add <주기> [task] <프롬프트>` | 반복 작업 등록 (cron) |
| `!task list` | 작업 목록 |
| `!task pause/resume/cancel <ID>` | 작업 제어 |
| `!history [프로젝트] [날짜]` | 대화 히스토리 조회 |
| `!backend [claude\|codex]` | AI 백엔드 전환 |
| `!update` | 새 버전 빌드 & 자동 재시작 (Windows) |
| `!help` | 전체 도움말 |

### 알림 · 작업 스케줄 예시

```
!remind 30m 커피 마시기
!remind 09:00 task 오늘 할 일 정리해줘
!remind 2026-06-15 18:00 task 월간 리포트 작성

!task add daily task 매일 오전 9시 할 일 목록
!task add 0 9 * * 1-5 task 평일 오전 스탠드업 준비
!task add @every 2h 서버 상태 확인해줘
!task add 30m --script ~/scripts/check.sh task 체크 결과 분석
```

---

## 배포 스크립트

| 파일 | 설명 |
|------|------|
| [`scripts/deploy-linux.sh`](scripts/deploy-linux.sh) | 크로스컴파일 + SSH 배포 + 서비스 재시작 |
| [`scripts/install-linux-service.sh`](scripts/install-linux-service.sh) | systemd user 서비스 설치 (대상 머신에서 실행) |
| [`scripts/install-windows-task.ps1`](scripts/install-windows-task.ps1) | Windows 작업 스케줄러 등록 |
| [`launcher.ps1`](launcher.ps1) | Windows hot-swap 업데이트 런처 |

### 배포 워크플로 (Windows → NanoPi/ARM64)

```
1. 코드 수정
2. .\scripts\deploy-linux.sh nanopi   ← 빌드 + SCP + 서비스 재시작 자동화
3. 텔레그램 봇 테스트
```

---

## 플러그인 확장 (aglink-*)

teleclaude 본체는 텔레그램 봇/라우팅/스케줄러만 다루고, **실제 화면·브라우저
조작**은 sibling 저장소로 독립 배포되는 aglink-* 플러그인이 담당합니다. 각자
자체 GitHub 저장소를 갖는 완전히 독립된 프로젝트지만, teleclaude 옆에
나란히 두면 워커의 `--mcp-config`에 자동으로 물려 도구로 노출됩니다.

| 플러그인 | 기능 |
|---|---|
| [`aglink-screen`](https://github.com/tyranno/aglink-screen) | Windows 화면 제어 (UIA/Win32/GDI — snapshot/invoke/click/screenshot/type 등, Windows 전용) |
| [`aglink-web`](https://github.com/tyranno/aglink-web) | 실제 Chrome 브라우저 제어 (list_tabs/navigate/get_page_text/click/type/screenshot 등) |

### 설치

각 플러그인을 teleclaude와 **형제 디렉터리**로 clone하고 빌드해서 teleclaude
실행파일과 같은 폴더에 둡니다:

```
88.MyProject/
├── teleclaude/
│   ├── teleclaude.exe
│   ├── aglink-screen.exe   ← 여기 나란히
│   └── aglink-web.exe      ← 여기 나란히
├── aglink-screen/          ← 소스 (형제 디렉터리)
└── aglink-web/              ← 소스 (형제 디렉터리)
```

`config.yaml`에서 켭니다 (레거시 `config.txt` 포맷은 지원하지 않음 — yaml 전용):

```yaml
screen_control:
  enabled: true
  binary_path: ""   # 비우면 teleclaude exe와 같은 폴더에서 자동 탐색

web_control:
  enabled: true
  binary_path: ""   # 비우면 teleclaude exe와 같은 폴더에서 자동 탐색
```

둘 다 켜도 워커의 `--mcp-config`/`--allowedTools`가 자동으로 하나로 병합되어
노출됩니다 (Claude CLI가 이 플래그들을 1회씩만 받기 때문에, 플러그인마다 따로
넘기면 나중 것이 앞 것을 덮어씁니다 — teleclaude가 이 병합을 대신 처리).

### `!update`가 셋 다 같이 배포

`!update`는 teleclaude 자체를 빌드하기 전에 형제 디렉터리에 있는
`aglink-screen`/`aglink-web`도 먼저 `go build`해서 teleclaude 옆에 배치합니다
— 저장소 3개를 손으로 각각 빌드/복사할 필요 없이 명령 하나로 전부
최신화됩니다. 플러그인 중 하나라도 빌드가 깨지면 teleclaude 자체 업데이트도
시작하지 않고 에러만 보고합니다. 형제 디렉터리가 없는 배포(예: 화면제어가
필요 없는 헤드리스 NanoPi)에서는 조용히 건너뜁니다.

> ⚠️ `aglink-web`의 Chrome 확장(`extension/`)이 바뀐 경우 `!update`가 새
> 바이너리는 배포해주지만, Chrome에 이미 로드된 확장 자체는 자동으로
> 리로드되지 않습니다 — `chrome://extensions`에서 수동으로 새로고침해야
> 반영됩니다.

---

## 동작 방식

```
[Telegram] → bot(인증, 직렬 큐)
    → Manager(경량 모델, 프로젝트·대화 라우팅)
    → store.json (프로젝트 → 대화 → 세션 UUID)
    → Worker(claude --resume, cwd=프로젝트 디렉터리)
    → 결과 4096자 분할 회신
```

각 claude 실행은 `--strict-mcp-config` + `--setting-sources project,local` 으로 격리됩니다
(전역 MCP 서버 차단, OAuth 인증은 유지).

상태 파일: `~/.teleclaude/store.json`  
태스크 파일: `~/.teleclaude/tasks.json`  
히스토리: `~/.teleclaude/history/<프로젝트>/<YYYY-MM-DD>.md`

## 로그

```bash
# Linux (systemd)
tail -f ~/.teleclaude/logs/teleclaude.error.log
journalctl --user -u teleclaude -f

# Windows (Task Scheduler)
# 표준 출력 없음 — 로그 파일 설정은 launcher.ps1 수정 필요
```

---

## 한계 (현재)

- 한 번에 한 작업만 처리 (직렬화). 진행 중 새 메시지는 `!cancel` 후 재시도.
- claude 콜드스타트 지연 (호출당 수~십수 초). `MANAGER_ALWAYS=false`로 완화 가능.
- `!update` (hot-swap 업데이트)는 현재 Windows 전용. Linux는 `deploy-linux.sh` 사용.
