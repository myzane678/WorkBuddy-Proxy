# 更新桌面快捷方式图标为项目 icon.ico 并刷新图标缓存（一次性脚本）
$ws = New-Object -ComObject WScript.Shell
$desktop = [Environment]::GetFolderPath('Desktop')
$lnk = $ws.CreateShortcut("$desktop\WorkBuddy Proxy.lnk")
$lnk.IconLocation = 'E:\work\workbuddy2api\assets\icon.ico,0'
$lnk.Save()
ie4uinit.exe -show
Write-Output "ICON UPDATED: $($lnk.IconLocation)"
