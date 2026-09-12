# login-workbuddy.ps1 — WorkBuddy CN OAuth login (Windows native, replaces login.sh)
# Flow: wb-login url -> browser auth -> wb-login poll -> checkin -> save auths/workbuddy-<uid>.json
# Auth file format matches internal/auth nested form:
#   {"account":{"uid","enterpriseId","nickname"},"auth":{"accessToken","refreshToken","expiresAt","domain"}}
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

# Go login tool writes state to /tmp/ which resolves to <current-drive>:\tmp — ensure it exists
New-Item -ItemType Directory -Force -Path 'E:\tmp' | Out-Null
New-Item -ItemType Directory -Force -Path '.\auths' | Out-Null

Write-Host '============================================================'
Write-Host '  WorkBuddy OAuth 登录'
Write-Host '============================================================'
Write-Host ''

$authUrl = & .\wb-login.exe url
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($authUrl)) {
    Write-Host '获取授权链接失败（wb-login url）' -ForegroundColor Red
    exit 1
}

Write-Host '请在浏览器中打开以下链接完成登录：'
Write-Host ''
Write-Host "  $authUrl"
Write-Host ''
try { Set-Clipboard -Value $authUrl; Write-Host '(已复制到剪贴板)' } catch { }

Start-Process $authUrl  # auto-open default browser

Write-Host ''
Read-Host '完成登录后按回车继续（未登录请先去浏览器完成）' | Out-Null

Write-Host ''
Write-Host '正在获取 token...'
# PS5.1 兼容：2>&1 叠加 $ErrorActionPreference='Stop' 会把 stderr 升级为终止错误，
# 且 $LASTEXITCODE 可能不更新，故临时降 EAP 并把输出统一成文本再判空。
$prevEap = $ErrorActionPreference
$ErrorActionPreference = 'Continue'
$raw = (& .\wb-login.exe poll 2>&1 | Out-String).Trim()
$pollExit = $LASTEXITCODE
$ErrorActionPreference = $prevEap
if ($pollExit -ne 0 -or [string]::IsNullOrWhiteSpace($raw)) {
    Write-Host ''
    Write-Host "获取 token 失败：$raw" -ForegroundColor Red
    Write-Host '可能登录还没完成就按了回车，请重新运行本脚本再试。'
    exit 1
}

$r = $raw | ConvertFrom-Json
$token    = $r.access_token
$refresh  = $r.refresh_token
$expiresIn = [int64]$r.expires_in
$domain   = [string]$r.domain
$uid      = [string]$r.uid
$entId    = [string]$r.enterprise_id
$nick     = [string]$r.nickname

if ([string]::IsNullOrEmpty($uid)) {
    Write-Host '无法获取 uid，请检查 token 是否有效' -ForegroundColor Red
    exit 1
}

$expiresAt = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds() + $expiresIn

# --- daily checkin (idempotent, same endpoint as login.sh) ---
Write-Host ''
try {
    $headers = @{
        'Authorization' = "Bearer $token"
        'Accept'        = 'application/json'
        'Content-Type'  = 'application/json'
        'X-User-Id'     = $uid
    }
    if ($entId) { $headers['X-Enterprise-Id'] = $entId; $headers['X-Tenant-Id'] = $entId }
    if ($domain) { $headers['X-Domain'] = $domain }
    $resp = Invoke-RestMethod -Uri 'https://www.codebuddy.cn/v2/billing/meter/daily-checkin' `
        -Method Post -Body '{}' -Headers $headers -TimeoutSec 15
    if ($resp.code -eq 0) { Write-Host "签到: 成功 $($resp.data | ConvertTo-Json -Compress)" }
    else { Write-Host "签到: $($resp.msg)" }
} catch {
    $msg = try { ($_.ErrorDetails.Message | ConvertFrom-Json).msg } catch { $_.Exception.Message }
    Write-Host "签到: $msg（不影响登录，服务端 09:00/21:00 会自动签到）"
}

# --- save auth file (nested form, identical to internal/auth reader) ---
$authFile = ".\auths\workbuddy-$uid.json"
$action = if (Test-Path $authFile) { '覆盖' } else { '新增' }
$auth = @{
    account = @{ uid = $uid; enterpriseId = $entId; nickname = $nick }
    auth    = @{ accessToken = $token; refreshToken = $refresh; expiresAt = $expiresAt; domain = $domain }
}
$auth | ConvertTo-Json -Depth 5 | Set-Content $authFile -Encoding utf8
Write-Host "已保存（$action）: $authFile"

Write-Host ''
Write-Host '============================================================'
Write-Host '  登录完成！'
Write-Host "  UID: $uid"
Write-Host "  Nickname: $(if ($nick) { $nick } else { '（未获取到）' })"
Write-Host "  Token: $($token.Substring(0, [Math]::Min(30, $token.Length)))..."
Write-Host "  有效期至: $([DateTimeOffset]::FromUnixTimeSeconds($expiresAt).LocalDateTime.ToString('yyyy-MM-dd HH:mm'))"
Write-Host '============================================================'
Write-Host ''
Write-Host '如服务正在运行且未自动识别新账号，请重启服务（stop 后再 start）。'
