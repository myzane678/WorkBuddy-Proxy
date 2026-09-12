# watchdog.ps1 — hidden watchdog for workbuddy-proxy
# Polls http://127.0.0.1:8091/healthz every 3s; respawns the exe if the
# HTTP endpoint stops answering (any HTTP status counts as alive, incl. 503
# from /healthz when no healthy account — connection failure is the only
# "down" signal). Logs to data\watchdog.log.
$ErrorActionPreference = 'Continue'
$proj  = 'E:\work\workbuddy2api'
$exe   = Join-Path $proj 'workbuddy-proxy.exe'
$log   = Join-Path $proj 'data\watchdog.log'
$health = 'http://127.0.0.1:8091/healthz'

function Log($msg) {
    "$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss') $msg" | Add-Content -Path $log -Encoding utf8
}

New-Item -ItemType Directory -Force -Path (Join-Path $proj 'data') | Out-Null
Log 'watchdog started'

while ($true) {
    # 直接调用 curl（而非 Start-Process）：Start-Process 不写 $LASTEXITCODE，
    # 导致误判服务宕机、每轮循环强杀重启（v1-v2 全部"偶发断流"的真凶）。
    curl.exe -s -o NUL -m 3 $health | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Log "health check FAILED (curl exit $LASTEXITCODE) - starting proxy"
        $p = Get-Process -Name 'workbuddy-proxy' -ErrorAction SilentlyContinue
        if ($p) {
            Log "proxy process exists (pid=$($p.Id)) but not answering - killing"
            $p | Stop-Process -Force
            Start-Sleep -Seconds 2
        }
        # stdout/stderr -> data\proxy-*.log：panic 栈与请求表格日志落盘可查
        Start-Process -FilePath $exe -WorkingDirectory $proj -WindowStyle Hidden `
            -RedirectStandardOutput (Join-Path $proj 'data\proxy-stdout.log') `
            -RedirectStandardError (Join-Path $proj 'data\proxy-stderr.log')
        Log "proxy spawned (stdout/stderr -> data\proxy-*.log), waiting for health..."
        Start-Sleep -Seconds 5
    }
    Start-Sleep -Seconds 3
}
