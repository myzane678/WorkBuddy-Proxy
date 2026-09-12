# stop-helper.ps1 - kill watchdog + proxy, verify port release (ASCII only for encoding safety)
$ErrorActionPreference = 'SilentlyContinue'

Write-Host '[*] Stopping watchdog...'
Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" |
    Where-Object { $_.CommandLine -like '*workbuddy2api*watchdog.ps1*' } |
    ForEach-Object { Stop-Process -Id $_.ProcessId -Force }

Write-Host '[*] Stopping proxy process...'
Stop-Process -Name 'workbuddy-proxy' -Force

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
