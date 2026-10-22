<#
.SYNOPSIS
  Installs thetix on Windows (x64 or arm64).

.EXAMPLE
  irm https://thetix.ai/install.ps1 | iex

  (thetix.ai/install.ps1 redirects to this file on GitHub:
  https://raw.githubusercontent.com/thetaedge/thetix-cli/main/scripts/install.ps1)

.NOTES
  Environment (all optional):
    THETIX_VERSION         tag to install, e.g. v0.1.0 or 0.1.0 (default: latest)
    THETIX_INSTALL_DIR     install directory (default: %LOCALAPPDATA%\Programs\thetix)
    THETIX_NO_MODIFY_PATH  set to 1 to leave the user PATH alone
    THETIX_BASE_URL        download from here instead of GitHub Releases (mirrors, tests)

  Works in Windows PowerShell 5.1 and PowerShell 7+. Verifies the SHA-256 of the archive
  against the release's checksums.txt before installing anything.
#>
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"   # Invoke-WebRequest is very slow with the progress bar on PS 5.1
try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch {}

$repo = "thetaedge/thetix-cli"
$version = if ($env:THETIX_VERSION) { $env:THETIX_VERSION } else { "latest" }
if ($version -match '^\d') { $version = "v$version" }

if (-not [Environment]::Is64BitOperatingSystem) { throw "thetix needs 64-bit Windows" }
$arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" }
$asset = "thetix-windows-$arch.zip"

$base = if ($env:THETIX_BASE_URL) {
  $env:THETIX_BASE_URL.TrimEnd("/")
} elseif ($version -eq "latest") {
  "https://github.com/$repo/releases/latest/download"
} else {
  "https://github.com/$repo/releases/download/$version"
}

$dir = if ($env:THETIX_INSTALL_DIR) { $env:THETIX_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\thetix" }
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("thetix-" + [System.Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null

try {
  Write-Host "Downloading $asset ($version)..."
  Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile (Join-Path $tmp $asset)
  Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp "checksums.txt")

  Write-Host "Verifying checksum..."
  $line = Get-Content (Join-Path $tmp "checksums.txt") | Where-Object { $_ -match "[ *]$([regex]::Escape($asset))$" } | Select-Object -First 1
  if (-not $line) { throw "no checksum for $asset in checksums.txt" }
  $expected = ($line -split "\s+")[0].ToLower()
  $actual = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $asset)).Hash.ToLower()
  if ($expected -ne $actual) { throw "checksum mismatch for $asset (expected $expected, got $actual)" }

  Expand-Archive -Path (Join-Path $tmp $asset) -DestinationPath $tmp -Force
  $extracted = Join-Path $tmp "thetix.exe"
  if (-not (Test-Path $extracted)) { throw "archive did not contain thetix.exe" }

  New-Item -ItemType Directory -Force -Path $dir | Out-Null
  $target = Join-Path $dir "thetix.exe"
  # A running thetix.exe cannot be overwritten, but it can be renamed.
  if (Test-Path $target) {
    $old = "$target.old"
    Remove-Item $old -Force -ErrorAction SilentlyContinue
    Move-Item $target $old -Force
  }
  Copy-Item $extracted $target -Force
  Unblock-File $target -ErrorAction SilentlyContinue
  Remove-Item "$target.old" -Force -ErrorAction SilentlyContinue

  $installed = & $target --version
  if ($LASTEXITCODE -ne 0) { throw "$target was installed but does not run (exit code $LASTEXITCODE)" }
  Write-Host "Installed $installed to $target"

  # Current session first, so `thetix` works right away in this terminal.
  if (($env:Path -split ";") -notcontains $dir) { $env:Path = "$dir;$env:Path" }

  if ($env:THETIX_NO_MODIFY_PATH -and $env:THETIX_NO_MODIFY_PATH -ne "0") {
    Write-Host ""
    Write-Host "Add $dir to your PATH to use thetix from other terminals."
  } else {
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if (-not $userPath) { $userPath = "" }
    if (($userPath -split ";") -notcontains $dir) {
      $newPath = if ($userPath.TrimEnd(";")) { "$($userPath.TrimEnd(';'));$dir" } else { $dir }
      [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
      Write-Host "Added $dir to your user PATH. Other terminals will see it once reopened."
    }
  }

  Write-Host ""
  Write-Host "Next: run  thetix login  to connect your Thetix account."
}
finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
