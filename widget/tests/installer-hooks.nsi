Unicode true
!include MUI2.nsh
!include LogicLib.nsh
!define LEDGERGATE_RUN_KEY "Software\LedgerGate\InstallerHookRegression"
!include "..\src-tauri\installer-hooks.nsh"
!define MAINBINARYNAME "LedgerGateWidget"
Name "LedgerGate installer hook regression"
OutFile "${HOOK_TEST_OUTPUT}"
RequestExecutionLevel user
SilentInstall silent
Var UpdateMode
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

!macro AssertRun expected
  ReadRegStr $0 HKCU "${LEDGERGATE_RUN_KEY}" "LedgerGateWidget"
  ${If} $0 != '${expected}'
    DetailPrint "Startup regression assertion failed; actual value: $0"
    DeleteRegKey HKCU "${LEDGERGATE_RUN_KEY}"
    SetErrorLevel 1
    Quit
  ${EndIf}
!macroend

Section
  ; 所有注册表操作限于独立测试键，不读写真实 Windows 启动项。
  DeleteRegKey HKCU "${LEDGERGATE_RUN_KEY}"
  StrCpy $INSTDIR "C:\LedgerGate Old"
  StrCpy $LedgerGateAutostart 0
  StrCpy $UpdateMode 0

  ; 全新安装和原本关闭的状态不自动启用。
  Call LedgerGateRememberAutostart
  !insertmacro NSIS_HOOK_PREINSTALL
  !insertmacro NSIS_HOOK_POSTINSTALL
  !insertmacro AssertRun ""

  ; 普通升级先卸载旧版，再安装到另一路径。
  WriteRegStr HKCU "${LEDGERGATE_RUN_KEY}" "LedgerGateWidget" '"$INSTDIR\LedgerGateWidget.exe"'
  Call LedgerGateRememberAutostart
  !insertmacro NSIS_HOOK_POSTUNINSTALL
  !insertmacro AssertRun ""
  StrCpy $INSTDIR "C:\LedgerGate New"
  !insertmacro NSIS_HOOK_PREINSTALL
  !insertmacro NSIS_HOOK_POSTINSTALL
  !insertmacro AssertRun '"C:\LedgerGate New\LedgerGateWidget.exe"'

  ; 正常卸载仍删除本安装路径的启动项。
  !insertmacro NSIS_HOOK_POSTUNINSTALL
  !insertmacro AssertRun ""

  ; 其他便携副本的启动项不能被本次安装、卸载改写。
  StrCpy $LedgerGateAutostart 0
  WriteRegStr HKCU "${LEDGERGATE_RUN_KEY}" "LedgerGateWidget" '"C:\Portable\LedgerGateWidget.exe"'
  Call LedgerGateRememberAutostart
  !insertmacro NSIS_HOOK_PREINSTALL
  !insertmacro NSIS_HOOK_POSTINSTALL
  !insertmacro NSIS_HOOK_POSTUNINSTALL
  !insertmacro AssertRun '"C:\Portable\LedgerGateWidget.exe"'

  ; 静默覆盖通过 PREINSTALL 读取；/UPDATE 卸载不删除。
  StrCpy $LedgerGateAutostart 0
  WriteRegStr HKCU "${LEDGERGATE_RUN_KEY}" "LedgerGateWidget" '"$INSTDIR\LedgerGateWidget.exe"'
  !insertmacro NSIS_HOOK_PREINSTALL
  StrCpy $UpdateMode 1
  !insertmacro NSIS_HOOK_POSTUNINSTALL
  !insertmacro NSIS_HOOK_POSTINSTALL
  !insertmacro AssertRun '"C:\LedgerGate New\LedgerGateWidget.exe"'
  DeleteRegKey HKCU "${LEDGERGATE_RUN_KEY}"
  SetErrorLevel 0
SectionEnd
