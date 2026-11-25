!ifndef LEDGERGATE_RUN_KEY
  !define LEDGERGATE_RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"
!endif

Var LedgerGateAutostart
!define MUI_CUSTOMFUNCTION_GUIINIT LedgerGateRememberAutostart

Function LedgerGateRememberAutostart
  ; GUI 初始化早于旧版卸载；此处主程序宏尚未定义，使用固定的发行文件名。
  Push $0
  ReadRegStr $0 HKCU "${LEDGERGATE_RUN_KEY}" "LedgerGateWidget"
  ${If} $0 == '"$INSTDIR\LedgerGateWidget.exe"'
    StrCpy $LedgerGateAutostart 1
  ${EndIf}
  Pop $0
FunctionEnd

!macro NSIS_HOOK_PREINSTALL
  ; 静默安装不经过 GUI 初始化，其覆盖安装路径在这里读取既有启动项。
  Call LedgerGateRememberAutostart
!macroend

!macro NSIS_HOOK_POSTINSTALL
  ${If} $LedgerGateAutostart == 1
    WriteRegStr HKCU "${LEDGERGATE_RUN_KEY}" "LedgerGateWidget" '"$INSTDIR\${MAINBINARYNAME}.exe"'
  ${EndIf}
!macroend

!macro NSIS_HOOK_POSTUNINSTALL
  ; 升级保留启动项；卸载仅清理指向本次安装路径的启动项。
  ${If} $UpdateMode <> 1
    Push $0
    ReadRegStr $0 HKCU "${LEDGERGATE_RUN_KEY}" "LedgerGateWidget"
    ${If} $0 == '"$INSTDIR\${MAINBINARYNAME}.exe"'
      DeleteRegValue HKCU "${LEDGERGATE_RUN_KEY}" "LedgerGateWidget"
    ${EndIf}
    Pop $0
  ${EndIf}
!macroend
