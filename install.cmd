@echo off
setlocal EnableExtensions
rem ============================================================================
rem  Firefly AI Gateway - Windows installer bootstrap (cmd wrapper)
rem
rem  Downloads install.ps1 from the repository and runs it. All arguments are
rem  forwarded, so this is a drop-in equivalent of the Linux one-liner:
rem
rem      curl -fsSL https://raw.githubusercontent.com/dickymuliafiqri/firefly/main/install.sh | bash
rem
rem  Examples:
rem      install.cmd
rem      install.cmd -User                     (install for this user only, no UAC)
rem      install.cmd -Version v1.44.10 -Addr 0.0.0.0:8080
rem      install.cmd -Uninstall
rem      install.cmd -SkipService -SkipFirewall   (binaries only, no UAC)
rem
rem  Scope: an elevated shell installs machine-wide (Program Files, boot task,
rem  firewall rule). A normal shell installs per-user (%LOCALAPPDATA%, Startup
rem  shortcut, no firewall rule) and needs no admin. -Machine / -User force one.
rem
rem  Environment overrides:
rem      FIREFLY_VERSION      pin the installer script to a release tag
rem                           (default: main branch)
rem      FIREFLY_INSTALL_URL  full URL of install.ps1 (forks, mirrors, air-gapped
rem                           hosts); overrides FIREFLY_VERSION
rem      FIREFLY_NO_PAUSE     set to 1 to never pause at the end
rem ============================================================================

set "REPO=dickymuliafiqri/firefly"
set "REF=main"
if not "%FIREFLY_VERSION%"=="" set "REF=%FIREFLY_VERSION%"

set "PS1_URL=https://raw.githubusercontent.com/%REPO%/%REF%/install.ps1"
if not "%FIREFLY_INSTALL_URL%"=="" set "PS1_URL=%FIREFLY_INSTALL_URL%"
set "PS1_FILE=%TEMP%\firefly-install.ps1"

rem Pause at the end only when it looks like an interactive double-click run,
rem so scripts and CI invocations never block.
set "FF_PAUSE=1"
if not "%~1"=="" set "FF_PAUSE="
if not "%FIREFLY_NO_PAUSE%"=="" set "FF_PAUSE="

echo.
echo  Firefly AI Gateway - Windows Installer
echo  --------------------------------------
echo  Script : %PS1_URL%
echo.

where powershell >nul 2>&1
if errorlevel 1 (
    echo [x] PowerShell was not found on PATH. Windows 10/11 ships it by default.
    set "RC=1"
    goto :finish
)

echo  ^>^> Downloading installer...
powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command ^
    "$ErrorActionPreference='Stop';" ^
    "try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch {};" ^
    "try { Invoke-WebRequest -UseBasicParsing -Uri '%PS1_URL%' -OutFile '%PS1_FILE%' } catch { Write-Host ('       ' + $_.Exception.Message); exit 1 }"
if errorlevel 1 (
    echo [x] Failed to download install.ps1 from %PS1_URL%
    echo     Check your network connection, or clone the repository and run
    echo     powershell -ExecutionPolicy Bypass -File .\install.ps1
    set "RC=1"
    goto :finish
)

echo  ^>^> Running installer...
powershell -NoProfile -ExecutionPolicy Bypass -File "%PS1_FILE%" %*
set "RC=%ERRORLEVEL%"

del /f /q "%PS1_FILE%" >nul 2>&1

if not "%RC%"=="0" (
    echo.
    echo [x] Installation failed with exit code %RC%.
) else (
    echo.
    echo [ok] Installation finished.
)

:finish
if "%FF_PAUSE%"=="1" pause
endlocal & exit /b %RC%
