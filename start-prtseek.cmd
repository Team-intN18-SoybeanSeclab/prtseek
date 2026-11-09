@echo off
setlocal
powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0start-prtseek.ps1" %*
set "launchExit=%errorlevel%"
if not "%launchExit%"=="0" pause
endlocal & exit /b %launchExit%
