# install-booth.ps1 — one-command install for the Bykami booth agent on Windows.
#
# This script downloads the newest qualifying agent release from GitHub,
# verifies the SHA256 digest, installs the binary under Program Files, and
# registers it as a service. A re-run updates an existing installation.
#
# What this proves and what it does not:
# - The download is integrity-checked against the release's SHA256 file.
# - It is NOT authenticated. Verifying the ed25519 signature in PowerShell
#   needs a component Windows does not ship, so the script checks the digest
#   and stops on mismatch. Authentication starts with the first self-update,
#   where the running agent verifies the signature against its compiled-in
#   public key before installing anything.
# - Closing the first hop (script-to-release) properly needs an Authenticode
#   certificate, and that has not been bought.
#
# What the script does not do:
# - The WinUSB driver swap for the Canon camera (Zadig). That is a one-time
#   manual step.
# - Chrome in kiosk mode. The browser is started from a logon session, not by
#   the service.
# - Assigned Access. That is a Windows configuration step done separately.

param(
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"

function Write-Step($msg) {
    Write-Host "[install-booth] $msg"
}

# 1. Must run elevated.
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Error "This script must run as Administrator. Right-click PowerShell and choose 'Run as administrator'."
    exit 1
}

# Use the canonical owner name. The old path (bhaktiyudha/bykami) still works
# because GitHub issues a 301 redirect, but Invoke-RestMethod does not
# reliably follow redirects for API calls, and a 301 at a shop with no
# person to retry is a failed first install.
$Repo = "yudhabhaktin/bykami"
$AssetName = "bykami-agent.exe"
$InstallDir = "${env:ProgramFiles}\Bykami"
$BoothRoot = "${env:ProgramData}\Bykami\booth"
$HotFolder = "$BoothRoot\hot"
$VersionFile = "$BoothRoot\.deployed-version"

Write-Step "repository: $Repo"
Write-Step "install directory: $InstallDir"
Write-Step "booth root: $BoothRoot"
if ($DryRun) {
    Write-Step "DRY RUN — nothing will be installed or changed"
}

# 2. Pick the newest qualifying release.
# Not /releases/latest: that returns the newest release of any kind, and
# api-* releases are published to this same repository. The list endpoint is
# used instead.
#
# The explicit sort is not decoration. GitHub's list endpoint does not reliably
# return releases newest-first — confirmed on this repository, where an api-*
# release is returned ahead of the agent-* one published a minute later — so
# taking the first match would install a stale binary or appear to sit on the
# current version forever.
Write-Step "fetching releases list ..."
$releasesUri = "https://api.github.com/repos/$Repo/releases?per_page=100"
$releases = Invoke-RestMethod -Uri $releasesUri -Headers @{ Accept = "application/vnd.github+json" }

$candidates = @()
foreach ($r in $releases) {
    if ($r.draft -or $r.prerelease) { continue }
    if (-not $r.tag_name.StartsWith("agent-")) { continue }
    $hasAsset = $false
    foreach ($a in $r.assets) {
        if ($a.name -eq $AssetName) {
            $hasAsset = $true
            break
        }
    }
    if (-not $hasAsset) { continue }
    $pub = if ($r.published_at) { $r.published_at } else { $r.created_at }
    $candidates += [pscustomobject]@{
        Tag         = $r.tag_name
        PublishedAt = [datetime]::Parse($pub)
        AssetUrl    = ($r.assets | Where-Object { $_.name -eq $AssetName }).browser_download_url
    }
}

if ($candidates.Count -eq 0) {
    Write-Error "No qualifying agent-* release carrying $AssetName found."
    exit 1
}

$candidates = $candidates | Sort-Object PublishedAt -Descending
$chosen = $candidates[0]
Write-Step "chosen release: $($chosen.Tag) published at $($chosen.PublishedAt)"

# 3. Download the asset and its SHA256, verify the digest.
$tmpDir = Join-Path $env:TEMP "bykami-install-$([guid]::NewGuid().ToString())"
if (-not $DryRun) {
    New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null
}

