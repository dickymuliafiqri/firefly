#Requires -Version 5.1
<#
.SYNOPSIS
    Firefly One-Line Installer for Windows.
    High-Concurrency OpenAI & Anthropic AI Reverse Proxy & Gateway.

.DESCRIPTION
    Downloads a prebuilt Firefly release, installs it under
    C:\Program Files\Firefly, bootstraps its configuration under
    C:\ProgramData\Firefly, and registers a Windows Scheduled Task that starts
    the gateway automatically at boot (as SYSTEM, whether or not anyone logs in).

    Quick install (run in an *Administrator* PowerShell):
      irm https://raw.githubusercontent.com/dickymuliafiqri/firefly/main/install.ps1 | iex

    Or with the cmd bootstrap (self-elevates):
      curl.exe -fsSL -o install.cmd https://raw.githubusercontent.com/dickymuliafiqri/firefly/main/install.cmd && install.cmd

.PARAMETER Version
    Release tag to install (e.g. v1.44.10). Defaults to $env:FIREFLY_VERSION or the latest release.

.PARAMETER InstallDir
    Directory for the executables. Defaults to $env:FIREFLY_INSTALL_DIR or "$env:ProgramFiles\Firefly".

.PARAMETER ConfigDir
    Directory for JSON configuration and runtime data.
    Defaults to $env:FIREFLY_CONFIG_DIR or "$env:ProgramData\Firefly".

.PARAMETER Addr
    Listen address for AI traffic and the dashboard. Defaults to $env:FIREFLY_ADDR or 0.0.0.0:8080.

.PARAMETER AdminToken
    Optional bearer token protecting admin endpoints. Written to firefly.env.

.PARAMETER DashboardPassword
    Optional dashboard master password. Written to firefly.env as FIREFLY_DASHBOARD_PASSWORD.

.PARAMETER SessionSecret
    Optional HMAC key for stateless dashboard sessions. Written to firefly.env.

.PARAMETER TursoURL / TursoToken
    Optional Turso cloud database URL and auth token. Written to firefly.env.

.PARAMETER SkipService
    Install binaries and configuration only; do not register the autostart task.

.PARAMETER SkipFirewall
    Do not add an inbound Windows Firewall rule for the listen port.

.PARAMETER SkipStart
    Register the autostart task but do not start Firefly now.

.PARAMETER Force
    Reinstall even when binaries already exist.

.PARAMETER Uninstall
    Stop and remove the scheduled task, firewall rule, and installed binaries.

.PARAMETER Purge
    With -Uninstall, also delete the configuration directory.

.EXAMPLE
    irm https://raw.githubusercontent.com/dickymuliafiqri/firefly/main/install.ps1 | iex

.EXAMPLE
    .\install.ps1 -Addr 0.0.0.0:9000 -AdminToken 'sk-admin-secret'

.EXAMPLE
    .\install.ps1 -Uninstall -Purge
