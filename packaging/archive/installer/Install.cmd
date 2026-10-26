@echo off
"%~dp0bundle\python\python.exe" -I -X utf8 -B "%~dp0installer\install.py" install %*
set "result=%ERRORLEVEL%"
if not "%result%"=="0" pause
exit /b %result%
