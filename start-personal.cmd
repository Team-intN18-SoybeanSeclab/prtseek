@echo off
rem Compatibility entry point; the new launcher never replaces a running EXE.
call "%~dp0start-prtseek.cmd" %*
exit /b %errorlevel%
