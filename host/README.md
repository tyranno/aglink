# aglink

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
go build -o aglink.exe .
.\aglink.exe run        # 처음 실행 시 설정 마법사 자동 시작
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
# 바이너리를 ~/aglink 로 복사한 후:
bash scripts/install-linux-service.sh

# 또는 수동으로 설정 파일 먼저 작성:
cp config.example.txt ~/.aglink/config.txt
nano ~/.aglink/config.txt   # 토큰·user ID 편집
bash scripts/install-linux-service.sh
```

**Linux에서 직접 빌드:**
```bash
git clone https://github.com/tyranno/aglink
cd aglink
go build -o aglink .
./aglink run                # 설정 마법사
```

---

## 설정 (`~/.aglink/config.txt`)

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

처음 실행 시 설정 마법사가 자동으로 안내합니다 (`aglink run`):
1. **봇 만들기 + 토큰** — [@BotFather](https://t.me/BotFather) 5단계 안내 + 즉시 검증
2. **내 계정 연결** — 봇에게 메시지 한 번 보내면 user ID 자동 감지
3. **(선택) 첫 프로젝트 폴더** 등록

---

## 브라우저 채팅 UI (선택, 기본 꺼짐)

텔레그램 없이(또는 텔레그램과 병행해) 브라우저에서도 같은 대화 상태로 채팅할 수 있습니다.
별도 저장소인 [`aglink-chat`](https://github.com/tyranno/aglink-chat)이 실제 웹 서버 역할을
하고, aglink는 로컬 전용 control API로만 붙습니다.

```
88.MyProject/
├── aglink/        ← 이 저장소
└── aglink-chat/   ← 형제 디렉터리로 clone + go build
```

`~/.aglink/config.yaml`에 추가 후 재시작:

```yaml
chat_control:
  enabled: true
aglink_chat:
  enabled: true
```

재시작 로그에 뜨는 `http://127.0.0.1:27271/?token=...` 주소를 브라우저로 열면 됩니다
(로컬 전용, 같은 컴퓨터에서만 접속 가능). 자세한 단계는 [`QUICKSTART.md`](QUICKSTART.md) 참고.

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

## 원격 Claude 세션에 붙기

VS Code 를 SSH 원격으로 열어 놓고 그 안에서 Claude 세션이 돌고 있을 때, 그
세션과 텔레그램으로 직접 주고받는다. 화면을 캡처하지 않고, 원격에 설치할 것도
없다.

```
!sessions            붙을 수 있는 세션 목록
!attach <번호|이름>   붙기 — 이후 평문은 전부 그 세션으로
!detach              풀기
!status              지금 붙어 있는 세션 확인
```

목록은 이렇게 나온다. **이름을 몰라도 번호로 고를 수 있다.**

```
1. proj-a-cf  dev  1시간째 · 쉬는 중
   마지막: 2단계가 끝났습니다. 담기와 짓기를 지시했습니다.
2. proj-b-4f  dev  2시간째 · 일하는 중
```

`"proj-a 제어할게"` 처럼 평문으로도 붙는다.

### 갈아탈 때는 `!` 로

다른 세션으로 옮길 때 `!detach` 를 먼저 할 필요는 없다. `!attach` 를 다시
치면 그대로 옮겨 붙는다.

다만 **붙어 있는 동안 평문은 예외 없이 그 세션으로 간다.** 그래서
`"proj-b 제어할게"` 라고 치면 그 말이 지금 붙어 있는 세션에게 전달되고 만다.
평문으로 붙는 것은 아무 데도 붙어 있지 않을 때만 동작한다. 갈아탈 때는 반드시
`!attach <번호|이름>` 을 쓴다.

이름은 앞부분만 대도 된다(`proj-a-cf` → `proj-a`). 둘 이상에 걸리면 어느
것인지 되묻는다. 텔레그램 입력창이 하이픈을 다른 문자로 바꿔 버리는 일이
있으니, 하이픈 앞까지만 치거나 번호를 쓰는 편이 안전하다.

### 켜는 법

`config.yaml` 의 `ssh.hosts` 항목에 한 줄을 더한다. 기본은 꺼져 있다 — 목록을
만들 때 그 계정에서 `claude -p` 가 한 번 돌아 토큰을 쓰므로, 대화형 세션이 없는
호스트를 뒤질 이유가 없다.

```yaml
ssh:
  enabled: true
  hosts:
    - name: dev
      host: 10.0.0.2
      user: someone
      key_file: ~/.ssh/id_ed25519
      claude_sessions: true    # 이 호스트를 !sessions 가 뒤진다
      # claude_bin: /opt/claude  # PATH 로 못 찾을 때만
```

### 알아둘 것 셋

