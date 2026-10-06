; aglink-screen installer (per-user).
;
; Built by installer\build-product-setup.ps1 -Product screen, which defines
; VERSION, STAGE and OUTFILE.
;
; aglink-screen runs as a stdio MCP server that Claude Code starts on demand,
; so installing it is: put the exe in place and register it. Nothing runs at
; logon.

Unicode true
!include "MUI2.nsh"
!include "common.nsh"

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef STAGE
  !error "STAGE must be defined (run build-product-setup.ps1)"
!endif
!ifndef OUTFILE
  !define OUTFILE "..\aglink-screen-Setup.exe"
!endif

!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\aglink-screen"

Name "aglink-screen ${VERSION}"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\aglink-screen"
InstallDirRegKey HKCU "${UNINST_KEY}" "InstallLocation"
RequestExecutionLevel user

!define MUI_ICON "${NSISDIR}\Contrib\Graphics\Icons\modern-install.ico"
!define MUI_UNICON "${NSISDIR}\Contrib\Graphics\Icons\modern-uninstall.ico"
!define MUI_ABORTWARNING
!define MUI_WELCOMEPAGE_TITLE "aglink-screen ${VERSION} 설치"
!define MUI_WELCOMEPAGE_TEXT "aglink-screen 은 Claude 가 윈도우 화면을 직접 조작하게 해 주는 도구입니다 — 창 목록, 접근성 트리로 읽기, 클릭·입력, 캡처.$\r$\n$\r$\n웹 페이지·Electron 앱·VS Code 창은 aglink-web 이 더 가볍습니다. aglink-screen 은 그 밖의 네이티브 앱용입니다.$\r$\n$\r$\n관리자 권한 없이 사용자 폴더에 설치됩니다. 이미 설치돼 있으면 새 버전으로 바꿉니다 — 이때 aglink-screen 을 쓰던 Claude 세션은 한 번 다시 시작해야 도구가 다시 붙습니다."
!define MUI_FINISHPAGE_TEXT "설치가 끝났습니다.$\r$\n$\r$\nClaude Code 세션을 새로 열면 aglink-screen 도구가 붙습니다."
!define MUI_FINISHPAGE_SHOWREADME "$INSTDIR\guide.html"
!define MUI_FINISHPAGE_SHOWREADME_TEXT "사용 안내 열기"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "Korean"

VIProductVersion "${VERSION}.0"
VIAddVersionKey /LANG=${LANG_KOREAN} "ProductName" "aglink-screen"
VIAddVersionKey /LANG=${LANG_KOREAN} "CompanyName" "aglink"
VIAddVersionKey /LANG=${LANG_KOREAN} "LegalCopyright" "aglink"
VIAddVersionKey /LANG=${LANG_KOREAN} "FileDescription" "aglink-screen Setup"
VIAddVersionKey /LANG=${LANG_KOREAN} "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=${LANG_KOREAN} "ProductVersion" "${VERSION}"

Function .onInit
    !insertmacro ReadOptions
FunctionEnd

Section "Install"
    SetOutPath "$INSTDIR"

    DetailPrint "실행 중인 aglink-screen 정리 (이 설치 폴더의 것만)..."
    !insertmacro StopFromInstDir "aglink-screen"

    DetailPrint "파일 설치..."
    File "${STAGE}\aglink-screen.exe"
    File "${STAGE}\guide.html"

    StrCpy $McpEnvArgs ""
    !insertmacro McpEnvSetting "aglink-screen" "AGLINK_SCREEN_STEP_DELAY_MS" $OptStepDelay $HasStepDelay
    !insertmacro RegisterClaudeMCP "aglink-screen" "$INSTDIR\aglink-screen.exe"

    CreateDirectory "$SMPROGRAMS\aglink-screen"
    CreateShortcut "$SMPROGRAMS\aglink-screen\aglink-screen 사용 안내.lnk" "$INSTDIR\guide.html"
    CreateShortcut "$SMPROGRAMS\aglink-screen\aglink-screen 제거.lnk" "$INSTDIR\uninstall.exe"

    !insertmacro WriteUninstallInfo "${UNINST_KEY}" "aglink-screen" "${VERSION}" "$INSTDIR\aglink-screen.exe"
    DetailPrint "설치 완료."
SectionEnd

Section "Uninstall"
    DetailPrint "실행 중인 aglink-screen 종료..."
    !insertmacro StopFromInstDir "aglink-screen"
    !insertmacro UnregisterClaudeMCP "aglink-screen"
    DeleteRegKey HKCU "Software\aglink\mcp-env\aglink-screen"
    Delete "$INSTDIR\aglink-screen.exe"
    Delete "$INSTDIR\guide.html"
    Delete "$INSTDIR\uninstall.exe"
    RMDir "$INSTDIR"
    RMDir /r "$SMPROGRAMS\aglink-screen"
    DeleteRegKey HKCU "${UNINST_KEY}"
    DetailPrint "제거 완료 (사용자 설정 %USERPROFILE%\.aglink 는 남겨 둡니다)."
SectionEnd
