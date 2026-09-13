# 创建桌面快捷方式（一次性脚本）
$ws = New-Object -ComObject WScript.Shell
$desktop = [Environment]::GetFolderPath('Desktop')
$lnk = $ws.CreateShortcut("$desktop\WorkBuddy Proxy.lnk")
$lnk.TargetPath = 'E:\work\workbuddy2api\launch.vbs'
$lnk.WorkingDirectory = 'E:\work\workbuddy2api'
$chrome = "$env:ProgramFiles\Google\Chrome\Application\chrome.exe"
if (Test-Path $chrome) { $lnk.IconLocation = "$chrome,0" }
$lnk.Description = 'WorkBuddy Proxy 管理台（自动启动服务）'
$lnk.Save()
Write-Output "CREATED: $desktop\WorkBuddy Proxy.lnk"
