# teleclaude 빠른 시작 (테스터용)

텔레그램 봇 + (선택) 브라우저 채팅창 하나로 내 컴퓨터에서 `claude` CLI를 돌려 코딩/작업을
시키는 개인용 도구입니다. **1인 1인스턴스** 구조라 — 다른 사람이 써보려면 이 저장소를
직접 clone해서 **본인 컴퓨터에 본인 봇으로 직접 실행**해야 합니다 (원격으로 남의
인스턴스에 접속하는 구조가 아님).

---

## 0. 준비물

- Go 1.25+ ([설치](https://go.dev/dl/))
- `claude` 또는 `codex` CLI 중 최소 하나 설치 + 로그인 완료 (해당 명령이 그냥 실행되는 상태).
  claude만 있어도, codex만 있어도, 둘 다 있어도 됩니다 — 설정 마법사가 설치된 것을 자동 감지합니다.
- 텔레그램 계정 (봇 토큰은 실행 중 마법사가 만들어줌)
- Windows 또는 Linux

## 1. 저장소 clone (전부 형제 디렉터리로!)

teleclaude 본체 + 원하는 플러그인을 **같은 부모 폴더 아래** 나란히 clone하면
teleclaude가 자동으로 찾아줍니다. 전부 선택사항(텔레그램만 쓸 거면 teleclaude 하나로
충분)이지만, 아래처럼 다 받아두면 이 가이드의 모든 기능을 써볼 수 있습니다.

```powershell
mkdir myfolder; cd myfolder
git clone https://github.com/tyranno/teleclaude
git clone https://github.com/tyranno/aglink-chat      # 브라우저 채팅 UI
git clone https://github.com/tyranno/aglink-screen    # Windows 화면 제어 (Windows 전용)
git clone https://github.com/tyranno/aglink-web       # 실제 Chrome 브라우저 제어
```

> 이 단계는 사실 건너뛰어도 됩니다 — `teleclaude`만 clone해서 3단계(설정 마법사)까지
> 진행하면, 없는 aglink-* 저장소를 마법사가 자동으로 clone+빌드해줄지 하나씩 물어봅니다
> (Windows 전용, git/go가 PATH에 있어야 함).

```
myfolder/
├── teleclaude/        ← 봇 본체
├── aglink-chat/        ← 브라우저 채팅 UI
├── aglink-screen/      ← 화면 제어 플러그인
└── aglink-web/         ← 브라우저 제어 플러그인
```

## 2. 빌드

```powershell
cd teleclaude;      go build -o teleclaude.exe .;      cd ..
cd aglink-chat;     go build -o aglink-chat.exe .;     cd ..
cd aglink-screen;   go build -o aglink-screen.exe .;   cd ..
cd aglink-web;      go build -o aglink-web.exe .;      cd ..
```

(Linux는 확장자(`.exe`) 없이 동일 — `aglink-screen`은 Windows 전용이라 Linux에는 없음)

## 3. 첫 실행 — 설정 마법사

```powershell
cd teleclaude
.\teleclaude.exe run
```

처음 실행하면 마법사가 순서대로 안내합니다:
1. **봇 만들기** — [@BotFather](https://t.me/BotFather)에게 `/newbot` → 토큰 발급 → 붙여넣기 (즉시 검증)
2. **내 계정 연결** — 안내대로 봇에게 아무 메시지나 한 번 보내면 자동으로 내 user ID 등록
3. **(선택) 첫 프로젝트 폴더 등록**
4. **(선택, Windows) 빠진 aglink-\* 자동 설치** — 형제 폴더에 없는 것마다 clone+빌드할지 물어봄.
   `aglink-web`을 새로 설치했다면 Chrome을 열어 `chrome://extensions`까지 띄워주는데,
   압축해제된 확장은 크롬 정책상 완전 무클릭 설치가 안 되어 "개발자 모드 켜기 → 압축해제된
   확장 프로그램을 로드합니다 → 폴더 선택"까지는 직접 클릭해야 합니다.

완료되면 `~/.teleclaude/config.yaml`이 생성되고, 텔레그램에서 봇에게 말을 걸면 바로 동작합니다.

## 4. 브라우저 채팅 UI 켜기 (기본값 = 꺼짐)

`aglink-chat`을 clone했다면, `~/.teleclaude/config.yaml`을 열어 아래 두 블록을 추가하고
teleclaude를 재시작하세요:

```yaml
chat_control:
  enabled: true
aglink_chat:
  enabled: true
```

재시작 로그에 아래처럼 뜹니다:

```
[aglinkchat] http://127.0.0.1:1717/?token=xxxxxxxx...
```

그 주소를 그대로 브라우저에 붙여넣으면 채팅 UI가 뜨고, 텔레그램과 완전히 같은 대화
상태를 공유합니다 (둘 중 아무 데서나 이어서 대화 가능). 이 서버는 **로컬(loopback)
전용**이라 같은 컴퓨터의 브라우저에서만 열립니다.

## 5. 사용법

봇/웹 채팅창에 그냥 자연어로 말하면 됩니다:

```
myapp 로그인 버그 이어서 보자
voice 서버에 헬스체크 엔드포인트 새로 만들자
그거 다시 보자   → 봇이 어느 대화인지 되물음
```

자주 쓰는 명령어:

| 명령 | 설명 |
|------|------|
| `!project add <이름> <경로>` | 프로젝트 등록 |
| `!project list` | 프로젝트·대화 목록 |
| `!chat new [제목]` | 새 대화 |
| `!status` | 현재 활성 대화 + 실행 중 작업 |
| `!cancel` | 진행 중 작업 취소 |
| `!remind <시간> <메시지>` | 알림 (예: `!remind 30m 회의`) |
| `!task add <주기> task <프롬프트>` | 반복 작업 등록 (cron) |
| `!history [프로젝트] [날짜]` | 히스토리 조회 |
| `!update` | 최신 코드로 자동 재빌드+재시작 (Windows) |
| `!help` | 전체 도움말 |

## 5-1. Codex 백엔드 (선택 — 기본은 Claude)

워커를 Claude 대신 [OpenAI Codex CLI](https://github.com/openai/codex)로 돌릴 수 있습니다.
teleclaude 설치와 무관하게 완전히 선택사항이며, 설치가 안 돼 있으면 조용히 무시됩니다.
**claude가 아예 없는 codex 전용 환경**도 지원합니다 — `teleclaude setup` 마법사가 claude를
못 찾으면 자동으로 codex 전용 설정으로 진행하고, 부팅 시에도 둘 중 하나만 있으면 됩니다.

**1) codex CLI 설치 + 로그인**

```powershell
npm install -g @openai/codex
codex login
```

`codex` 명령이 PATH에서 그냥 실행되는 상태면 끝 — teleclaude가 시작할 때 자동으로 찾습니다
(`CODEX_PATH`/`backend.codex_path`로 경로를 직접 지정할 수도 있음).

**2) `config.yaml`에 추가 (선택 — 기본 백엔드를 아예 codex로 하고 싶을 때만)**

```yaml
backend:
  default: codex              # 비우면 claude가 기본
  codex_path: ""              # 비우면 PATH에서 자동 탐지
  codex_model: ""             # 비우면 codex 기본 모델
  codex_manager_model: ""     # 비우면 codex 기본 모델
```

**3) 실행 중 전환 (설정 안 바꿔도 즉시 전환 가능)**

```
!backend            → 현재 백엔드 확인
!backend codex       → codex로 전환
!backend claude       → claude로 되돌리기
```

명령어 없이 자연어로도 전환됩니다: `"코덱스로 전환해줘"`, `"claude 써줘"` 등 명시적인 전환
동사가 있어야 인식합니다(그냥 대화 중 "codex"라는 단어만 나오면 전환되지 않음). 진행 중인
작업이 있으면 전환이 거부되니 `!cancel` 후 다시 시도하세요.

## 6. aglink-screen — Windows 화면 제어 (선택)

에이전트가 실제 마우스/키보드처럼 창을 조작(스냅샷, 클릭, 타이핑, 스크린샷 등)하게
해줍니다. **Windows 전용**. `aglink-screen`을 clone+build했다면 `config.yaml`에 추가:

```yaml
screen_control:
  enabled: true
  binary_path: ""    # 비우면 teleclaude.exe와 같은 폴더에서 자동 탐색
  elevated: false    # 대상 앱이 "관리자 권한"으로 떠 있으면 true로 (UIPI 우회)
  keep_awake: false
```

teleclaude를 재시작하면 워커(claude)가 자동으로 이 도구들을 인식합니다. 그냥
자연어로 시키면 됩니다:

```
메모장 열어서 "안녕" 이라고 쓰고 저장해줘
지금 열려있는 창 목록 보여줘
```

LLM을 거치지 않는 즉시-실행 명령(`!screen`)도 있습니다 (빠른 반복 조작용):

| 명령 | 설명 |
|------|------|
| `!screen list` | 보이는 창 목록 |
| `!screen shot [창이름]` | 스크린샷 (창 지정 시 그 창만, 없으면 전체 화면) |
| `!screen region <x> <y> <w> <h> [창이름]` | 특정 영역만 캡처 |
| `!screen preset save <이름>` | 현재 커서 위치를 이름 붙여 저장 |
| `!screen click <프리셋이름>` | 저장한 좌표를 즉시 클릭 |

> 대상 앱이 관리자 권한으로 떠 있는데 클릭이 씹히면(UIPI), `screen_control.elevated: true`로
> 바꾸고 재시작하세요 — teleclaude 전체가 관리자 권한으로 재기동됩니다.

## 7. aglink-web — 실제 Chrome 브라우저 제어 (선택)

에이전트가 사용자의 **실제 Chrome**(탭 목록/이동/텍스트 읽기/클릭/타이핑/스크린샷)을
조작하게 해줍니다. 빌드 외에 Chrome 확장 설치가 한 번 더 필요합니다.

**1) Chrome 확장 로드 (최초 1회)**
1. `chrome://extensions` 접속 → 우측 상단 **개발자 모드** 켜기
2. **압축해제된 확장 프로그램을 로드합니다** → `aglink-web/extension` 폴더 선택
3. 카드에 표시된 확장 **ID**를 확인 (나중에 여러 확장이 섞여 헷갈리면 이 ID로 특정 가능)

