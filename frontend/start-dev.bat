@echo off
setlocal

rem Development-only Vite launcher. Production uses the root webhost binary.
cd /d "%~dp0"
if errorlevel 1 goto :directory_error

where node >nul 2>nul
if errorlevel 1 goto :node_error

where pnpm >nul 2>nul
if not errorlevel 1 (
    set "PNPM_CMD=pnpm"
) else (
    where corepack >nul 2>nul
    if errorlevel 1 goto :pnpm_error
    set "PNPM_CMD=corepack pnpm"
)

if not exist "node_modules\" (
    echo [CollyRobot Frontend] Installing development dependencies...
    call %PNPM_CMD% install
    if errorlevel 1 goto :install_error
)

if /i "%~1"=="--check" (
    echo [CollyRobot Frontend] Development startup checks passed.
    exit /b 0
)

echo [CollyRobot Frontend] Starting Vite development server at http://localhost:5173
echo [CollyRobot Frontend] Production deployment uses webhost, not this script.
call %PNPM_CMD% dev
if errorlevel 1 goto :start_error
exit /b 0

:directory_error
echo [CollyRobot Frontend] Cannot open the frontend directory.
goto :failed

:node_error
echo [CollyRobot Frontend] Node.js was not found. Install Node.js and try again.
goto :failed

:pnpm_error
echo [CollyRobot Frontend] pnpm or Corepack was not found. Run: npm install -g pnpm
goto :failed

:install_error
echo [CollyRobot Frontend] Failed to install development dependencies.
goto :failed

:start_error
echo [CollyRobot Frontend] The Vite development server stopped with an error.

:failed
if /i not "%~1"=="--check" pause
exit /b 1
