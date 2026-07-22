; aglink Installer (NSIS)
; Installs aglink as a login-time auto-start (elevated Scheduled Task), not a
; Session-0 service, because aglink drives the interactive desktop (screen/web
; control) which needs the user session + a High-integrity token.

!include "MUI2.nsh"
!include "LogicLib.nsh"

; ===== Product Info =====
!define PRODUCT_NAME "aglink"
!define PRODUCT_PUBLISHER "aglink"
!define PRODUCT_EXE "aglink.exe"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${PRODUCT_NAME}"

Name "${PRODUCT_NAME}"
OutFile "..\aglink-Setup.exe"
InstallDir "$PROGRAMFILES64\aglink"
InstallDirRegKey HKLM "${UNINST_KEY}" "InstallLocation"
RequestExecutionLevel admin
Unicode true

; ===== MUI =====
!define MUI_ICON "${NSISDIR}\Contrib\Graphics\Icons\modern-install.ico"
!define MUI_UNICON "${NSISDIR}\Contrib\Graphics\Icons\modern-uninstall.ico"
!define MUI_ABORTWARNING
!define MUI_WELCOMEPAGE_TITLE "aglink 설치"
!define MUI_WELCOMEPAGE_TEXT "aglink는 Telegram·웹에서 claude / codex / opencode 를 구동하는 AI 게이트웨이입니다.$\r$\n$\r$\n※ 사전 준비: 이 프로그램은 이미 설치된 claude, codex, opencode CLI 를 사용합니다. 설치 전에 사용할 CLI가 준비돼 있어야 합니다(없어도 설치는 되지만 해당 백엔드는 동작하지 않습니다).$\r$\n$\r$\n설치 후 로그온 시 관리자 권한으로 자동 실행됩니다(화면제어용)."
!define MUI_FINISHPAGE_TEXT "aglink 설치가 완료되었습니다.$\r$\n$\r$\n로그온 시 자동 시작되도록 등록되었습니다. 지금 바로 시작되었습니다.$\r$\n설정은 웹 UI(http://127.0.0.1:27271) 또는 데스크톱 앱에서 하세요."
!define MUI_FINISHPAGE_RUN "$INSTDIR\aglink-desktop.exe"
!define MUI_FINISHPAGE_RUN_TEXT "aglink 데스크톱 앱 실행"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "Korean"

VIProductVersion "1.0.0.0"
VIAddVersionKey /LANG=${LANG_KOREAN} "ProductName" "${PRODUCT_NAME}"
VIAddVersionKey /LANG=${LANG_KOREAN} "CompanyName" "${PRODUCT_PUBLISHER}"
VIAddVersionKey /LANG=${LANG_KOREAN} "FileDescription" "aglink Installer"
VIAddVersionKey /LANG=${LANG_KOREAN} "FileVersion" "1.0.0.0"

