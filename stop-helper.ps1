# stop-helper.ps1 - kill watchdog + proxy, verify port release (ASCII only for encoding safety)
$ErrorActionPreference = 'SilentlyContinue'

Write-Host '[*] Stopping watchdog...'
Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" |
    Where-Object { $_.CommandLine -like '*workbuddy2api*watchdog.ps1*' } |
    ForEach-Object { Stop-Process -Id $_.ProcessId -Force }

Write-Host '[*] Stopping proxy process...'
Stop-Process -Name 'workbuddy-proxy' -Force

Write-Host '[*] Closing admin window (Chrome app instance)...'
# launch.vbs / start-workbuddy.cmd start the admin window with an isolated
# profile and the --workbuddy-admin-window marker flag. Chrome blocks scripts
# from closing their own window (window.close is intercepted), so this script
# closes the dedicated instance at OS level by command-line feature. The
# isolated profile guarantees the user's daily Chrome is never matched/kept
# untouched. (Keep ASCII only - see file header note.)
Get-CimInstance Win32_Process -Filter "Name='chrome.exe'" |
    Where-Object { $_.CommandLine -like '*--workbuddy-admin-window*' } |
    ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }

Start-Sleep -Seconds 2

Write-Host '[*] Verifying port 8091...'
Start-Process -FilePath 'curl.exe' -ArgumentList '-s -o NUL -m 3 http://127.0.0.1:8091/healthz' -WindowStyle Hidden -Wait
if ($LASTEXITCODE -eq 0) {
    Write-Host '[!] Port 8091 still answering - leftover process may remain.'
    exit 1
} else {
    Write-Host '[OK] Service stopped, port 8091 released.'
    exit 0
}
