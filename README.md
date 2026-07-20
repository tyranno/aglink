# aglink

개인 컴퓨터 에이전트. 대화 호스트 하나와, 그 호스트가 쓰는 "손과 눈"(MCP 서버), 그리고 클라이언트들로 이루어져 있습니다.

기존 `teleclaude` / `aglink-screen` / `aglink-web` / `aglink-chat` / `aglink-desktop` 다섯 저장소를
히스토리를 보존한 채 이 저장소로 합쳤습니다.

## 구조

```
host/      에이전트 호스트 — 대화 관리, 채널(telegram/web/desktop), 백엔드(claude/codex)
screen/    MCP 서버 — 화면(UIA/Win32/스크린샷) 제어. Windows 전용 기능
web/       MCP 서버 — 브라우저 DOM/엘리먼트 접근
chat/      웹 채팅 클라이언트
desktop/   데스크톱 클라이언트 (Wails)
```

`screen`/`web`은 독립 제품이 아니라 호스트가 spawn하는 도구입니다.
`host`가 단독으로 실행되는 본체이고, 나머지는 붙거나 빠질 수 있습니다.

## 빌드

`host` / `screen` / `web` / `chat`은 Go 워크스페이스(`go.work`)로 묶여 있습니다.

```sh
cd host && go build        # 나머지도 동일
```

리눅스(nanopi 등) 대상 교차 컴파일도 그대로 됩니다.

```sh
GOOS=linux GOARCH=arm64 go build ./...
```

리눅스에서는 화면 제어(`screen`)가 빠지고 텔레그램 채널 + 백엔드 CLI만으로 동작합니다.

### desktop

`desktop/`은 워크스페이스에서 제외돼 있습니다. 저장소 밖의 로컬 wails/v3 체크아웃
(`../../wails/v3`)에 의존하고, 프런트엔드를 먼저 빌드해야 하기 때문입니다.
독립적으로 빌드하세요.
