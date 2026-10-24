# install.ps1 - installs Vaglio as a Windows service, in one command, from an elevated PowerShell:
#
#   & ([scriptblock]::Create((irm https://raw.githubusercontent.com/BioDream-LLC/vaglio/main/deploy/install.ps1))) -Version v0.1.0
#
# It downloads the release archive for this machine, refuses it unless its SHA-256 is the one in the release's
# SHA256SUMS (and, when the GitHub CLI is installed, unless GitHub's build provenance attestation verifies), installs
# the program in C:\Program Files\Vaglio, keeps data in C:\ProgramData\Vaglio\data (readable by SYSTEM and
# Administrators only) and the plan's policies in C:\ProgramData\Vaglio\policies, and registers a startup task "Vaglio"
# run as LocalService. Run again with a newer -Version to upgrade: data and policies are kept.
param(
  [Parameter(Mandatory = $true)][string]$Version,
  [string]$Archive = '',
  [string]$Sums = '',
  [string]$Addr = '127.0.0.1:8740',
  [string]$HostName = '',
  [switch]$NoStart
)
$ErrorActionPreference = 'Stop'
if ($Version -notmatch '^v\d+\.\d+\.\d+$') { throw "$Version is not vX.Y.Z" }
$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $admin) { throw 'Run it from an elevated PowerShell (Run as administrator).' }
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$name = "vaglio-$Version-windows-$arch"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid())
New-Item -ItemType Directory $tmp | Out-Null
try {
  $zip = Join-Path $tmp "$name.zip"
  $sumsFile = Join-Path $tmp 'SHA256SUMS'
  if ($Archive -eq '') {
    $base = "https://github.com/BioDream-LLC/vaglio/releases/download/$Version"
    Invoke-WebRequest "$base/$name.zip" -OutFile $zip -UseBasicParsing
    Invoke-WebRequest "$base/SHA256SUMS" -OutFile $sumsFile -UseBasicParsing
  } else {
    if ($Sums -eq '') { throw '-Archive needs -Sums' }
    Copy-Item $Archive $zip; Copy-Item $Sums $sumsFile
  }
  $want = (Get-Content $sumsFile | Where-Object { $_ -match "\s\*?$([regex]::Escape("$name.zip"))$" } | ForEach-Object { ($_ -split '\s+')[0] })
  $got = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLower()
  if (-not $want -or $want -ne $got) { throw "$name.zip does not match SHA256SUMS: not installed" }
  Write-Host 'SHA-256 matches SHA256SUMS.'
  if ($Archive -eq '' -and (Get-Command gh -ErrorAction SilentlyContinue)) {
    gh attestation verify $zip --repo BioDream-LLC/vaglio | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'The build provenance does not verify: not installed' }
    Write-Host 'GitHub build provenance verifies.'
  }
  Expand-Archive $zip -DestinationPath $tmp
  $prog = Join-Path $env:ProgramFiles 'Vaglio'
  $root = Join-Path $env:ProgramData 'Vaglio'
  $data = Join-Path $root 'data'
  $policies = Join-Path $root 'policies'
  Stop-ScheduledTask -TaskName Vaglio -ErrorAction SilentlyContinue
  Get-Process vaglio -ErrorAction SilentlyContinue | Stop-Process -Force
  New-Item -ItemType Directory -Force $prog, $data, $policies | Out-Null
  Copy-Item (Join-Path $tmp "$name\vaglio.exe") (Join-Path $prog 'vaglio.exe') -Force
  # Data: SYSTEM, Administrators and the service's account only; nothing inherited.
  icacls $data /inheritance:r /grant:r 'SYSTEM:(OI)(CI)F' 'Administrators:(OI)(CI)F' 'NT AUTHORITY\LocalService:(OI)(CI)M' | Out-Null
  # The bearer token the PAS server sends on /api/pas/: made once, random, readable by the service's account,
  # SYSTEM and Administrators only. Give the same value to the PAS server.
  $tokenFile = Join-Path $root 'pas-token'
  if (-not (Test-Path $tokenFile) -or (Get-Item $tokenFile).Length -eq 0) {
    $bytes = New-Object byte[] 32
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    [System.IO.File]::WriteAllText($tokenFile, (($bytes | ForEach-Object { $_.ToString('x2') }) -join ''))
  }
  icacls $tokenFile /inheritance:r /grant:r 'SYSTEM:F' 'Administrators:F' 'NT AUTHORITY\LocalService:R' | Out-Null
  $vargs = "serve -data `"$data`" -policies `"$policies`" -addr $Addr -token-file `"$tokenFile`""
  if ($HostName -ne '') { $vargs += " -hostname $HostName" }
  # A scheduled task at startup, as LocalService, restarted when it stops: vaglio.exe is a program, not a Windows
  # service binary, and the service manager would kill it for not answering its protocol.
  $action = New-ScheduledTaskAction -Execute (Join-Path $prog 'vaglio.exe') -Argument $vargs -WorkingDirectory $root
  $trigger = New-ScheduledTaskTrigger -AtStartup
  $principal = New-ScheduledTaskPrincipal -UserId 'NT AUTHORITY\LOCALSERVICE' -LogonType ServiceAccount -RunLevel Limited
  $settings = New-ScheduledTaskSettingsSet -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
    -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
  Register-ScheduledTask -TaskName Vaglio -Action $action -Trigger $trigger -Principal $principal -Settings $settings `
    -Description 'Vaglio prior authorization decisions' -Force | Out-Null
  if (-not $NoStart) { Start-ScheduledTask -TaskName Vaglio }
  & (Join-Path $prog 'vaglio.exe') version
  Write-Host "Installed. Put the plan's policies in $policies. The console's certificate authority, to trust once, is $data\tls\ca.crt."
  Write-Host "The PAS server's bearer token is in $tokenFile (give it to the PAS server; it is never printed)."
  Write-Host "The first start made an administrator, admin, whose password went unseen. Give it one with:"
  Write-Host "  & '$prog\vaglio.exe' user reset -data '$data' admin"
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