#>
[CmdletBinding()]
param(
    [string] $Version,
    [string] $InstallDir,
    [string] $ConfigDir,
    [string] $Addr,
    [string] $AdminToken,
    [string] $DashboardPassword,
    [string] $SessionSecret,
    [string] $TursoURL,
    [string] $TursoToken,
    [switch] $SkipService,
    [switch] $SkipFirewall,
    [switch] $SkipStart,
    [switch] $Force,
    [switch] $Uninstall,
    [switch] $Purge
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

# -----------------------------------------------------------------------------
# Constants & Terminal Helpers
# -----------------------------------------------------------------------------
$Repo = 'dickymuliafiqri/firefly'
$TaskName = 'Firefly'
$FirewallRuleName = 'Firefly AI Gateway'

function Write-Step { param([string] $Message) Write-Host '==> ' -NoNewline -ForegroundColor Cyan; Write-Host $Message -ForegroundColor Cyan }
function Write-Info { param([string] $Message) Write-Host ":: $Message" -ForegroundColor DarkCyan }
function Write-Ok { param([string] $Message) Write-Host "[ok] $Message" -ForegroundColor Green }
function Write-Warn { param([string] $Message) Write-Host "[!] $Message" -ForegroundColor Yellow }
function Write-Err { param([string] $Message) Write-Host "[x] Error: $Message" -ForegroundColor Red }

function Test-Admin {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    return (New-Object Security.Principal.WindowsPrincipal($identity)).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Write-TextFileNoBom {
    param([string] $Path, [string] $Content)
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($Path, $Content, $utf8)
}

# Modern GitHub endpoints reject TLS 1.0, which is what Windows PowerShell 5.1
# negotiates by default.
try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch { }

# -----------------------------------------------------------------------------
# Privilege Verification (self-elevate when launched from a file)
#
# Admin is only required for operations that touch machine-wide state: the
# autostart Scheduled Task, the firewall rule, and uninstall. A binaries-only
# install (-SkipService -SkipFirewall) into a writable directory needs no
# elevation, which also keeps CI/local dry runs possible.
# -----------------------------------------------------------------------------
$needsAdmin = $Uninstall -or (-not $SkipService) -or (-not $SkipFirewall)

if ($needsAdmin -and -not (Test-Admin)) {
    if (-not $PSCommandPath) {
        Write-Err 'Administrator privileges are required.'
        Write-Info 'Open PowerShell with "Run as Administrator" and run the installer again:'
        Write-Host "    irm https://raw.githubusercontent.com/$Repo/main/install.ps1 | iex" -ForegroundColor Cyan
        exit 1
    }
    Write-Warn 'Administrator privileges required - relaunching elevated...'
    $relaunch = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $PSCommandPath)
    foreach ($key in $PSBoundParameters.Keys) {
        $value = $PSBoundParameters[$key]
        if ($value -is [switch]) {
            if ($value.IsPresent) { $relaunch += "-$key" }
        } else {
            $relaunch += "-$key"
            $relaunch += [string] $value
        }
    }
    $elevated = Start-Process -FilePath (Get-Process -Id $PID).Path -Verb RunAs `
        -ArgumentList $relaunch -Wait -PassThru
    exit $elevated.ExitCode
}

# -----------------------------------------------------------------------------
# Parameter Resolution (parameters > environment > defaults)
# -----------------------------------------------------------------------------
if (-not $Version) { $Version = $env:FIREFLY_VERSION }
if (-not $InstallDir) { $InstallDir = $env:FIREFLY_INSTALL_DIR }
if (-not $ConfigDir) { $ConfigDir = $env:FIREFLY_CONFIG_DIR }
if (-not $Addr) { $Addr = $env:FIREFLY_ADDR }
if (-not $AdminToken) { $AdminToken = $env:FIREFLY_ADMIN_TOKEN }
if (-not $DashboardPassword) { $DashboardPassword = $env:FIREFLY_DASHBOARD_PASSWORD }
if (-not $SessionSecret) { $SessionSecret = $env:FIREFLY_SESSION_SECRET }
if (-not $TursoURL) { $TursoURL = $env:TURSO_DATABASE_URL }
if (-not $TursoToken) { $TursoToken = $env:TURSO_AUTH_TOKEN }

if (-not $InstallDir) { $InstallDir = Join-Path $env:ProgramFiles 'Firefly' }
if (-not $ConfigDir) { $ConfigDir = Join-Path $env:ProgramData 'Firefly' }
if (-not $Addr) { $Addr = '0.0.0.0:8080' }

$InstallDir = [System.IO.Path]::GetFullPath($InstallDir)
$ConfigDir = [System.IO.Path]::GetFullPath($ConfigDir)

$Port = 8080
$parsedPort = 0
if ([int]::TryParse(($Addr -split ':')[-1], [ref] $parsedPort)) { $Port = $parsedPort }

$BinaryPath = Join-Path $InstallDir 'firefly.exe'
$LauncherPath = Join-Path $InstallDir 'run-firefly.ps1'
$EnvFilePath = Join-Path $ConfigDir 'firefly.env'
$LogPath = Join-Path $ConfigDir 'logs\firefly.log'

function Get-PrimaryIP {
    try {
        $ip = Get-NetIPAddress -AddressFamily IPv4 -ErrorAction Stop |
            Where-Object {
                $_.IPAddress -ne '127.0.0.1' -and
                $_.PrefixOrigin -ne 'WellKnown' -and
                $_.InterfaceAlias -notmatch 'Loopback'
            } |
            Select-Object -First 1 -ExpandProperty IPAddress
        if ($ip) { return $ip }
    } catch { }
    return '127.0.0.1'
}

# -----------------------------------------------------------------------------
# Architecture Detection
# -----------------------------------------------------------------------------
function Resolve-Arch {
    $raw = $env:PROCESSOR_ARCHITECTURE
    try { $raw = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString() } catch { }
    switch -Regex ($raw) {
        '^(X64|AMD64)$' { return 'amd64' }
        '^(Arm64|ARM64)$' { return 'arm64' }
        default { throw "Unsupported architecture: $raw. Supported: amd64 (x64), arm64." }
    }
}

# -----------------------------------------------------------------------------
# Release Version Resolution
# -----------------------------------------------------------------------------
function Resolve-FireflyVersion {
    param([string] $Requested)
    if ($Requested) { return $Requested }

    Write-Info 'Checking latest Firefly release on GitHub...'
    $resolved = $null

    # 1. GitHub API (simple, but rate limited when unauthenticated)
    try {
        $response = Invoke-WebRequest -Uri "https://api.github.com/repos/$Repo/releases/latest" `
            -UseBasicParsing -Headers @{ 'User-Agent' = 'firefly-installer' } -ErrorAction Stop
        $resolved = ($response.Content | ConvertFrom-Json).tag_name
    } catch { }

    # 2. Redirect URL of /releases/latest (no API rate limit)
    if (-not $resolved) {
        try {
            $response = Invoke-WebRequest -Uri "https://github.com/$Repo/releases/latest" `
                -MaximumRedirection 5 -UseBasicParsing -ErrorAction Stop
            $uri = $null
            try { $uri = $response.BaseResponse.ResponseUri } catch { }
            if (-not $uri) {
                try { $uri = $response.BaseResponse.RequestMessage.RequestUri } catch { }
            }
            if ($uri) { $resolved = ($uri.AbsolutePath.TrimEnd('/') -split '/')[-1] }
        } catch { }
    }

    if (-not $resolved -or $resolved -notlike 'v*') {
        throw 'Could not determine the latest Firefly release. Re-run with an explicit tag, e.g. -Version v1.44.10'
    }

    Write-Info "Resolved version: $resolved"
    return $resolved
}

# -----------------------------------------------------------------------------
# Download and Install Binaries
# -----------------------------------------------------------------------------
function Install-FireflyBinaries {
    param([string] $Version, [string] $Arch, [string] $InstallDir)

    Write-Step "Downloading Firefly $Version (windows/$Arch)..."

    $archive = "firefly_${Version}_windows_${Arch}.zip"
    $url = "https://github.com/$Repo/releases/download/$Version/$archive"
    $tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("firefly-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null

    try {
        $zipPath = Join-Path $tmpDir $archive
        $downloaded = $false
        try {
            Invoke-WebRequest -Uri $url -OutFile $zipPath -UseBasicParsing -ErrorAction Stop
            $downloaded = $true
        } catch {
            Write-Warn "Prebuilt release binary not found at $url"
        }

        if ($downloaded) {
            Expand-Archive -LiteralPath $zipPath -DestinationPath $tmpDir -Force
        } elseif ((Get-Command go -ErrorAction SilentlyContinue) -and (Test-Path './cmd/firefly/main.go')) {
            Write-Info 'Found local Firefly source with Go toolchain. Compiling from source...'
            & go build -trimpath -ldflags "-s -w -X main.Version=$Version" -o (Join-Path $tmpDir 'firefly.exe') ./cmd/firefly
            if ($LASTEXITCODE -ne 0) { throw 'go build failed for ./cmd/firefly' }
            if (Test-Path './cmd/loadtest/main.go') {
                & go build -trimpath -ldflags '-s -w' -o (Join-Path $tmpDir 'loadtest.exe') ./cmd/loadtest
            }
        } else {
            throw "Could not download Firefly release $Version. Please verify https://github.com/$Repo/releases"
        }

        Write-Step "Installing executables to $InstallDir..."
        New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

        $sourceExe = Join-Path $tmpDir 'firefly.exe'
        if (-not (Test-Path $sourceExe)) { throw 'firefly.exe not found in the extracted archive.' }
        Copy-Item -LiteralPath $sourceExe -Destination (Join-Path $InstallDir 'firefly.exe') -Force
        Write-Ok "Installed $(Join-Path $InstallDir 'firefly.exe')"

        $sourceLoad = Join-Path $tmpDir 'loadtest.exe'
        if (Test-Path $sourceLoad) {
            Copy-Item -LiteralPath $sourceLoad -Destination (Join-Path $InstallDir 'loadtest.exe') -Force
            Write-Ok "Installed $(Join-Path $InstallDir 'loadtest.exe') (CLI Benchmark Tool)"
        }
    } finally {
        Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# -----------------------------------------------------------------------------
# Configuration Bootstrap
# -----------------------------------------------------------------------------
function Initialize-FireflyConfig {
    param([string] $ConfigDir, [string] $Addr)

    Write-Step "Setting up configuration in $ConfigDir..."
    New-Item -ItemType Directory -Force -Path $ConfigDir | Out-Null
    New-Item -ItemType Directory -Force -Path (Join-Path $ConfigDir 'logs') | Out-Null

    if (-not (Test-Path $EnvFilePath)) {
        $adminTokenLine = '# FIREFLY_ADMIN_TOKEN=sk-admin-secret-token'
        if ($AdminToken) { $adminTokenLine = "FIREFLY_ADMIN_TOKEN=$AdminToken" }

        $dashboardPasswordLine = '# FIREFLY_DASHBOARD_PASSWORD=change-me'
        if ($DashboardPassword) { $dashboardPasswordLine = "FIREFLY_DASHBOARD_PASSWORD=$DashboardPassword" }

        $sessionSecretLine = '# FIREFLY_SESSION_SECRET=change-me-to-a-long-random-string'
        if ($SessionSecret) { $sessionSecretLine = "FIREFLY_SESSION_SECRET=$SessionSecret" }

        $envLines = @(
            '# Firefly Service Environment Configuration',
            "# Documentation: https://github.com/$Repo",
            '# One KEY=VALUE per line. Do not wrap values in quotes.',
            '',
            "FIREFLY_ADDR=$Addr",
            'FIREFLY_LOG_LEVEL=info',
            'FIREFLY_SHUTDOWN_GRACE_SECONDS=30',
            '',
            '# Optional: Bearer token protecting admin endpoints (/metrics, /debug/*)',
            $adminTokenLine,
            '',
            '# Optional: dashboard master password (defaults to 12345678)',
            $dashboardPasswordLine,
            '',
            '# Recommended on serverless / multi-instance hosts: HMAC key for dashboard',
            '# sessions. Keep it identical across instances so logins survive restarts.',
            $sessionSecretLine
        )

        if ($TursoURL -or $TursoToken) {
            $envLines += ''
            $envLines += '# Optional: Turso cloud database (libsql://...)'
            if ($TursoURL) { $envLines += "TURSO_DATABASE_URL=$TursoURL" } else { $envLines += '# TURSO_DATABASE_URL=libsql://...' }
            if ($TursoToken) { $envLines += "TURSO_AUTH_TOKEN=$TursoToken" } else { $envLines += '# TURSO_AUTH_TOKEN=...' }
        }

        Write-TextFileNoBom -Path $EnvFilePath -Content (($envLines -join "`r`n") + "`r`n")
        Write-Ok "Created environment file: $EnvFilePath"
    } else {
        Write-Info "Keeping existing environment file: $EnvFilePath"
    }

    $templates = @{
        'upstreams.json' = "{`r`n  `"upstreams`": []`r`n}`r`n"
        'models.json'    = "{`r`n  `"models`": []`r`n}`r`n"
        'tenants.json'   = "{`r`n  `"tenants`": []`r`n}`r`n"
        'combos.json'    = "{`r`n  `"combos`": []`r`n}`r`n"
    }
    foreach ($name in $templates.Keys) {
        $path = Join-Path $ConfigDir $name
        if (-not (Test-Path $path)) {
            Write-TextFileNoBom -Path $path -Content $templates[$name]
        }
    }
    Write-Ok 'Default JSON configuration verified'
}

# -----------------------------------------------------------------------------
# Service Launcher (loads firefly.env, appends logs, runs the gateway)
# -----------------------------------------------------------------------------
function New-FireflyLauncher {
    param([string] $LauncherPath, [string] $InstallDir, [string] $ConfigDir, [string] $Addr)

    Write-Step 'Writing service launcher...'

    # Single-quoted here-string: placeholders are substituted afterwards so the
    # launcher's own PowerShell variables are never interpolated by the installer.
    $template = @'
# Firefly service launcher - generated by the Firefly installer.
# Loads KEY=VALUE pairs from firefly.env, then runs the gateway with its
# output appended to <config-dir>\logs\firefly.log.
$ErrorActionPreference = 'Stop'
$installDir = '__INSTALL_DIR__'
$configDir = '__CONFIG_DIR__'
$addr = '__ADDR__'

$envFile = Join-Path $configDir 'firefly.env'
if (Test-Path -LiteralPath $envFile) {
    foreach ($line in [System.IO.File]::ReadAllLines($envFile)) {
        $text = $line.Trim()
        if (-not $text -or $text.StartsWith('#')) { continue }
        if ($text -match '^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$') {
            $name = $Matches[1]
            $value = $Matches[2].Trim()
            if ($value.Length -ge 2) {
                $first = $value[0]
                $last = $value[$value.Length - 1]
                if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
                    $value = $value.Substring(1, $value.Length - 2)
                }
            }
            Set-Item -Path ('Env:' + $name) -Value $value
        }
    }
}

$logDir = Join-Path $configDir 'logs'
if (-not (Test-Path -LiteralPath $logDir)) { New-Item -ItemType Directory -Force -Path $logDir | Out-Null }
$logFile = Join-Path $logDir 'firefly.log'

& (Join-Path $installDir 'firefly.exe') "-config-dir=$configDir" "-addr=$addr" *>> $logFile
exit $LASTEXITCODE
'@

    $content = $template.Replace('__INSTALL_DIR__', $InstallDir).
        Replace('__CONFIG_DIR__', $ConfigDir).
        Replace('__ADDR__', $Addr)

    Write-TextFileNoBom -Path $LauncherPath -Content ($content -replace "\r?\n", "`r`n")
    Write-Ok "Launcher written: $LauncherPath"
}

# -----------------------------------------------------------------------------
# Firewall Rule
# -----------------------------------------------------------------------------
function Add-FireflyFirewallRule {
    param([int] $Port)

    if ($SkipFirewall) { return }

    Write-Step "Adding Windows Firewall rule for inbound TCP $Port..."
    try {
        if (Get-NetFirewallRule -DisplayName $FirewallRuleName -ErrorAction SilentlyContinue) {
            Write-Info "Firewall rule already present: $FirewallRuleName"
            return
        }
        New-NetFirewallRule -DisplayName $FirewallRuleName -Direction Inbound -Action Allow `
            -Protocol TCP -LocalPort $Port -Profile Any | Out-Null
        Write-Ok "Firewall rule added for inbound TCP $Port"
    } catch {
        Write-Warn "Could not add firewall rule: $($_.Exception.Message)"
        Write-Info "Add it manually: netsh advfirewall firewall add rule name=`"$FirewallRuleName`" dir=in action=allow protocol=TCP localport=$Port"
    }
}

# -----------------------------------------------------------------------------
# Autostart: Windows Scheduled Task (At startup, running as SYSTEM)
# -----------------------------------------------------------------------------
function Install-FireflyTask {
    param([string] $LauncherPath)

    Write-Step 'Registering autostart Scheduled Task (runs at boot as SYSTEM)...'

    $shell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    $action = New-ScheduledTaskAction -Execute $shell `
        -Argument ('-NoProfile -NonInteractive -ExecutionPolicy Bypass -File "{0}"' -f $LauncherPath)

    $trigger = New-ScheduledTaskTrigger -AtStartup
    try { $trigger.Delay = 'PT15S' } catch { }

    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest

    # ExecutionTimeLimit Zero = unlimited, so a long-lived gateway is never
    # killed after the default 3 days; long SSE streams must not be interrupted.
    $settings = New-ScheduledTaskSettingsSet `
        -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable `
        -MultipleInstances IgnoreNew -ExecutionTimeLimit ([TimeSpan]::Zero) `
        -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -Hidden

    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
        -Principal $principal -Settings $settings -Force | Out-Null

    Write-Ok "Scheduled task '$TaskName' registered"
}

function Test-FireflyHealth {
    param([int] $Port, [int] $TimeoutSeconds = 25)

    $url = "http://127.0.0.1:$Port/healthz"
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        try {
            $response = Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 3 -ErrorAction Stop
            if ($response.StatusCode -eq 200) { return $true }
        } catch { }
        Start-Sleep -Milliseconds 750
    }
    return $false
}

function Uninstall-Firefly {
    Write-Step 'Removing Firefly...'

    if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
        Write-Ok "Removed scheduled task '$TaskName'"
    }

    try {
        Get-NetFirewallRule -DisplayName $FirewallRuleName -ErrorAction SilentlyContinue |
            Remove-NetFirewallRule -ErrorAction SilentlyContinue
    } catch { }

    if (Test-Path $InstallDir) {
        Remove-Item -LiteralPath $InstallDir -Recurse -Force -ErrorAction SilentlyContinue
        Write-Ok "Removed $InstallDir"
    }

    if (Test-Path $ConfigDir) {
        if ($Purge) {
            Remove-Item -LiteralPath $ConfigDir -Recurse -Force -ErrorAction SilentlyContinue
            Write-Ok "Removed $ConfigDir"
        } else {
            Write-Info "Kept configuration at $ConfigDir (use -Purge to delete it)"
        }
    }
}

# -----------------------------------------------------------------------------
# Main Execution Flow
# -----------------------------------------------------------------------------
function Invoke-Main {
    $banner = @'
  _____ _           __ _
 |  ___(_)_ __ ___ / _| |_   _
 | |_  | | '__/ _ \ |_| | | | |
 |  _| | | | |  __/  _| | |_| |
 |_|   |_|_|  \___|_| |_|\__, |
                         |___/
'@
    Write-Host ''
    Write-Host $banner -ForegroundColor Cyan
    Write-Host ' Firefly High-Concurrency AI Gateway Installer (Windows)' -ForegroundColor White
    Write-Host ''

    if ($Uninstall) {
        Uninstall-Firefly
        Write-Host ''
        Write-Ok 'Firefly has been uninstalled.'
        Write-Host ''
        return
    }

    $arch = Resolve-Arch
    Write-Info "Detected platform: windows/$arch"

    if ((Test-Path $BinaryPath) -and -not $Force) {
        Write-Warn "Firefly is already installed at $BinaryPath (use -Force to reinstall)"
    } else {
        $Version = Resolve-FireflyVersion -Requested $Version
        Install-FireflyBinaries -Version $Version -Arch $arch -InstallDir $InstallDir
    }

    Initialize-FireflyConfig -ConfigDir $ConfigDir -Addr $Addr
    New-FireflyLauncher -LauncherPath $LauncherPath -InstallDir $InstallDir -ConfigDir $ConfigDir -Addr $Addr

    if ($SkipService) {
        Write-Warn 'Skipping autostart setup (-SkipService)'
    } else {
        Install-FireflyTask -LauncherPath $LauncherPath

        if (-not $SkipFirewall) { Add-FireflyFirewallRule -Port $Port }

        if ($SkipStart) {
            Write-Warn 'Autostart registered but Firefly was not started (-SkipStart)'
        } else {
            Write-Step 'Starting Firefly in the background...'
            Start-ScheduledTask -TaskName $TaskName
            Start-Sleep -Seconds 1.5

            if (Test-FireflyHealth -Port $Port) {
                Write-Ok 'Firefly background service is ACTIVE and responding on /healthz'
            } else {
                Write-Warn 'Service started but /healthz did not respond yet.'
                Write-Info "Check the log: Get-Content `"$LogPath`" -Tail 50"
            }
        }
    }

    $hostIp = Get-PrimaryIP

    Write-Host ''
    Write-Host '==================================================================' -ForegroundColor Green
    Write-Host '  Firefly has been installed successfully!' -ForegroundColor Green
    Write-Host '==================================================================' -ForegroundColor Green
    Write-Host ''
    Write-Host "  Dashboard URL:      http://${hostIp}:$Port"
    Write-Host '  Default Password:   12345678 (change it in Settings)'
    Write-Host "  API Endpoint:       http://${hostIp}:$Port/v1/chat/completions"
    Write-Host "  Install Directory:  $InstallDir"
    Write-Host "  Config Directory:   $ConfigDir"
    Write-Host "  Environment File:   $EnvFilePath"
    Write-Host "  Log File:           $LogPath"
    Write-Host ''
    if (-not $SkipService) {
        Write-Host '  Service Controls (PowerShell):' -ForegroundColor White
        Write-Host "    Status:    Get-ScheduledTask -TaskName $TaskName"
        Write-Host "    Start:     Start-ScheduledTask -TaskName $TaskName"
        Write-Host "    Stop:      Stop-ScheduledTask -TaskName $TaskName"
        Write-Host "    Restart:   Stop-ScheduledTask -TaskName $TaskName; Start-ScheduledTask -TaskName $TaskName"
        Write-Host "    Logs:      Get-Content `"$LogPath`" -Wait -Tail 50"
        Write-Host '    Uninstall: .\install.ps1 -Uninstall [-Purge]'
        Write-Host ''
    }
    Write-Host '  Quick Concurrency Test:' -ForegroundColor White
    Write-Host "    & '$InstallDir\loadtest.exe' -mock -c 100 -profile low"
    Write-Host ''
}

try {
    Invoke-Main
} catch {
    Write-Err $_.Exception.Message
    exit 1
}
