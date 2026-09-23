; Shared pieces of the per-product aglink installers (aglink-web, aglink-screen).
;
; These installers are per-user: no administrator rights, installed under
; %LOCALAPPDATA%\Programs\<product>, registered in HKCU. That is what lets a
; teammate install one product without asking IT, and update it by running the
; next setup over the top.
;
; Command-line switches every product accepts (also usable with /S):
;   /NOCLAUDE     do not register the MCP server with Claude Code
;   /NOVSCODE     do not install the VS Code extension (aglink-web only)
;   /NOAUTOSTART  do not start at logon or start now (aglink-web only)

!include "FileFunc.nsh"
!include "LogicLib.nsh"

Var OptNoClaude
Var OptNoVSCode
Var OptNoAutostart

!macro ReadOptions
    ${GetParameters} $R0
    ClearErrors
    ${GetOptions} $R0 "/NOCLAUDE" $R1
    ${IfNot} ${Errors}
        StrCpy $OptNoClaude "1"
    ${EndIf}
    ClearErrors
    ${GetOptions} $R0 "/NOVSCODE" $R1
    ${IfNot} ${Errors}
        StrCpy $OptNoVSCode "1"
    ${EndIf}
    ClearErrors
    ${GetOptions} $R0 "/NOAUTOSTART" $R1
    ${IfNot} ${Errors}
        StrCpy $OptNoAutostart "1"
    ${EndIf}
!macroend

; StopFromInstDir ends running copies of EXE that live in $INSTDIR — and only
; those. An update must release the file lock, but a second copy of the same
; program running from somewhere else (a developer build, another install) is
; none of this installer's business.
!macro StopFromInstDir EXE
    nsExec::ExecToLog `powershell -NoProfile -ExecutionPolicy Bypass -Command "Get-Process -Name '${EXE}' -ErrorAction SilentlyContinue | Where-Object { $$_.Path -and $$_.Path.StartsWith('$INSTDIR', [StringComparison]::OrdinalIgnoreCase) } | Stop-Process -Force"`
    Pop $0
    Sleep 800
!macroend

; RegisterClaudeMCP registers NAME as a stdio MCP server for Claude Code, user
; scope, replacing any earlier registration of the same name. Claude Code may be
; installed as a native claude.exe or an npm claude.cmd; `cmd /c` finds either
; on PATH. Missing Claude Code is reported, not fatal.
!macro RegisterClaudeMCP NAME EXEPATH
    ${If} $OptNoClaude == "1"
        DetailPrint "Claude Code 등록 건너뜀 (/NOCLAUDE)"
    ${Else}
        nsExec::ExecToStack 'cmd /c where claude'
        Pop $0
        Pop $1
        ${If} $0 == "0"
            DetailPrint "Claude Code 에 MCP 서버 '${NAME}' 등록..."
            nsExec::ExecToLog 'cmd /c claude mcp remove ${NAME} -s user'
            Pop $0
            nsExec::ExecToLog 'cmd /c claude mcp add --scope user ${NAME} -- "${EXEPATH}" mcp'
            Pop $0
            ${If} $0 == "0"
                ; Remember that WE registered this name, from THIS folder, so the
                ; uninstaller can tell our registration from someone else's.
                WriteRegStr HKCU "Software\aglink\mcp" "${NAME}" "$INSTDIR"
            ${Else}
                DetailPrint "  [경고] 등록 실패(코드 $0) — 수동: claude mcp add --scope user ${NAME} -- $\"${EXEPATH}$\" mcp"
            ${EndIf}
        ${Else}
            DetailPrint "  [안내] claude CLI 를 찾지 못했습니다. Claude Code 설치 후 다음을 실행하세요:"
            DetailPrint "         claude mcp add --scope user ${NAME} -- $\"${EXEPATH}$\" mcp"
        ${EndIf}
    ${EndIf}
!macroend

; UnregisterClaudeMCP removes the registration ONLY if this install created it,
; which the install recorded under HKCU\Software\aglink\mcp.
;
; A registration of the same name is often something else: this machine's own
; always-on daemon registered over HTTP, or a developer build in another folder.
; An uninstall here must not take that away. It did once — a test uninstall
; silently dropped the working HTTP registrations of aglink-web and
; aglink-screen, and nothing said so until the tools went missing.
!macro UnregisterClaudeMCP NAME
    ReadRegStr $2 HKCU "Software\aglink\mcp" "${NAME}"
    ${If} $2 == "$INSTDIR"
        nsExec::ExecToStack 'cmd /c where claude'
        Pop $0
        Pop $1
        ${If} $0 == "0"
            DetailPrint "Claude Code 등록 '${NAME}' 해제..."
            nsExec::ExecToLog 'cmd /c claude mcp remove ${NAME} -s user'
            Pop $0
        ${EndIf}
        DeleteRegValue HKCU "Software\aglink\mcp" "${NAME}"
    ${ElseIf} $2 != ""
        DetailPrint "  [건너뜀] '${NAME}' 등록은 다른 설치($2)의 것이라 그대로 둡니다."
    ${Else}
        DetailPrint "  [건너뜀] '${NAME}' 등록은 이 설치가 만든 것이 아니라 그대로 둡니다."
    ${EndIf}
!macroend

; WriteUninstallInfo records the product in Settings → Apps (per user).
!macro WriteUninstallInfo KEY DISPLAYNAME VERSION ICONEXE
    WriteUninstaller "$INSTDIR\uninstall.exe"
    WriteRegStr HKCU "${KEY}" "DisplayName" "${DISPLAYNAME}"
    WriteRegStr HKCU "${KEY}" "DisplayVersion" "${VERSION}"
    WriteRegStr HKCU "${KEY}" "Publisher" "aglink"
    WriteRegStr HKCU "${KEY}" "InstallLocation" "$INSTDIR"
    WriteRegStr HKCU "${KEY}" "DisplayIcon" "${ICONEXE}"
    WriteRegStr HKCU "${KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
    WriteRegStr HKCU "${KEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
    WriteRegDWORD HKCU "${KEY}" "NoModify" 1
    WriteRegDWORD HKCU "${KEY}" "NoRepair" 1
!macroend