확장은 로컬 데몬(`ws://127.0.0.1:48219`)에 자동으로 연결/재연결됩니다. 별도 설정 없이
바로 동작하며, ID를 고정하고 싶으면 teleclaude 실행 전 환경변수로
`AGLINK_WEB_EXT_ID=<그 ID>`를 설정하면 됩니다(선택사항).

**2) teleclaude에 연결**

```yaml
web_control:
  enabled: true
  binary_path: ""    # 비우면 teleclaude.exe와 같은 폴더에서 자동 탐색
```

재시작 후 자연어로 시키면 됩니다:

```
크롬 탭 목록 보여줘
example.com 열어서 페이지 내용 읽어줘
검색창에 "날씨" 치고 검색해줘
```

`screen_control`과 동시에 켜도 워커가 두 플러그인 도구를 하나로 병합해서 받으므로
문제없이 같이 동작합니다.

> teleclaude 없이 `aglink-web`만 단독 테스트하려면:
> ```sh
> ./aglink-web.exe serve                    # 데몬 기동 (1회, 백그라운드)
> ./aglink-web.exe cmd list_tabs            # 열린 탭 확인
> ./aglink-web.exe cmd navigate https://example.com
> ```

## 8. 전부 켰을 때 최종 `config.yaml` 모양 (참고용)

