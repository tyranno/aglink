; Chrome native messaging host for aglink-web (com.aglink.web).
;
; The extension prefers chrome.runtime.connectNative over its WebSocket: Chrome
; then starts aglink-web.exe itself and keeps the service worker alive while the
; port is open. The host manifest sits next to aglink-web.exe, so its "path" can
; stay relative. allowed_origins is the id fixed by the "key" in the extension's
; manifest.json — keep the two in step.

!define NMHOST_NAME "com.aglink.web"
!define NMHOST_EXT_ID "oojfjplopnochjenjpmjpmjelndnomoi"
!define NMHOST_REG "Software\Google\Chrome\NativeMessagingHosts\${NMHOST_NAME}"

; WriteNMHost ROOT — ROOT is HKCU (per-user setup) or HKLM (machine-wide).
!macro WriteNMHost ROOT
    DetailPrint "크롬 네이티브 메시징 호스트 등록 (${NMHOST_NAME})..."
    FileOpen $9 "$INSTDIR\${NMHOST_NAME}.json" w
    FileWrite $9 '{$\r$\n'
    FileWrite $9 '  "name": "${NMHOST_NAME}",$\r$\n'
    FileWrite $9 '  "description": "aglink-web bridge to the local daemon",$\r$\n'
    FileWrite $9 '  "path": "aglink-web.exe",$\r$\n'
    FileWrite $9 '  "type": "stdio",$\r$\n'
    FileWrite $9 '  "allowed_origins": ["chrome-extension://${NMHOST_EXT_ID}/"]$\r$\n'
    FileWrite $9 '}$\r$\n'
    FileClose $9
    WriteRegStr ${ROOT} "${NMHOST_REG}" "" "$INSTDIR\${NMHOST_NAME}.json"
!macroend

!macro RemoveNMHost ROOT
    DeleteRegKey ${ROOT} "${NMHOST_REG}"
    Delete "$INSTDIR\${NMHOST_NAME}.json"
!macroend
