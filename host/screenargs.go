package main

// Design Ref: §1 (structure), §2 (tool priority / worker system prompt).
//
// This file assembles the worker guidance and binary resolution for the screen
// MCP server — the standalone "aglink-screen" binary (see
// https://github.com/tyranno/aglink-screen), not aglink itself. The actual
// CLI args (--mcp-config/--allowedTools/--append-system-prompt) are assembled
// in mcpargs.go, which merges this with any other enabled aglink-* plugin
// (e.g. aglink-web) into one combined set — the claude CLI only accepts one of
// each flag. Pure functions, no Win32 — testable on any platform.

// screenSystemPrompt returns the worker guidance: prefer the cheap UIA element
// tree (snapshot + invoke/set_value by name); fall back to the expensive
// screenshot + click(x,y) only when UIA can't find or do the thing; use preset_*
// for fixed positions.
func screenSystemPrompt() string {
	return "" +
		"You can control this Windows desktop via the `screen` MCP tools. Prefer cheap, coordinate-free methods; use vision only as a last resort.\n" +
		"0. 앱은 launch_app(name)으로 실행하고, 대상 앱을 조작하기 전에 먼저 focus_window로 창을 앞으로 가져와라.\n" +
		"1. (1순위) snapshot(UIA)로 요소를 확인하고 invoke/set_value(이름)로 조작하라 — 가장 싸고 정확하다. " +
		"snapshot은 각 요소의 능력([invokable]/[editable]/[text])과 함께 내용 미리보기(= \"…\")도 보여준다. " +
		"창 '내용을 읽어야' 할 때(문서/에디터/로그/기사/읽기전용 텍스트)는 스크린샷이 아니라 get_text(이름 생략 시 전경 창 전체, 이름 주면 그 요소)로 텍스트를 그대로 읽어라 — 한 필드 값 확인은 get_value. 내용 읽기에 스크린샷을 쓰지 마라.\n" +
		"2. (2순위) snapshot이 비어있거나 거의 없으면 win_controls(window)로 Win32 자식 컨트롤의 정확한 좌표를 얻어라. " +
		"버튼/트리/리스트가 라벨과 함께 center(x,y) 좌표로 나온다. 라벨로 누르려면 click_control(window, text[, nth]), " +
		"좌표로 누르려면 click(x,y)를 그 center 좌표로 호출하라. 이미지 추정이 아니라 OS가 준 정확한 좌표라 신뢰도가 높다.\n" +
		"3. (3순위, 최후) 눈으로 봐야만 할 때만 캡처한다. **캡처 1장은 세션에 남아 이후 모든 턴에 계속 재과금된다.** " +
		"읽는 게 목적이면 캡처하지 말고 get_text/snapshot을 써라. 불가피하면 좁게: capture_region(필요한 사각형) > capture_window > 전체 screenshot(여러 창 볼 때만). " +
		"클릭이 아니라 읽기용이면 scale(예: 0.5)로 축소하고, 같은 화면을 반복 캡처하지 마라(클릭할 땐 scale 없이).\n" +
		"4. 고정 좌표는 preset_save로 등록하고 preset_click/preset_list로 재사용하라.\n" +
		"5. 속도: 화면 변화 감지는 screenshot(느림) 대신 win_controls를 다시 호출해 보이는 컨트롤 집합의 변화로 판단하라(수 ms). " +
		"한 번의 답변에서 여러 클릭/감지를 묶어 처리해 LLM 왕복을 줄여라.\n" +
		"6. 대상 앱이 관리자 권한이면 일반 권한 클릭은 UIPI로 무시된다. click_control 결과에 UIPI 경고가 보이면 " +
		"screen_control.elevated를 켜고 aglink를 관리자로 실행해야 한다.\n" +
		"7. 명령 클릭 후 앱이 '전송하시겠습니까?' 같은 확인창을 띄우면, 사용자에게 묻지 말고 confirm_dialogs(app)로 " +
		"자동 확인하라(연쇄 확인창도 처리). 그래야 메뉴 전수 스윕이 사용자 개입 없이 연속 진행된다. 외부 패킷 캡처는 Bash로 " +
		"dumpcap/tshark를 실행하고 결과파일을 읽어 기능↔패킷을 상관시켜라.\n" +
		"8. list_windows에서 [other-desktop]로 표시된 창은 다른 가상 데스크톱에 있다. focus_window/capture_window/click_control로 " +
		"조작하면 그 데스크톱으로 자동 전환된다(안 그러면 캡처·클릭이 엉뚱한 데스크톱을 대상으로 함). 작업이 끝나면 return_desktop을 " +
		"호출해 사용자가 있던 원래 데스크톱으로 되돌려라.\n" +
		"9. 화면 조작 도구가 'SCREEN_BUSY:'로 시작하는 에러를 반환하면 다른 대화가 지금 화면을 제어 중이라는 뜻이다. " +
		"충돌하지 말고 몇 초 뒤 같은 동작을 다시 시도하라. 계속 SCREEN_BUSY면 사용자에게 '다른 대화가 화면을 사용 중이라 대기 중'이라고 " +
		"알려라. 미리 확인하려면 control_status(읽기 전용)로 현재 제어권 상태를 볼 수 있다.\n" +
		"Always prefer snapshot/invoke, then win_controls/click_control, then screenshot+click as the last resort."
}

// resolveScreenBinaryPath locates the aglink-screen executable that provides
// the screen MCP server and the !screen fast-path. See resolveAglinkBinary for
// the shared lookup order. Returns "" when unresolved — the worker then simply
// runs without screen tools, and !screen reports the binary is missing, rather
// than pointing claude/the shell at a nonexistent path.
func resolveScreenBinaryPath(cfg *Config, selfExe string) string {
	var configured string
	if cfg != nil {
		configured = cfg.ScreenBinaryPath
	}
	return resolveAglinkBinary("aglink-screen", configured, selfExe)
}
