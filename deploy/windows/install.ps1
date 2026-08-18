<#
.SYNOPSIS
  Kraken Windows Agent installer — the PowerShell counterpart to deploy/install.sh.

.DESCRIPTION
  Downloads the release binaries from GitHub, verifies their checksums,
  installs them under the Kraken root, writes agent.yaml, opens the firewall
  ports, registers the agent as a Windows service, and starts it. With an
  enrollment token (from the Panel's Add Node dialog) the agent enrolls
  itself on first start — one command from paste to running node.

  Idempotent: safe to re-run to upgrade the binaries or to retry enrollment
  with a fresh token. Existing agent.yaml settings are preserved; only the
  enrollment keys are updated when new values are passed.

.EXAMPLE
  # One-command install + enroll (values from the Panel's Add Node dialog):
  powershell -ExecutionPolicy Bypass -File install.ps1 `
    -PanelUrl http://panel:8080 -Token <TOKEN> -CaFingerprint <SHA256>

.EXAMPLE
  # Upgrade the binaries in place (service is stopped and restarted):
  powershell -ExecutionPolicy Bypass -File install.ps1

.NOTES
  Requires an elevated PowerShell and Docker Desktop in Windows-containers
  mode (see deploy/windows/README.md).
#>
[CmdletBinding()]
param(
  # Panel base URL for auto-enrollment (e.g. http://panel:8080).
  [string]$PanelUrl = "",
  # One-time bootstrap token minted in the Panel's Add Node dialog.
  [string]$Token = "",
  # Full SHA-256 fingerprint of the Panel CA (pins the enrollment).
  [string]$CaFingerprint = "",
  # Release tag to install (e.g. v0.17.0); default resolves the latest.
  [string]$Version = "",
  # Kraken root: binaries, state, server data, and backups live beneath it.
  [string]$Root = "C:\kraken",
  # Skip the firewall rule (e.g. when a domain GPO manages the firewall).
  [switch]$NoFirewall,
  # Install binaries + config only; skip service registration and start.
  [switch]$NoService
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"  # Invoke-WebRequest is ~10x faster without the progress bar

$Repo = "briggleman/kraken"
$ServiceName = "kraken-agent"

function Log([string]$msg)  { Write-Host "-> $msg" -ForegroundColor Cyan }
function Warn([string]$msg) { Write-Host "!  $msg" -ForegroundColor Yellow }
function Die([string]$msg)  { Write-Host "x  $msg" -ForegroundColor Red; exit 1 }

# ---- preconditions -------------------------------------------------------
$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  Die "must run from an elevated PowerShell (right-click -> Run as administrator)"
}
if ($Token -and -not $PanelUrl) {
  Die "-Token requires -PanelUrl (the Panel the token was minted on)"
}

# ---- version resolution --------------------------------------------------
if (-not $Version) {
  Log "resolving latest release from github.com/$Repo"
  try {
    $rel = Invoke-RestMethod -UseBasicParsing -Uri "https://api.github.com/repos/$Repo/releases/latest"
    $Version = $rel.tag_name
  } catch {
    Die "could not resolve the latest release tag: $_"
  }
}
Log "installing Kraken Agent $Version (root: $Root)"

# ---- directories ---------------------------------------------------------
foreach ($d in @("$Root\bin", "$Root\state", "$Root\server-data", "$Root\backups", "$Root\certs")) {
  New-Item -ItemType Directory -Force -Path $d | Out-Null
}

# ---- download + verify ---------------------------------------------------
$base = "https://github.com/$Repo/releases/download/$Version"
$tmp = Join-Path $env:TEMP "kraken-install-$([guid]::NewGuid().ToString('n').Substring(0,8))"
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  $binaries = @("kraken-agent-windows-amd64.exe", "kraken-krakenctl-windows-amd64.exe")

  Log "downloading SHA256SUMS"
  Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS" -OutFile "$tmp\SHA256SUMS"
  $sums = @{}
  foreach ($line in Get-Content "$tmp\SHA256SUMS") {
    $parts = $line -split '\s+', 2
    if ($parts.Count -eq 2) { $sums[$parts[1].TrimStart('*')] = $parts[0].ToLower() }
  }

  foreach ($f in $binaries) {
    Log "  $f"
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$f" -OutFile "$tmp\$f"
    if (-not $sums.ContainsKey($f)) { Die "no SHA256SUMS entry for $f" }
    $got = (Get-FileHash "$tmp\$f" -Algorithm SHA256).Hash.ToLower()
    if ($got -ne $sums[$f]) {
      Die "checksum mismatch for ${f}: expected $($sums[$f]), got $got - aborting"
    }
  }
  Log "checksums verified"

  # ---- install binaries (stop the service first when upgrading) ----------
  $agentExe = "$Root\bin\kraken-agent.exe"
  $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
  $wasRunning = $svc -and $svc.Status -eq "Running"
  if ($wasRunning) {
    Log "stopping $ServiceName to replace the binary"
    Stop-Service -Name $ServiceName -Force
    $svc.WaitForStatus("Stopped", (New-TimeSpan -Seconds 60))
  }
  # Stable names: the firewall rule is port-based and the service definition
  # points here, so upgrades are a pure file swap.
  Copy-Item "$tmp\kraken-agent-windows-amd64.exe"    $agentExe               -Force
  Copy-Item "$tmp\kraken-krakenctl-windows-amd64.exe" "$Root\bin\kraken-krakenctl.exe" -Force
  Log "installed $Root\bin\kraken-agent.exe + kraken-krakenctl.exe"
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

# ---- agent.yaml ----------------------------------------------------------
# The agent discovers <root>\agent.yaml on its own (--root is on the service
# command line). Base settings are written once; the enrollment keys are
# replaced-or-appended on every run that provides them, because re-running
# with a fresh token is the recovery path for an expired one.
$configPath = "$Root\agent.yaml"
if (-not (Test-Path $configPath)) {
  $nodeId = $env:COMPUTERNAME.ToLower()
  @"
# Kraken Agent configuration. Managed by deploy/windows/install.ps1 - edit freely.
# Every key maps 1:1 to a KRAKEN_* environment variable and a --flag,
# and those spellings outrank this file (env > file, flags > env).
node_id: $nodeId
node_os: windows
addr: ":9090"
sftp_addr: ":2022"
"@ | Out-File -FilePath $configPath -Encoding utf8
  Log "wrote $configPath (node_id: $nodeId)"
} else {
  Log "(existing) $configPath"
}

function Set-YamlKey([string]$path, [string]$key, [string]$value) {
  $lines = @(Get-Content $path)
  $pattern = "^#?\s*${key}:"
  $newLine = "${key}: `"$value`""
  if ($lines -match $pattern) {
    $lines = $lines | ForEach-Object { if ($_ -match $pattern) { $newLine } else { $_ } }
  } else {
    $lines += $newLine
  }
  Set-Content -Path $path -Value $lines -Encoding utf8
}

