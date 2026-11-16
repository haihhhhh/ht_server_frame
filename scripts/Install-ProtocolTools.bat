@echo off
setlocal
if not defined GO_BIN set "GO_BIN=go"
set "GOTOOLCHAIN=local"
set "GOWORK=off"
pushd "%~dp0.."
if errorlevel 1 exit /b 1
"%GO_BIN%" run -mod=readonly ./tools/protocol install %*
set "result=%errorlevel%"
popd
if not "%PROTOCOL_NO_PAUSE%"=="1" pause
exit /b %result%
