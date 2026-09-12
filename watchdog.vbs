' watchdog.vbs — launch watchdog.ps1 fully hidden (no console flash)
Set ws = CreateObject("WScript.Shell")
ws.Run "powershell -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File ""E:\work\workbuddy2api\watchdog.ps1""", 0, False
