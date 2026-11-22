# 교차 검증 요청: 원격 SSH 창에서 aglink-screen 연결 실패 수정

- 저장소: `github.com:tyranno/aglink` (main)
- 커밋: `5eb46b8` — vscode: start aglink-screen serve on demand for Remote-SSH windows
- 변경 파일: `vscode/relay.js`, `vscode/package.json` (aglink-vscode 0.4.2 → 0.4.3)
- 함께 읽을 파일: `vscode/bridge/extension.js`, `screen/screenproxy.go`, `screen/mcphttp_windows.go`, `installer/aglink-setup.nsi`

검토자에게 부탁하는 일은 두 가지입니다.
1. 이 수정이 맞는지, 그리고 놓친 버그가 없는지 확인해 주세요.
2. 같은 증상을 완전히 막으려면 기능이 더 필요한지 판단해 주세요(아래 "열린 질문" 참고).

## 1. 구조 (요약)

Windows PC에서 VS Code를 열고 Remote-SSH로 Linux 서버(100.64.0.1)에 접속합니다. 그 서버에서 실행되는 Claude Code가 Windows PC의 브라우저(aglink-web)와 화면(aglink-screen)을 조작합니다.

```
[Linux 서버]  Claude Code
   └─ ~/bin/aglink-screen mcp  (프록시 모드: 127.0.0.1:$AGLINK_SCREEN_PORT/mcp 로 전달)
        └─ aglink-bridge 확장 (원격 확장 호스트, 창마다 임의 포트로 listen)
             └─ vscode.commands.executeCommand("aglink.relay", ...)
[Windows PC]  aglink-vscode 확장의 relay.js
   └─ http://127.0.0.1:48220/mcp  ← aglink-screen serve (HTTP MCP 서버)
   └─ http://127.0.0.1:48219      ← aglink-web serve
```

- 원격 쪽 Authorization 헤더는 relay가 버립니다. PC 쪽 relay가 자기 프로세스 환경의 `AGLINK_SCREEN_REMOTE_TOKEN`을 붙입니다(`destination()`).

## 2. 증상과 원인

- **증상:** 원격 SSH 창의 Claude Code에서 aglink-screen 도구를 부르면 매번 실패합니다. aglink-web은 정상입니다.
- **원격 서버에서 재현한 오류:**
  ```
  screen proxy: cannot reach the screen server at http://127.0.0.1:39477/mcp ...:
  aglink relay: screen (127.0.0.1:48220) is not reachable on this PC: connect ECONNREFUSED 127.0.0.1:48220
  ```
- **원인:** `relay()`는 ECONNREFUSED가 나면 **web만** 데몬(`aglink-web serve`)을 띄우고 다시 시도했습니다. `aglink-screen serve`는 이 경로에서도, 설치 프로그램에서도, aglink 본체에서도 띄우는 곳이 없었습니다. aglink-web은 로컬 `aglink-web mcp`가 serve 데몬을 스스로 띄우기 때문에 우연히 동작해 왔던 것입니다.

## 3. 수정 내용

