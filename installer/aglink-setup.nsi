; aglink Installer (NSIS)
; Installs aglink as a login-time auto-start (elevated Scheduled Task), not a
; Session-0 service, because aglink drives the interactive desktop (screen/web
; control) which needs the user session + a High-integrity token.

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "nmhost.nsh"

; ===== Product Info =====
!define PRODUCT_NAME "aglink"
!define PRODUCT_PUBLISHER "aglink"
!define PRODUCT_EXE "aglink.exe"
; build-installer.ps1 passes /DVERSION=1.2.<patch>, the same number aglink reports.
!ifndef VERSION
  !define VERSION "1.2.0"
!endif
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
!define MUI_FINISHPAGE_TEXT "aglink 설치가 완료되었습니다.$\r$\n$\r$\n로그온 시 자동 시작되도록 등록되었습니다. 지금 바로 시작되었습니다.$\r$\n처음 설치라면 설정 파일을 만들어야 시작됩니다. Claude Code 에게 $\"$INSTDIR\AI-SETUP.md 대로 aglink 설정해줘$\" 라고 하면 AI 가 설정해 줍니다.$\r$\n직접 하려면 터미널에서 aglink.exe setup 을 실행하세요."
; MUI_FINISHPAGE_RUN enables the "run app" checkbox; MUI_FINISHPAGE_RUN_FUNCTION
; (when set) takes over what actually happens on Finish instead of MUI2's
; default Exec of the MUI_FINISHPAGE_RUN path directly. That default would run
; from this installer's own elevated process (RequestExecutionLevel admin), so
; the app would start de-facto elevated; the custom function below routes it
; through explorer.exe like the silent-install relaunch so it runs as the
; signed-in user instead. MUI_FINISHPAGE_RUN's value itself is unused once the
; function is set, but MUI2.nsh requires it to be non-empty.
!define MUI_FINISHPAGE_RUN "$INSTDIR\aglink-desktop.exe"
!define MUI_FINISHPAGE_RUN_FUNCTION "RunDesktopAppUnelevated"
!define MUI_FINISHPAGE_RUN_TEXT "aglink 데스크톱 앱 실행"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "Korean"

VIProductVersion "${VERSION}.0"
VIAddVersionKey /LANG=${LANG_KOREAN} "ProductName" "${PRODUCT_NAME}"
VIAddVersionKey /LANG=${LANG_KOREAN} "CompanyName" "${PRODUCT_PUBLISHER}"
VIAddVersionKey /LANG=${LANG_KOREAN} "FileDescription" "aglink Installer"
VIAddVersionKey /LANG=${LANG_KOREAN} "FileVersion" "${VERSION}.0"

; Chrome relaunches the aglink-web native host as soon as it is killed, so the
; exe can be locked again by the time File runs. A running exe can still be
; renamed, so the old one is moved aside and the new one is copied in fresh.
!macro MoveAside NAME
    Delete "$INSTDIR\${NAME}.old.exe"
    Delete "$INSTDIR\${NAME}.old2.exe"
    ClearErrors
    Rename "$INSTDIR\${NAME}.exe" "$INSTDIR\${NAME}.old.exe"
    ${If} ${Errors}
        Rename "$INSTDIR\${NAME}.exe" "$INSTDIR\${NAME}.old2.exe"
    ${EndIf}
!macroend

; Whether the desktop app was open when the install began: it is closed to be
; replaced, and a silent install (an agent running the setup, an update) has
; no finish page to start it again, so it is brought back afterwards.
Var DesktopWasRunning

; Finish-page "run app" handler (MUI_FINISHPAGE_RUN_FUNCTION). This installer
; runs elevated (RequestExecutionLevel admin), so a plain Exec here would
; start the desktop app elevated too; route it through explorer.exe like the
; silent-install relaunch so it runs as the signed-in user instead.
Function RunDesktopAppUnelevated
    Exec '"$WINDIR\explorer.exe" "$INSTDIR\aglink-desktop.exe"'
FunctionEnd

