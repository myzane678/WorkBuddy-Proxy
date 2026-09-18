' launch.vbs — WorkBuddy Proxy 一键启动入口（桌面快捷方式指向本文件，当作应用使用）。
' 无黑窗静默运行：服务未启动时自动拉起看门狗并等待健康（约 3~15 秒），已在运行则秒开；
' 最后用 Chrome 应用窗口（--app，无地址栏）打开 admin 页，没有 Chrome 时回退默认浏览器。
Option Explicit
Const HEALTH = "http://127.0.0.1:8091/healthz"
Const ADMIN  = "http://127.0.0.1:8091/admin"
Const BASE   = "E:\work\workbuddy2api"

Dim sh, fso, chrome, i, ok
Set sh = CreateObject("WScript.Shell")
Set fso = CreateObject("Scripting.FileSystemObject")

' 定位 Chrome（与 start-workbuddy.cmd 同款探测顺序）
chrome = ""
If fso.FileExists(sh.ExpandEnvironmentStrings("%ProgramFiles%\Google\Chrome\Application\chrome.exe")) Then
  chrome = sh.ExpandEnvironmentStrings("%ProgramFiles%\Google\Chrome\Application\chrome.exe")
ElseIf fso.FileExists(sh.ExpandEnvironmentStrings("%ProgramFiles(x86)%\Google\Chrome\Application\chrome.exe")) Then
  chrome = sh.ExpandEnvironmentStrings("%ProgramFiles(x86)%\Google\Chrome\Application\chrome.exe")
ElseIf fso.FileExists(sh.ExpandEnvironmentStrings("%LocalAppData%\Google\Chrome\Application\chrome.exe")) Then
  chrome = sh.ExpandEnvironmentStrings("%LocalAppData%\Google\Chrome\Application\chrome.exe")
End If

' Healthy 用系统自带 curl 探测 healthz（curl 有应答即视为存活，与 start-workbuddy.cmd 口径一致）
Function Healthy()
  Healthy = (sh.Run("%ComSpec% /c ""C:\Windows\System32\curl.exe"" -s -o NUL -m 3 " & HEALTH, 0, True) = 0)
End Function

If Not Healthy() Then
  ' 未运行：隐藏拉起看门狗（watchdog.vbs 自身即无窗口），再轮询健康最多 15 秒
  sh.CurrentDirectory = BASE
  sh.Run "wscript.exe """ & BASE & "\watchdog.vbs""", 0, False
  ok = False
  For i = 1 To 15
    WScript.Sleep 1000
    If Healthy() Then
      ok = True
      Exit For
    End If
  Next
  If Not ok Then WScript.Quit 1
End If

' Chrome app window (frameless, desktop-app like).
' Isolated user-data-dir: never merges with the user's daily Chrome instance.
' --workbuddy-admin-window is a marker flag for stop-helper.ps1 to close this
' window at OS level when stopping the service (Chrome ignores unknown flags
' but keeps them on the command line, so process query can match it).
If chrome <> "" Then
  sh.Run """" & chrome & """ --user-data-dir=""" & BASE & "\data\admin-profile"" --workbuddy-admin-window --no-first-run --no-default-browser-check --app=" & ADMIN, 1, False
Else
  sh.Run "rundll32.exe url.dll,FileProtocolHandler " & ADMIN, 0, False
End If