`vscode/relay.js`
- `webDaemonExe()`를 `daemonExe(target)`으로 바꿨습니다. `aglink-web.exe` / `aglink-screen.exe`를 같은 순서로 찾습니다: `AGLINK_WEB_EXE`/`AGLINK_SCREEN_EXE` 환경변수 → `%ProgramFiles%\aglink\` → `%LOCALAPPDATA%\Programs\aglink-<name>\`.
- `startWebDaemon()`을 `startDaemon(target, readPort)`로 바꿨습니다.
  - screen은 `serve --addr 127.0.0.1:48220`으로 실행합니다. 토큰이 설정돼 있으면 `RunScreenRemote`의 기본 주소가 `0.0.0.0:48220`이기 때문에 loopback으로 고정했습니다.
  - 자식 프로세스는 relay가 보내는 것과 같은 토큰(`AGLINK_SCREEN_REMOTE_TOKEN`)을 VS Code 프로세스 환경에서 상속받습니다.
  - 상태 확인 포트는 `destination({target}, readPort).port`로 구합니다(web=readPort(), screen=48220). 경로는 `/health`입니다.
  - 동시 요청은 target별로 시도 하나를 공유합니다(`starting[target]`).
- `relay()`는 `web` 또는 `screen`이 refused이면 데몬을 띄운 뒤 한 번 다시 시도합니다.

## 4. 검증한 것

- 수정 전 원격 서버에서 `~/bin/aglink-screen mcp`로 `control_status`를 호출하면 위 ECONNREFUSED가 났습니다. PC에서 serve를 수동으로 띄우자 정상 응답했습니다.
- 수정 후 PC에서 screen serve를 종료한 상태로 `relay({target:"screen", path:"/mcp", tools/list})`를 직접 호출했습니다. serve가 자동으로 실행됐고(`serve --addr 127.0.0.1:48220`), 200 응답과 도구 목록을 받았습니다.
- `node --test vscode/test/*.test.js`: 23개 중 22개 통과. 실패 1개(`terminals.test.js`, 50초 시간 초과)는 수정 전 코드에서도 똑같이 실패합니다.
- **하지 않은 것:** relay의 screen 자동 시작에 대한 단위 테스트는 추가하지 않았습니다. 실제 VS Code 창을 Reload한 뒤 Claude Code에서 screen 도구를 부르는 종단 확인도 아직 하지 않았습니다.

## 5. 열린 질문 (검토자 판단 요청)

1. **토큰 불일치:** serve가 이미 다른 토큰으로 떠 있으면(수동 실행, 사용자가 토큰을 바꾼 뒤 VS Code는 예전 환경 유지 등) 401이 나고, refused가 아니므로 재시작하지 않습니다. 401일 때 다시 띄우는 처리가 필요할까요? 그러면 다른 프로세스가 띄운 serve를 죽여야 하는 문제가 생깁니다.
2. **원격 쪽 토큰의 의미:** 원격 `~/.claude.json`의 `aglink-screen-remote`에 `AGLINK_SCREEN_REMOTE_TOKEN`(PC 토큰과 다른 값)이 들어 있습니다. bridge 경로에서는 relay가 이 헤더를 버리니 쓰이지 않습니다. 이 값을 정리하거나 문서화해야 할까요?
3. **aglink-web이 엉뚱한 PC로 붙는 문제:** 원격 서버에서 `AGLINK_WEB_PORT` 없이 `aglink-web mcp`를 실행하면, 같은 서버에 붙어 있는 **회사 PC**의 브라우저 목록이 나왔습니다(기본 포트로 다른 창의 경로를 탄 것으로 보임). `bridge/extension.js` 주석은 `~/.claude.json`이 `${AGLINK_WEB_PORT:-48219}`를 읽는다고 하지만, 실제 원격 설정에는 이 치환이 없고 프로세스 환경에만 의존합니다. bridge 포트가 없을 때는 기본 포트로 넘어가지 말고 명확히 실패해야 하지 않을까요? screen도 `screenproxy.go`가 48220으로 넘어갑니다.
4. **권한(UIPI):** serve는 VS Code 권한으로 뜹니다. aglink를 관리자로 실행하는 설정(`screen_control.elevated`)에서는 관리자 창을 클릭하지 못합니다. relay가 띄우는 serve에도 이 설정을 반영해야 할까요?
5. **수명:** serve는 detached로 떠서 VS Code를 닫아도 남습니다(web 데몬과 같은 정책). 설치 프로그램은 `taskkill /F /T /IM aglink-screen.exe`로 종료하니 업데이트는 막히지 않습니다. 화면을 조작할 수 있는 HTTP 서버가 계속 떠 있어도 괜찮을까요, 아니면 유휴 시 종료가 필요할까요?
6. **포트 충돌:** 48220을 다른 프로세스(예: 토큰 있이 `0.0.0.0:48220`으로 수동 실행한 serve)가 잡고 있으면 새 serve는 바로 종료되고, 5초 동안 상태 확인만 하다 실패합니다. 오류 메시지를 더 분명히 해야 할까요?
7. **테스트:** `startDaemon`의 screen 경로(인자, 토큰 상속, 동시 요청 공유)에 단위 테스트를 추가해야 할까요? 그렇다면 spawn을 주입할 수 있게 구조를 바꾸는 편이 좋을까요?

## 6. 응답 형식 요청

- 버그 / 보안 / 개선 제안을 구분해 주세요. 각 항목에 파일과 줄 번호를 붙여 주세요.
- 위 열린 질문 1~7에 각각 "필요 / 불필요 / 보류"와 이유를 한두 줄로 답해 주세요.