; ===== Install =====
Section "Install"
    SetOutPath "$INSTDIR"
    StrCpy $DesktopWasRunning "0"
    nsExec::ExecToStack 'cmd /c tasklist /FI "IMAGENAME eq aglink-desktop.exe" /NH | find /I "aglink-desktop.exe"'
    Pop $0
    Pop $1
    ${If} $0 == "0"
        StrCpy $DesktopWasRunning "1"
    ${EndIf}

    ; --- Stop any previous auto-start + running instance (idempotent re-install) ---
    DetailPrint "기존 aglink 정리..."
    nsExec::ExecToLog '"$INSTDIR\${PRODUCT_EXE}" uninstall'
    ; The host is stopped without /T: its children are the agent workers, and
    ; when one of them runs this setup, the setup is in that tree — /T killed
    ; the setup itself right after the uninstall above, leaving no task and
    ; no aglink. Its own helpers are stopped by name (here and below).
    nsExec::ExecToLog 'taskkill /F /IM aglink.exe'
    nsExec::ExecToLog 'taskkill /F /T /IM aglink-chat.exe'
    nsExec::ExecToLog 'taskkill /F /T /IM aglink-desktop.exe'
    nsExec::ExecToLog 'taskkill /F /IM teleclaude.exe'
    nsExec::ExecToLog 'taskkill /F /IM teleclaude_new.exe'
    ; aglink-screen / aglink-web servers started from this folder (by the host, or
    ; on demand by aglink-vscode's relay for Remote-SSH windows) hold their exe
    ; open, so the File step below could not replace them. Only copies running
    ; from $INSTDIR itself are stopped (the exe's folder compared exactly, so a
    ; sibling like aglink-dev is not matched): a build elsewhere is left alone.
    ; The folder goes in through the environment, not the script text, so a
    ; path with a quote in it (C:\Users\O'Brien\...) cannot break the command.
    System::Call 'Kernel32::SetEnvironmentVariable(t "AGLINK_SETUP_DIR", t "$INSTDIR")i'
    ; Looked up through CIM: this installer is 32-bit, so its powershell is too,
    ; and a 32-bit Get-Process cannot read a 64-bit process's path (it came back
    ; empty, nothing matched, and the File step below left both exes old).
    nsExec::ExecToLog `powershell -NoProfile -ExecutionPolicy Bypass -Command "$$d = [IO.Path]::GetFullPath($$env:AGLINK_SETUP_DIR).TrimEnd('\'); Get-CimInstance Win32_Process | Where-Object { ($$_.Name -eq 'aglink-screen.exe' -or $$_.Name -eq 'aglink-web.exe') -and $$_.ExecutablePath -and ([IO.Path]::GetDirectoryName($$_.ExecutablePath).TrimEnd('\') -ieq $$d) } | ForEach-Object { Stop-Process -Id $$_.ProcessId -Force }"`
    Sleep 1200

    ; --- Files (all binaries flattened into one dir; aglink resolves helpers as siblings) ---
    DetailPrint "파일 설치..."
    File "stage\aglink.exe"
    File "stage\aglink-chat.exe"
    File "stage\aglink-desktop.exe"
    !insertmacro MoveAside "aglink-screen"
    !insertmacro MoveAside "aglink-web"
    File "stage\aglink-screen.exe"
    File "stage\aglink-web.exe"
    ; Chrome (or a Claude session's MCP client) can relaunch aglink-web from the
    ; old exe in the moment between the stop above and MoveAside; that copy then
    ; keeps serving the old daemon, renamed to .old.exe, until the next reboot.
    ; Stop whatever still runs from this folder (any aglink-web*/aglink-screen*
    ; image) now that the new exes are in place, so the next launch is new.
    nsExec::ExecToLog `powershell -NoProfile -ExecutionPolicy Bypass -Command "$$d = [IO.Path]::GetFullPath($$env:AGLINK_SETUP_DIR).TrimEnd('\'); Get-CimInstance Win32_Process | Where-Object { ($$_.Name -like 'aglink-screen*.exe' -or $$_.Name -like 'aglink-web*.exe') -and $$_.ExecutablePath -and ([IO.Path]::GetDirectoryName($$_.ExecutablePath).TrimEnd('\') -ieq $$d) } | ForEach-Object { Stop-Process -Id $$_.ProcessId -Force }"`
    File "stage\AI-SETUP.md"
    File "stage\rnnoise.dll"
    RMDir /r "$INSTDIR\chrome-extension"
    SetOutPath "$INSTDIR\chrome-extension"
    File /r "stage\chrome-extension\*.*"
    SetOutPath "$INSTDIR"
    ; Machine-wide install, so the host is registered for every user (HKLM).
    !insertmacro WriteNMHost HKLM

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

    ; --- Bring the desktop app back (silent install only; the finish page offers it otherwise) ---
    ; Started through explorer so it runs as the signed-in user, not elevated
    ; like this installer.
    ${If} ${Silent}
    ${AndIf} $DesktopWasRunning == "1"
        DetailPrint "데스크톱 앱 다시 실행..."
        Exec '"$WINDIR\explorer.exe" "$INSTDIR\aglink-desktop.exe"'
    ${EndIf}

    ; --- Desktop shortcut (launch the desktop app directly) ---
    CreateShortcut "$DESKTOP\aglink.lnk" "$INSTDIR\aglink-desktop.exe" "" "$INSTDIR\aglink-desktop.exe" 0

    ; --- Start Menu shortcuts ---
    CreateDirectory "$SMPROGRAMS\aglink"
    CreateShortcut "$SMPROGRAMS\aglink\aglink 데스크톱.lnk" "$INSTDIR\aglink-desktop.exe"
    ; A .lnk cannot point at a URL (it came out with no target); use a .url.
    Delete "$SMPROGRAMS\aglink\aglink 웹 UI.lnk"
    WriteINIStr "$SMPROGRAMS\aglink\aglink 웹 UI.url" "InternetShortcut" "URL" "http://127.0.0.1:27271"
    CreateShortcut "$SMPROGRAMS\aglink\설치·설정 가이드 (AI용).lnk" "$INSTDIR\AI-SETUP.md"
    CreateShortcut "$SMPROGRAMS\aglink\크롬 확장 폴더 열기.lnk" "$INSTDIR\chrome-extension"
    CreateShortcut "$SMPROGRAMS\aglink\제거.lnk" "$INSTDIR\uninstall.exe"

    ; --- Uninstaller + Add/Remove Programs ---
    WriteUninstaller "$INSTDIR\uninstall.exe"
    WriteRegStr HKLM "${UNINST_KEY}" "DisplayName" "${PRODUCT_NAME}"
    WriteRegStr HKLM "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
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
    nsExec::ExecToLog 'taskkill /F /IM aglink.exe' ; no /T: an agent worker may be running this uninstaller (see Install)
    nsExec::ExecToLog 'taskkill /F /T /IM aglink-chat.exe'
    nsExec::ExecToLog 'taskkill /F /T /IM aglink-desktop.exe'
    nsExec::ExecToLog 'taskkill /F /T /IM aglink-screen.exe'
    nsExec::ExecToLog 'taskkill /F /T /IM aglink-web.exe'
    nsExec::ExecToLog 'taskkill /F /IM teleclaude.exe'
    nsExec::ExecToLog 'taskkill /F /IM teleclaude_new.exe'
    Sleep 1000

    DetailPrint "파일 삭제..."
    Delete "$INSTDIR\aglink.exe"
    Delete "$INSTDIR\aglink-chat.exe"
    Delete "$INSTDIR\aglink-desktop.exe"
    Delete "$INSTDIR\aglink-screen.exe"
    Delete "$INSTDIR\aglink-web.exe"
    Delete /REBOOTOK "$INSTDIR\aglink-web.old.exe"
    Delete /REBOOTOK "$INSTDIR\aglink-web.old2.exe"
    Delete /REBOOTOK "$INSTDIR\aglink-screen.old.exe"
    Delete /REBOOTOK "$INSTDIR\aglink-screen.old2.exe"
    Delete "$INSTDIR\AI-SETUP.md"
    Delete "$INSTDIR\rnnoise.dll"
    !insertmacro RemoveNMHost HKLM
    RMDir /r "$INSTDIR\chrome-extension"
    Delete "$INSTDIR\uninstall.exe"
    RMDir "$INSTDIR"

    Delete "$DESKTOP\aglink.lnk"
    Delete "$SMPROGRAMS\aglink\aglink 데스크톱.lnk"
    Delete "$SMPROGRAMS\aglink\aglink 웹 UI.lnk"
    Delete "$SMPROGRAMS\aglink\aglink 웹 UI.url"
    Delete "$SMPROGRAMS\aglink\설치·설정 가이드 (AI용).lnk"
    Delete "$SMPROGRAMS\aglink\크롬 확장 폴더 열기.lnk"
    Delete "$SMPROGRAMS\aglink\제거.lnk"
    RMDir "$SMPROGRAMS\aglink"

    DeleteRegKey HKLM "${UNINST_KEY}"
    DetailPrint "제거 완료 (사용자 설정 %USERPROFILE%\.aglink 는 보존)."
SectionEnd