$assetTmp = Join-Path $tmpDir $AssetName
$shaTmp   = Join-Path $tmpDir "$AssetName.sha256"

Write-Step "downloading $($chosen.AssetUrl) ..."
if (-not $DryRun) {
    Invoke-WebRequest -Uri $chosen.AssetUrl -OutFile $assetTmp -UseBasicParsing
}

$shaUrl = "$($chosen.AssetUrl).sha256"
Write-Step "downloading $shaUrl ..."
if (-not $DryRun) {
    Invoke-WebRequest -Uri $shaUrl -OutFile $shaTmp -UseBasicParsing
}

if (-not $DryRun) {
    $shaContent = (Get-Content -Raw $shaTmp).Trim()
    $expectedHash = ($shaContent -split '\s+')[0]
    $actualHash = (Get-FileHash -Algorithm SHA256 $assetTmp).Hash.ToLower()

    Write-Step "expected SHA256: $expectedHash"
    Write-Step "actual SHA256:   $actualHash"

    if ($expectedHash -ne $actualHash) {
        Write-Error "SHA256 mismatch — the downloaded file is corrupt or has been tampered with."
        exit 1
    }
    Write-Step "digest verified"
}

# 4. Install to Program Files, preserve outgoing as .previous, write version file.
if (-not $DryRun) {
    if (-not (Test-Path $InstallDir)) {
        New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    }
    if (-not (Test-Path $BoothRoot)) {
        New-Item -ItemType Directory -Path $BoothRoot -Force | Out-Null
    }
    if (-not (Test-Path $HotFolder)) {
        New-Item -ItemType Directory -Path $HotFolder -Force | Out-Null
    }

    $installed = Join-Path $InstallDir $AssetName
    $previous = "$installed.previous"

    if (Test-Path $installed) {
        if (Test-Path $previous) {
            Remove-Item $previous -Force
        }
        Move-Item $installed $previous -Force
        Write-Step "preserved outgoing binary as $previous"
    }

    Move-Item $assetTmp $installed -Force
    Write-Step "installed $installed"

    Set-Content -Path $VersionFile -Value $chosen.Tag -NoNewline
    Write-Step "wrote version file: $VersionFile = $($chosen.Tag)"

    Remove-Item $tmpDir -Recurse -Force
}

# 5. Register and start the service using the binary's own subcommand.
# The service is registered with the flags the booth actually needs, so a
# service that starts on boot runs the booth that was configured, not the
# defaults. Uninstall then reinstall makes re-runs idempotent.
$installedBin = Join-Path $InstallDir $AssetName
$svcArgs = @(
    "-root", $BoothRoot,
    "-source", "hotfolder",
    "-hot-folder", $HotFolder,
    "-camera-tool", "gphoto2",
    "-update-repo", $Repo,
    "-printer", "dnp",
    "-printer-queue", "DS-RX1",
    "-printer-cut-queue", "DS-RX1 cut"
)
if (-not $DryRun) {
    Write-Step "registering service ..."
    & $installedBin service uninstall 2>$null
    & $installedBin @svcArgs service install
    Write-Step "starting service ..."
    Start-Service -Name "bykami-agent" -ErrorAction Stop
    Write-Step "service started"
} else {
    Write-Step "would run: $installedBin service uninstall (ignored if absent)"
    Write-Step "would run: $installedBin service install $svcArgs"
    Write-Step "would run: Start-Service -Name bykami-agent"
}

# 6. Diagnostics — run the built-in doctor.
if (-not $DryRun) {
    Write-Step "---"
    & $installedBin @svcArgs doctor
    Write-Step "---"
} else {
    Write-Step "would run: $installedBin doctor"
}

if ($DryRun) {
    Write-Step "dry run complete — nothing was installed"
} else {
    Write-Step "install complete — tag $($chosen.Tag)"
}
