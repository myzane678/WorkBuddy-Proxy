@echo off
setlocal
title WorkBuddy Proxy - Stopping

echo ============================================================
echo   WorkBuddy Proxy - Stop
echo ============================================================
echo.

powershell -NoProfile -ExecutionPolicy Bypass -File "E:\work\workbuddy2api\stop-helper.ps1"

echo.
echo Auto-close in 3s...
timeout /t 3 >nul
exit /b 0
