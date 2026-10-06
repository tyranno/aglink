; aglink-web installer (per-user).
;
; Built by installer\build-product-setup.ps1 -Product web, which defines
; VERSION, STAGE (the staged files) and OUTFILE.
;
; Installs the aglink-web daemon, the Chrome extension folder and the
; aglink-vscode extension; registers the MCP server with Claude Code; starts the
; daemon now and at every logon. The Chrome extension itself has to be loaded by
; hand (Chrome only installs store extensions on its own) — the finish page
; opens the guide that walks through it.

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
  !define OUTFILE "..\aglink-web-Setup.exe"
!endif

!define PRODUCT "aglink-web"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\aglink-web"
!define RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"

Name "aglink-web ${VERSION}"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\aglink-web"
InstallDirRegKey HKCU "${UNINST_KEY}" "InstallLocation"
RequestExecutionLevel user

!define MUI_ICON "${NSISDIR}\Contrib\Graphics\Icons\modern-install.ico"
!define MUI_UNICON "${NSISDIR}\Contrib\Graphics\Icons\modern-uninstall.ico"
!define MUI_ABORTWARNING
!define MUI_WELCOMEPAGE_TITLE "aglink-web ${VERSION} 설치"
!define MUI_WELCOMEPAGE_TEXT "aglink-web 은 Claude 가 브라우저와 VS Code 창을 텍스트로 읽고 조작하게 해 주는 도구입니다.$\r$\n$\r$\n• 크롬 탭 읽기·클릭·입력$\r$\n• Electron·Wails 앱 화면 조작$\r$\n• 다른 VS Code 창(Remote-SSH 포함)의 파일·터미널·문제 목록$\r$\n$\r$\n관리자 권한 없이 사용자 폴더에 설치됩니다. 이미 설치돼 있으면 새 버전으로 바꿉니다 — 이때 aglink-web 을 쓰던 Claude 세션은 한 번 다시 시작해야 도구가 다시 붙습니다."
!define MUI_FINISHPAGE_TEXT "설치가 끝났습니다.$\r$\n$\r$\n남은 일 하나 — 크롬 확장은 직접 불러와야 합니다. 아래 안내를 열어 2단계만 따라 하세요.$\r$\n$\r$\n이미 크롬 확장을 쓰고 계셨다면 chrome://extensions 에서 aglink-web 의 새로고침(↻)을 한 번 눌러 주세요."
!define MUI_FINISHPAGE_SHOWREADME "$INSTDIR\guide.html"
!define MUI_FINISHPAGE_SHOWREADME_TEXT "사용 안내 열기 (크롬 확장 불러오기)"
!define MUI_FINISHPAGE_SHOWREADME_CHECKED

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "Korean"

VIProductVersion "${VERSION}.0"
VIAddVersionKey /LANG=${LANG_KOREAN} "ProductName" "aglink-web"
VIAddVersionKey /LANG=${LANG_KOREAN} "CompanyName" "aglink"
VIAddVersionKey /LANG=${LANG_KOREAN} "LegalCopyright" "aglink"
VIAddVersionKey /LANG=${LANG_KOREAN} "FileDescription" "aglink-web Setup"
VIAddVersionKey /LANG=${LANG_KOREAN} "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=${LANG_KOREAN} "ProductVersion" "${VERSION}"

Function .onInit
    !insertmacro ReadOptions
FunctionEnd