if ($PanelUrl) {
  Set-YamlKey $configPath "panel_url" $PanelUrl
  Log "set panel_url: $PanelUrl"
}
if ($Token) {
  Set-YamlKey $configPath "enroll_token" $Token
  Log "set enroll_token: <one-time token>"
  if ($CaFingerprint) {
    Set-YamlKey $configPath "ca_fingerprint" $CaFingerprint
    Log "set ca_fingerprint: $CaFingerprint"
  } else {
    Warn "no -CaFingerprint given - enrollment will trust whatever CA the Panel returns"
  }
}

# ---- firewall ------------------------------------------------------------
# Port-based on purpose: program-based rules silently stop matching when a
# binary is renamed or replaced; port rules survive every upgrade.
if (-not $NoFirewall) {
  $ruleName = "Kraken Agent (TCP 9090 gRPC + 2022 SFTP)"
  if (-not (Get-NetFirewallRule -DisplayName $ruleName -ErrorAction SilentlyContinue)) {
    New-NetFirewallRule -DisplayName $ruleName -Direction Inbound -Action Allow `
      -Protocol TCP -LocalPort 9090, 2022 | Out-Null
    Log "firewall: allowed inbound TCP 9090 + 2022"
  } else {
    Log "firewall: rule already present"
  }
}

# ---- service -------------------------------------------------------------
if ($NoService) {
  if ($wasRunning) {
    Log "restarting $ServiceName (was running before the upgrade)"
    Start-Service -Name $ServiceName
  } else {
    Log "done (service skipped). Run manually with: $agentExe --root $Root"
  }
  exit 0
}

if (-not (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue)) {
  Log "registering the $ServiceName service"
  & $agentExe --service install --root $Root
  if ($LASTEXITCODE -ne 0) { Die "service install failed (exit $LASTEXITCODE)" }
}

Log "starting $ServiceName"
Start-Service -Name $ServiceName

# ---- verify --------------------------------------------------------------
# The service logs to <root>\state\agent.log. Poll it briefly so the operator
# sees enroll + serve happen (or the reason it didn't) without hunting.
$logPath = "$Root\state\agent.log"
$deadline = (Get-Date).AddSeconds(60)
$verified = $false
while ((Get-Date) -lt $deadline) {
  Start-Sleep -Seconds 2
  if (-not (Test-Path $logPath)) { continue }
  $tail = Get-Content $logPath -Tail 50 -ErrorAction SilentlyContinue
  if ($tail -match "serving with mutual TLS") { $verified = $true; break }
  $enrollErr = $tail | Where-Object { $_ -match "auto-enroll" -and $_ -match '"level":"ERROR"' } | Select-Object -Last 1
  if ($enrollErr) { break }
  if ((Get-Service -Name $ServiceName).Status -ne "Running") { break }
}

if ($verified) {
  Log "agent is serving with mutual TLS - finish registration in the Panel's Add Node dialog"
  Log "logs: $logPath"
} else {
  Warn "agent did not report mTLS serving within 60s - check the log:"
  Warn "  Get-Content $logPath -Tail 30"
  Warn "an expired token is recoverable: mint a fresh one in the Panel and re-run this script with -Token/-PanelUrl"
  exit 1
}
