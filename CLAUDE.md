# aglink — agent notes

리포 구조·빌드 방법은 [`README.md`](./README.md) 를 먼저 볼 것. 여기에는 그
문서에 없는, 작업할 때 걸려 넘어지기 쉬운 것만 적는다.

## 이 저장소가 본류다

`c:\Project\88.MyProject\Teleclaude` 는 이 저장소로 합쳐지기 전의 **구 리포**이고
2026-07-20 이후로 실작업이 없다. 같은 루트 커밋을 공유하며 이 저장소가 그쪽을
완전히 포함한다. **빌드·배포는 반드시 여기서** — 2026-08-24 에 구 리포에서
빌드해 한 달 묵은 코드를 서버에 올린 적이 있다.

## 서버(nanopi) 접속·운영 정보는 추적되지 않는 파일에 있다

이 저장소는 **GitHub 공개 저장소**(`tyranno/aglink`)다. 호스트 주소·계정·봇
식별자 같은 것은 커밋하지 않는다. 대신 다음 파일들에 있고 `*.local.md` 로
gitignore 되어 있다 — **로컬 워킹트리에만 존재한다.**

| 파일 | 내용 |
|---|---|
| `docs/deploy/nanopi.local.md` | nanopi SSH 접속 방법, 서비스/경로/스케줄 목록, 백업 위치, 비밀값이 **어디 있는지** |
| `docs/deploy/nanopi-2026-08-24-incident.local.md` | 2026-08-24 장애 점검 기록 (증거·원인·배제한 가설) |

서버 관련 작업을 시작하기 전에 위 파일을 먼저 읽을 것. 파일이 없다면 이
워킹트리가 새로 클론된 것이다 — 사용자에게 물어볼 것.

일반화된 배포·운영 절차(호스트 무관)는 추적되는
[`docs/deploy/linux-server.md`](./docs/deploy/linux-server.md) 에 있다.

**비밀값(봇 토큰, OAuth 토큰, API 키)은 어느 파일에도 적지 않는다.** 필요하면
서버에서 직접 읽는다. gitignore 되어 있어도 공개 리포 워킹트리 안이라
`git add -f` 한 번이면 새어 나간다.

## 빌드 시 `-mod=readonly` 를 강제해야 한다

이 머신의 `go env GOFLAGS` 에 `-mod=mod` 가 박혀 있는데, `go.work` 아래에서는
거부된다:

```
go: -mod may only be set to readonly or vendor when in workspace mode
```

전역 설정을 바꾸지 말고 명령 단위로 덮어쓸 것:

```sh
GOFLAGS=-mod=readonly go test ./...
GOFLAGS=-mod=readonly GOOS=linux GOARCH=arm64 go build -o aglink-linux-arm64 .
```

`scripts/deploy-linux.sh` 는 이미 이렇게 한다.

## 배포 경로 함정 두 개

둘 다 실제로 운영 장애를 냈다. 자세한 건 `docs/deploy/linux-server.md` §5.

- 바이너리를 **`~/aglink` 에 두지 말 것** — 그 경로는 `host/homedir.go` 의
  `defaultHomeDir()`, 즉 모든 워커의 작업 디렉터리다. 파일이 있으면 cwd 가
  디렉터리가 아니게 되어 `fork/exec ...: not a directory` 로 CLI 가 죽는다.
  `~/bin/aglink` 를 쓴다.
- **`~/.aglink` 를 미리 만들지 말 것** — `host/config.go` 의 `dataDir()` 은
  `~/.aglink` 가 **없을 때만** 구 `~/.teleclaude` 를 복사 이전한다. 로그
  디렉터리 만들자고 먼저 생성하면 마이그레이션이 스킵되고 빈 데이터로 기동한다.

## 여러 세션이 같은 워킹트리를 동시에 만진다

이 머신에는 Claude Code 세션이 둘 이상 동시에 열려 있는 일이 잦고, 워커가
스스로 이 리포를 편집하기도 한다. 잠금이나 조율 장치는 없다.

- 여러 파일을 고치거나 되돌리기 어려운 작업 전에는 `git status` / `git diff` 로
  **내가 만들지 않은 변경**을 먼저 확인할 것.
- 커밋 직전에 `git status` 를 다시 볼 것 — 그 사이에 다른 세션의 변경이 들어올
  수 있다. `git add -A` 대신 **내 파일만 지정해서** add 한다.
- 배포용 빌드는 미커밋 작업이 섞이지 않도록 HEAD 기준 워크트리에서 한다:
  `git worktree add --detach /c/tmp/<name> HEAD`

## 서버 배포 후에는 스케줄 경로까지 실제로 돌려볼 것

바이너리를 바꾸면 대화형 응답이 되는 것만으로는 부족하다. 스케줄 작업은 별도
경로(`HandleScheduledTask`)를 타고, 프로젝트 미등록 같은 경우 **로그를 남기지
않고 조용히 반환**한다. 검증 레시피는 `docs/deploy/linux-server.md` §8.

응답을 길이로 판단하지 말 것 — 인증 만료 시 CLI 가 exit 0 으로 끝나며 뱉는
401 문구가 약 99바이트라, 정상적인 짧은 답변과 구분되지 않는다.