; ===== Install =====
Section "Install"
    SetOutPath "$INSTDIR"

    ; --- Stop any previous auto-start + running instance (idempotent re-install) ---
    DetailPrint "기존 aglink 정리..."
    nsExec::ExecToLog '"$INSTDIR\${PRODUCT_EXE}" uninstall'
    nsExec::ExecToLog 'taskkill /F /T /IM aglink.exe'
    nsExec::ExecToLog 'taskkill /F /T /IM aglink-chat.exe'
    nsExec::ExecToLog 'taskkill /F /T /IM aglink-desktop.exe'
    Sleep 1200

    ; --- Files (all binaries flattened into one dir; aglink resolves helpers as siblings) ---
    DetailPrint "파일 설치..."
    File "stage\aglink.exe"
    File "stage\aglink-chat.exe"
    File "stage\aglink-desktop.exe"
    File "stage\aglink-screen.exe"
    File "stage\aglink-web.exe"

    ; --- Dependency check (informational; install proceeds either way) ---
    DetailPrint "외부 CLI 확인 (claude/codex/opencode)..."
    nsExec::ExecToStack 'where claude'
    Pop $0
    ${If} $0 != "0"
        DetailPrint "  [경고] claude CLI 미발견 — claude 백엔드 사용 시 먼저 설치하세요."
    ${EndIf}
    nsExec::ExecToStack 'where codex'
    Pop $0
    ${If} $0 != "0"
        DetailPrint "  [경고] codex CLI 미발견 — codex 백엔드 사용 시 먼저 설치하세요."
    ${EndIf}
    nsExec::ExecToStack 'where opencode'
    Pop $0
    ${If} $0 != "0"
        DetailPrint "  [경고] opencode CLI 미발견 — opencode 백엔드 사용 시 먼저 설치하세요."
    ${EndIf}

    ; --- Register login auto-start task (elevated, user session) ---
    DetailPrint "로그온 자동시작 작업 등록..."
    nsExec::ExecToLog '"$INSTDIR\${PRODUCT_EXE}" install'
    Pop $0
    ${If} $0 != "0"
        DetailPrint "  [경고] 자동시작 작업 등록 반환코드 $0"
    ${EndIf}

    ; --- Start now (no logout needed) ---
    DetailPrint "지금 시작..."
    nsExec::ExecToLog '"$INSTDIR\${PRODUCT_EXE}" start'

    ; --- Desktop shortcut (launch the desktop app directly) ---
    CreateShortcut "$DESKTOP\aglink.lnk" "$INSTDIR\aglink-desktop.exe" "" "$INSTDIR\aglink-desktop.exe" 0

    ; --- Start Menu shortcuts ---
    CreateDirectory "$SMPROGRAMS\aglink"
    CreateShortcut "$SMPROGRAMS\aglink\aglink 데스크톱.lnk" "$INSTDIR\aglink-desktop.exe"
    CreateShortcut "$SMPROGRAMS\aglink\aglink 웹 UI.lnk" "http://127.0.0.1:27271"
    CreateShortcut "$SMPROGRAMS\aglink\제거.lnk" "$INSTDIR\uninstall.exe"

    ; --- Uninstaller + Add/Remove Programs ---
    WriteUninstaller "$INSTDIR\uninstall.exe"
    WriteRegStr HKLM "${UNINST_KEY}" "DisplayName" "${PRODUCT_NAME}"
    WriteRegStr HKLM "${UNINST_KEY}" "DisplayVersion" "1.0.0"
    WriteRegStr HKLM "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
    WriteRegStr HKLM "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
    WriteRegStr HKLM "${UNINST_KEY}" "Publisher" "${PRODUCT_PUBLISHER}"
    WriteRegDWORD HKLM "${UNINST_KEY}" "NoModify" 1
    WriteRegDWORD HKLM "${UNINST_KEY}" "NoRepair" 1

    DetailPrint "설치 완료!"
SectionEnd

; ===== Uninstall =====
Section "Uninstall"
    DetailPrint "자동시작 작업 제거..."
    nsExec::ExecToLog '"$INSTDIR\${PRODUCT_EXE}" uninstall'
    Sleep 800
    DetailPrint "실행 중 프로세스 종료..."
    nsExec::ExecToLog 'taskkill /F /T /IM aglink.exe'
    nsExec::ExecToLog 'taskkill /F /T /IM aglink-chat.exe'
    nsExec::ExecToLog 'taskkill /F /T /IM aglink-desktop.exe'
    nsExec::ExecToLog 'taskkill /F /T /IM aglink-screen.exe'
    nsExec::ExecToLog 'taskkill /F /T /IM aglink-web.exe'
    Sleep 1000

    DetailPrint "파일 삭제..."
    Delete "$INSTDIR\aglink.exe"
    Delete "$INSTDIR\aglink-chat.exe"
    Delete "$INSTDIR\aglink-desktop.exe"
    Delete "$INSTDIR\aglink-screen.exe"
    Delete "$INSTDIR\aglink-web.exe"
    Delete "$INSTDIR\uninstall.exe"
    RMDir "$INSTDIR"

    Delete "$DESKTOP\aglink.lnk"
    Delete "$SMPROGRAMS\aglink\aglink 데스크톱.lnk"
    Delete "$SMPROGRAMS\aglink\aglink 웹 UI.lnk"
    Delete "$SMPROGRAMS\aglink\제거.lnk"
    RMDir "$SMPROGRAMS\aglink"

    DeleteRegKey HKLM "${UNINST_KEY}"
    DetailPrint "제거 완료 (사용자 설정 %USERPROFILE%\.aglink 는 보존)."
SectionEnd
