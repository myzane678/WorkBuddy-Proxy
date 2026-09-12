@echo off
setlocal
title WorkBuddy Proxy - Starting

set HEALTH=http://127.0.0.1:8091/healthz
set CURL="C:\Windows\System32\curl.exe"

echo ============================================================
echo   WorkBuddy Proxy - Start
echo ============================================================
echo.

REM -- pre-check: already running? (any HTTP answer counts) --
%CURL% -s -o NUL -m 3 %HEALTH%
if %errorlevel%==0 (
    echo [!] Service already running. Nothing to do.
    echo.
    timeout /t 5 >nul
    exit /b 0
)

REM -- pre-check: auths dir has credentials? --
if not exist "E:\work\workbuddy2api\auths\*.json" (
    echo [!] No account logged in yet. Run login-workbuddy.ps1 first.
    echo.
    timeout /t 8 >nul
    exit /b 1
)

echo [*] Launching hidden watchdog...
wscript.exe "E:\work\workbuddy2api\watchdog.vbs"

REM -- poll for health up to 15s --
echo [*] Waiting for service to come up...
set /a tries=0
:waitloop
%CURL% -s -o NUL -m 3 %HEALTH%
if %errorlevel%==0 goto started
set /a tries+=1
if %tries% geq 15 goto failed
timeout /t 1 >nul
goto waitloop

:started
echo.
echo ============================================================
echo   [OK] START SUCCESS
echo   API:      http://127.0.0.1:8091/v1
echo   Health:   http://127.0.0.1:8091/healthz
echo   Status:   http://127.0.0.1:8091/status
echo ============================================================
echo.

REM -- open admin page in Google Chrome (fallback: default browser) --
set "CHROME="
if exist "%ProgramFiles%\Google\Chrome\Application\chrome.exe" set "CHROME=%ProgramFiles%\Google\Chrome\Application\chrome.exe"
if not defined CHROME if exist "%ProgramFiles(x86)%\Google\Chrome\Application\chrome.exe" set "CHROME=%ProgramFiles(x86)%\Google\Chrome\Application\chrome.exe"
if not defined CHROME if exist "%LocalAppData%\Google\Chrome\Application\chrome.exe" set "CHROME=%LocalAppData%\Google\Chrome\Application\chrome.exe"
if defined CHROME (
    echo [*] Opening admin page in Google Chrome...
    start "" "%CHROME%" "http://127.0.0.1:8091/admin"
) else (
    echo [*] Chrome not found, opening with default browser...
    start "" "http://127.0.0.1:8091/admin"
)

echo.
echo Auto-close in 5s...
timeout /t 5 >nul
exit /b 0

:failed
echo.
echo ============================================================
echo   [FAIL] Service not up within 15s. Check log:
echo   E:\work\workbuddy2api\data\watchdog.log
echo ============================================================
echo.
timeout /t 10 >nul
exit /b 1
