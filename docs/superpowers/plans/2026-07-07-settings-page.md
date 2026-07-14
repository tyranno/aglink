# 설정 페이지 (/setting) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development 또는 executing-plans. 체크박스(`- [ ]`) 추적.

**Goal:** 팝업으로 뜨던 config 편집 / aglink 상태를 `/setting` 탭 페이지로 이전. config는 구조화 UI 폼(설명 포함) + raw 편집 둘 다.

**Architecture:** [specs/2026-07-07-settings-page-design.md](../specs/2026-07-07-settings-page-design.md). teleclaude가 구조화 설정 스키마/적용을 소유(control RPC), aglink-chat이 `/api/settings` 프록시 + `/setting` SPA 서빙, app.js가 클라이언트 라우팅.

**Tech Stack:** Go 1.25, coder/websocket, Vanilla JS.

## Global Constraints
- 각 단계 `go build ./... && go vet ./... && go test ./...` 그린(양 저장소). gofmt CRLF: `tr -d '\r' | gofmt -l`.
- set_settings는 화이트리스트 키만, 기록 전 검증, teleclaude 고정 cfgPath만. 비밀값은 구조화 API 미노출.
- `node --check` app.js. push 금지. 커밋 trailer 유지.

---

## Task 1: teleclaude 설정 스키마 (settings.go)
**Files:** `settings.go`(신규), `settings_test.go`
**Interfaces (Produces):**
- `type settingField struct{ Key,Label,Desc,Type string; Value any; Options []string }`
- `type settingSection struct{ Title string; Fields []settingField }`
- `func buildSettings(cfg *Config) []settingSection`
- `func applySettings(cfg *Config, updates map[string]any) error` (화이트리스트, 타입강제 asString/asInt/asBool)

- [ ] 스키마 필드(스펙 표) 정의: buildSettings가 현재값 채워 반환.
- [ ] applySettings 화이트리스트 switch(미지 키 무시). asInt(float64/string), asBool(bool/string), asString.
- [ ] 테스트: buildSettings 섹션/필드 존재, set→get 라운드트립(models.manager 변경 반영), 미지 키 무시, 타입강제(float64→int).
- [ ] `go test -run Settings .` 그린. 커밋.

## Task 2: control RPC get_settings/set_settings
**Files:** `chatcontrol.go`, `chatcontrol_test.go`
**Interfaces:** controlIn.Type += `get_settings|set_settings`. set_settings: `m.Body`=JSON updates map.
- get_settings reply: `{sections: buildSettings(s.bot.cfg())}`.
- set_settings: `updates := json.Unmarshal(m.Body)`; `newCfg := *s.bot.cfg()`; `applySettings(&newCfg, updates)`; `raw,_ := marshalConfigYAML(&newCfg)`; `if _,e:=unmarshalConfigYAML(raw);e!=nil {reply ok:false,error}`; `os.WriteFile(s.cfgPath, raw, 0600)`; reply `{ok:true}`.
- [ ] 두 케이스 추가(+타입 주석). 테스트: get_settings reply 유효 JSON, set_settings 유효 updates→ok + 파일 반영, 잘못된 값(검증 실패)→ok:false.
- [ ] `go build/vet/test` 그린. 커밋.

## Task 3: aglink-chat /api/settings 프록시 + /setting 서빙
**Files:** `../aglink-chat/server.go`
- [ ] `handleSettings`: GET→`control.request(get_settings)` 프록시(JSON); PUT→body를 `set_settings`(Body)로, reply ok→204, 아니면 400+error. authOK 필수.
- [ ] mux: `/api/settings` 등록. `/setting`(및 `/setting/`)→`handleIndex` 서빙(SPA fallback) — handleIndex의 `r.URL.Path != "/"` 가드를 `/`·`/setting` 허용으로 확장.
- [ ] `go build/vet` 그린. 커밋(aglink-chat).

## Task 4: 프론트 /setting 뷰 + 라우팅 (index.html/app.js/style.css)
**Files:** `../aglink-chat/web/{index.html,app.js,style.css}`
- [ ] index.html: `#config-overlay`/`#conn-overlay` 제거. `#settings-view`(hidden) 추가 — topbar(← 채팅, 제목, 탭버튼 2개) + `#settings-tab-config`(구조화 폼 컨테이너 + 저장 + 접이식 raw: `#config-text`/`#config-save`/`#config-msg`) + `#settings-tab-conn`(`#conn-body`).
- [ ] app.js 라우터: `renderRoute()`(pathname==="/setting"→ `#shell` 숨김+`#settings-view` 표시+현재 탭 로드; 아니면 반대), `navigate(path,tab)`=pushState+renderRoute, `window.onpopstate=renderRoute`. 초기 호출.
- [ ] 헤더 핸들러: btnConfig→`navigate('/setting','config')`, btnConnections→`navigate('/setting','conn')`. openConfig/saveConfig/openConnections는 뷰 내부 로직으로 재사용(오버레이 hidden 토글 → 뷰/탭 표시로 대체).
- [ ] 구조화 폼: `loadSettingsForm()` = `GET /api/settings` → 섹션/필드 렌더(label+desc+input by type; 변경 추적) → `saveSettings()` = 변경분만 `PUT /api/settings`(204→"저장됨", else 오류). 저장 후 폼 새로고침.
- [ ] 연결 탭: 기존 openConnections 본문을 `#conn-body`에 렌더(팝업 대신 탭).
- [ ] `node --check` 통과.

## Task 5: 검증 + 배포
- [ ] teleclaude `go build/vet/test` 그린, aglink-chat `go build/vet` 그린, gofmt 클린, `node --check`.
- [ ] 두 저장소 커밋 → `!update`(control API 17170 트리거) 배포.
- [ ] 라이브 확인: `/setting` 접속(탭 전환), `GET /api/settings` 스키마, 구조화 저장 1건 반영(예: max_workers), raw 편집 저장, 연결 탭 상태. 배포는 사용자 브라우저 확인.

## Self-Review (스펙 대비)
- SPA /setting 라우팅 → Task 3,4. ✓
- 서버 구조화 API → Task 1,2,3. ✓
- 안전 부분집합 + raw → Task 1(필드) + Task 4(raw 접이식). ✓
- 팝업 제거 → Task 4. ✓
- 보안(화이트리스트/검증/고정경로/비밀 미노출) → Task 1,2 + Global. ✓