```yaml
telegram:
  bot_token: "..."          # 마법사가 자동으로 채움
  allowed_user_ids: [123456789]

chat_control:
  enabled: true
aglink_chat:
  enabled: true

screen_control:
  enabled: true
web_control:
  enabled: true
```

## 9. 업데이트 (`!update`)

teleclaude, aglink-chat, aglink-screen, aglink-web을 전부 형제 디렉터리로 clone해뒀다면,
텔레그램/웹 채팅창에서 `!update` 한 번이면 **넷 다** 최신 소스로 재빌드 + 무중단
재시작됩니다 (Windows 전용 기능). 없는 저장소는 조용히 건너뜁니다.

> ⚠️ `aglink-web`의 Chrome 확장 코드(`extension/`) 자체가 바뀐 경우, `!update`가 새
> 바이너리는 배포해주지만 이미 로드된 Chrome 확장은 자동 리로드되지 않습니다 —
> `chrome://extensions`에서 수동으로 새로고침해야 반영됩니다.

## 문제 해결

- **로그**: Windows는 콘솔 출력 그대로, Linux(systemd)는 `~/.teleclaude/logs/teleclaude.error.log`
- **포트 충돌**: `config.yaml`의 `aglink_chat.addr` / `chat_control.addr` 값을 바꾸면 됨
- **1717/17170 포트가 이미 사용 중**이거나 `aglink-chat` 바이너리가 안 잡히면, 재시작 로그에
  `[aglinkchat] binary not found` / `listen ... failed` 메시지로 원인이 찍힘
- **aglink-web 확장이 안 붙음**: `chrome://extensions`에서 확장이 활성 상태인지, 데몬 포트
  (`48219`)를 바꿨다면 확장 옵션 페이지에도 같은 포트를 입력했는지 확인
- **aglink-screen 클릭이 안 먹음**: 대상 앱이 관리자 권한인지 확인 → `screen_control.elevated: true`
- 더 자세한 설명 전체는 [`README.md`](README.md) 참고