Section "Install"
    SetOutPath "$INSTDIR"

    DetailPrint "실행 중인 aglink-web 정리 (이 설치 폴더의 것만)..."
    !insertmacro StopFromInstDir "aglink-web"

    DetailPrint "파일 설치..."
    File "${STAGE}\aglink-web.exe"
    File "${STAGE}\start-web-daemon.vbs"
    File "${STAGE}\aglink-vscode.vsix"
    File "${STAGE}\guide.html"
    ; Replace the extension folder wholesale so a file dropped in a later version
    ; does not linger. The folder name stays the same, which is what lets Chrome
    ; pick the new code up with a plain reload.
    RMDir /r "$INSTDIR\chrome-extension"
    SetOutPath "$INSTDIR\chrome-extension"
    File /r "${STAGE}\chrome-extension\*.*"
    SetOutPath "$INSTDIR"

    StrCpy $McpEnvArgs ""
    !insertmacro McpEnvSetting "aglink-web" "AGLINK_WEB_STEP_DELAY_MS" $OptStepDelay $HasStepDelay
    !insertmacro McpEnvSetting "aglink-web" "AGLINK_WEB_CDP_PORTS" $OptCdpPorts $HasCdpPorts
    !insertmacro McpEnvSetting "aglink-web" "AGLINK_WEB_INSECURE_HOSTS" $OptInsecureHosts $HasInsecureHosts
    !insertmacro RegisterClaudeMCP "aglink-web" "$INSTDIR\aglink-web.exe"

    ${If} $OptNoVSCode == "1"
        DetailPrint "VS Code 확장 설치 건너뜀 (/NOVSCODE)"
    ${Else}
        StrCpy $1 ""
        nsExec::ExecToStack 'cmd /c where code'
        Pop $0
        Pop $2
        ${If} $0 == "0"
            StrCpy $1 "code"
        ${ElseIf} ${FileExists} "$LOCALAPPDATA\Programs\Microsoft VS Code\bin\code.cmd"
            StrCpy $1 '"$LOCALAPPDATA\Programs\Microsoft VS Code\bin\code.cmd"'
        ${EndIf}
        ${If} $1 != ""
            DetailPrint "VS Code 확장(aglink-vscode) 설치..."
            nsExec::ExecToLog 'cmd /c $1 --install-extension "$INSTDIR\aglink-vscode.vsix" --force'
            Pop $0
            ${If} $0 != "0"
                DetailPrint "  [경고] VS Code 확장 설치 실패(코드 $0) — VS Code 에서 '확장 → VSIX에서 설치'로 $INSTDIR\aglink-vscode.vsix 를 고르세요."
            ${EndIf}
        ${Else}
            DetailPrint "  [안내] VS Code 를 찾지 못해 확장 설치를 건너뜁니다. 나중에 $INSTDIR\aglink-vscode.vsix 를 설치하세요."
        ${EndIf}
    ${EndIf}

    ${If} $OptNoAutostart == "1"
        DetailPrint "자동 시작 등록 건너뜀 (/NOAUTOSTART)"
    ${Else}
        DetailPrint "로그온 자동 시작 등록 + 지금 시작..."
        WriteRegStr HKCU "${RUN_KEY}" "aglink-web" '"$SYSDIR\wscript.exe" //B //Nologo "$INSTDIR\start-web-daemon.vbs"'
        Exec '"$SYSDIR\wscript.exe" //B //Nologo "$INSTDIR\start-web-daemon.vbs"'
    ${EndIf}

    CreateDirectory "$SMPROGRAMS\aglink-web"
    CreateShortcut "$SMPROGRAMS\aglink-web\aglink-web 사용 안내.lnk" "$INSTDIR\guide.html"
    CreateShortcut "$SMPROGRAMS\aglink-web\크롬 확장 폴더 열기.lnk" "$INSTDIR\chrome-extension"
    CreateShortcut "$SMPROGRAMS\aglink-web\aglink-web 제거.lnk" "$INSTDIR\uninstall.exe"

    !insertmacro WriteUninstallInfo "${UNINST_KEY}" "aglink-web" "${VERSION}" "$INSTDIR\aglink-web.exe"
    DetailPrint "설치 완료."
SectionEnd

Section "Uninstall"
    DeleteRegValue HKCU "${RUN_KEY}" "aglink-web"
    DetailPrint "실행 중인 aglink-web 종료..."
    !insertmacro StopFromInstDir "aglink-web"
    !insertmacro UnregisterClaudeMCP "aglink-web"
    DeleteRegKey HKCU "Software\aglink\mcp-env\aglink-web"

    nsExec::ExecToStack 'cmd /c where code'
    Pop $0
    Pop $1
    ${If} $0 == "0"
        nsExec::ExecToLog 'cmd /c code --uninstall-extension aglink.aglink-vscode'
        Pop $0
    ${EndIf}

    Delete "$INSTDIR\aglink-web.exe"
    Delete "$INSTDIR\start-web-daemon.vbs"
    Delete "$INSTDIR\aglink-vscode.vsix"
    Delete "$INSTDIR\guide.html"
    RMDir /r "$INSTDIR\chrome-extension"
    Delete "$INSTDIR\uninstall.exe"
    RMDir "$INSTDIR"
    RMDir /r "$SMPROGRAMS\aglink-web"
    DeleteRegKey HKCU "${UNINST_KEY}"
    DetailPrint "제거 완료 (사용자 설정 %USERPROFILE%\.aglink 는 남겨 둡니다)."
SectionEnd
