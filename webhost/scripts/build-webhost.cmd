@echo off
setlocal EnableExtensions

rem Builds Vue, synchronizes its dist into webhost, then creates the embedded Go executable.
for %%I in ("%~dp0..") do set "WEBHOST_DIR=%%~fI"
for %%I in ("%WEBHOST_DIR%\..") do set "PROJECT_ROOT=%%~fI"
set "FRONTEND_DIR=%PROJECT_ROOT%\frontend"
set "SOURCE_DIST=%FRONTEND_DIR%\dist"
set "TARGET_DIST=%WEBHOST_DIR%\dist"
set "OUTPUT_DIR=%WEBHOST_DIR%\bin"

where pnpm >nul 2>nul
if errorlevel 1 goto :pnpm_error

echo [WebHost] Building frontend...
pushd "%FRONTEND_DIR%"
call pnpm run build
if errorlevel 1 goto :frontend_error
popd

echo [WebHost] Synchronizing dist files...
if not exist "%SOURCE_DIST%\" goto :dist_error
if exist "%TARGET_DIST%\" rmdir /s /q "%TARGET_DIST%"
mkdir "%TARGET_DIST%"
xcopy "%SOURCE_DIST%\*" "%TARGET_DIST%\" /E /I /Y >nul
if errorlevel 1 goto :copy_error

echo [WebHost] Building Go executable...
if not exist "%OUTPUT_DIR%\" mkdir "%OUTPUT_DIR%"
pushd "%WEBHOST_DIR%"
go build -trimpath -o "%OUTPUT_DIR%\collyrobot-webhost.exe" .
if errorlevel 1 goto :go_error
popd

echo [WebHost] Build completed: %OUTPUT_DIR%\collyrobot-webhost.exe
exit /b 0

:pnpm_error
echo [WebHost] pnpm was not found. Install Node.js and pnpm in the build environment.
goto :failed

:frontend_error
popd
echo [WebHost] Frontend build failed.
goto :failed

:dist_error
echo [WebHost] Frontend dist directory was not generated.
goto :failed

:copy_error
echo [WebHost] Failed to synchronize frontend dist files.
goto :failed

:go_error
popd
echo [WebHost] Go executable build failed.

:failed
exit /b 1