- **SSH 계정이 띄운 세션만 보인다.** 세션 소켓이 계정별 `0600` 이라 남의 계정
  것은 보이지도 읽히지도 않는다. 작업 디렉터리가 다른 사람 홈 아래에 있어도
  소유자는 세션을 **띄운** 계정이다. 두 계정을 다 쓰려면 `ssh.hosts` 에 둘 다
  등록한다.
- **목록에 그 세션이 마지막으로 한 말이 실려 나간다.** 110자에서 자르지만,
  세션 화면에 있던 것은 무엇이든 거기 있을 수 있다. 민감한 것을 띄워 둔
  세션에는 쓰지 않는 편이 낫다.
- **붙은 상태는 기억되지 않는다.** 호스트가 재시작하면 전부 풀린다. 원격
  세션이 호스트보다 먼저 죽는 쪽이라, 살아 있지도 않은 이름에 붙어 있다고
  적힌 상태가 더 나쁘다. 지금 무엇에 붙어 있는지는 `!status` 가 늘 보여 준다.

읽기(목록·마지막 한 줄·턴이 끝났는지)는 전부 셸로 하고 LLM 을 태우지 않는다.
토큰이 드는 곳은 이름·busy 조회와 실제 보내기 두 군데뿐이다.

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

aglink 본체는 텔레그램 봇/라우팅/스케줄러만 다루고, **실제 화면·브라우저
조작**은 sibling 저장소로 독립 배포되는 aglink-* 플러그인이 담당합니다. 각자
자체 GitHub 저장소를 갖는 완전히 독립된 프로젝트지만, aglink 옆에
나란히 두면 워커의 `--mcp-config`에 자동으로 물려 도구로 노출됩니다.

| 플러그인 | 기능 |
|---|---|
| [`aglink-screen`](https://github.com/tyranno/aglink-screen) | Windows 화면 제어 (UIA/Win32/GDI — snapshot/invoke/click/screenshot/type 등, Windows 전용) |
| [`aglink-web`](https://github.com/tyranno/aglink-web) | 실제 Chrome 브라우저 제어 (list_tabs/navigate/get_page_text/click/type/screenshot 등) |

### 설치

각 플러그인을 aglink와 **형제 디렉터리**로 clone하고 빌드해서 aglink
실행파일과 같은 폴더에 둡니다:

```
88.MyProject/
├── aglink/
│   ├── aglink.exe
│   ├── aglink-screen.exe   ← 여기 나란히
│   └── aglink-web.exe      ← 여기 나란히
├── aglink-screen/          ← 소스 (형제 디렉터리)
└── aglink-web/             ← 소스 (형제 디렉터리)
```

`config.yaml`에서 켭니다 (레거시 `config.txt` 포맷은 지원하지 않음 — yaml 전용):

```yaml
screen_control:
  enabled: true
  binary_path: ""   # 비우면 aglink exe와 같은 폴더에서 자동 탐색

web_control:
  enabled: true
  binary_path: ""   # 비우면 aglink exe와 같은 폴더에서 자동 탐색
```

둘 다 켜도 워커의 `--mcp-config`/`--allowedTools`가 자동으로 하나로 병합되어
노출됩니다 (Claude CLI가 이 플래그들을 1회씩만 받기 때문에, 플러그인마다 따로
넘기면 나중 것이 앞 것을 덮어씁니다 — aglink가 이 병합을 대신 처리).

### `!update`가 셋 다 같이 배포

`!update`는 aglink 자체를 빌드하기 전에 형제 디렉터리에 있는
`aglink-screen`/`aglink-web`도 먼저 `go build`해서 aglink 옆에 배치합니다
— 저장소 3개를 손으로 각각 빌드/복사할 필요 없이 명령 하나로 전부
최신화됩니다. 플러그인 중 하나라도 빌드가 깨지면 aglink 자체 업데이트도
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

상태 파일: `~/.aglink/store.json`  
태스크 파일: `~/.aglink/tasks.json`  
히스토리: `~/.aglink/history/<프로젝트>/<YYYY-MM-DD>.md`

## 로그

```bash
# Linux (systemd)
tail -f ~/.aglink/logs/aglink.error.log
journalctl --user -u aglink -f

# Windows (Task Scheduler)
# 표준 출력 없음 — 로그 파일 설정은 launcher.ps1 수정 필요
```

---

## 한계 (현재)

- 한 번에 한 작업만 처리 (직렬화). 진행 중 새 메시지는 `!cancel` 후 재시도.
- claude 콜드스타트 지연 (호출당 수~십수 초). `MANAGER_ALWAYS=false`로 완화 가능.
- `!update` (hot-swap 업데이트)는 현재 Windows 전용. Linux는 `deploy-linux.sh` 사용.
