# Kraken — Windows Agent install

For hosts running **Windows-native game servers** (V Rising, Palworld
Xbox variant, other Windows-only titles). The Panel and its Postgres
stay on your Linux / Docker host; the Windows machine runs the Agent
bare-metal and reports back over mTLS gRPC.

For **Linux** Agents, use [`deploy/install.sh`](../install.sh) instead.
For running the Panel itself, see [`deploy/docker-compose.example.yml`](../docker-compose.example.yml).

## Prerequisites

- **Docker Desktop for Windows**, running in **Windows containers mode**
  (right-click tray → *Switch to Windows containers*). Linux game
  servers on this node need Linux mode; Windows-native games need
  Windows mode. Docker Desktop can't do both simultaneously — pick per
  host.
- **Hyper-V** (or Windows Sandbox) feature enabled. The Docker Desktop
  installer handles this on Windows 10/11 Pro; Server 2019/2022 comes
  with Hyper-V available.
- The Panel is up somewhere reachable (e.g.
  `http://media-server:9095`) and you're signed in as an admin.

All commands below assume an **elevated PowerShell** on the Windows host.

## 1. Download the release binaries

```powershell
$ver  = "v0.4.0"   # or the latest tag from https://github.com/briggleman/kraken/releases
$dest = "C:\kraken"
New-Item -ItemType Directory -Force -Path `
  "$dest\bin","$dest\state","$dest\certs","$dest\server-data","$dest\backups" | Out-Null

$base = "https://github.com/briggleman/kraken/releases/download/$ver"
foreach ($f in @(
    "kraken-agent-windows-amd64.exe",
    "kraken-krakenctl-windows-amd64.exe",
    "SHA256SUMS")) {
  Invoke-WebRequest -Uri "$base/$f" -OutFile "$dest\bin\$f"
}

# Verify — the two hashes below must match the corresponding lines in SHA256SUMS.
cd "$dest\bin"
Get-Content SHA256SUMS | Select-String 'windows-amd64.exe'
Get-FileHash kraken-agent-windows-amd64.exe    -Algorithm SHA256
Get-FileHash kraken-krakenctl-windows-amd64.exe -Algorithm SHA256
```

If any hash disagrees, stop.

## 2. Mint a one-time bootstrap token on the Panel

Pick one.

**A. Panel UI** (easier):

1. Sign into the Panel.
2. **Settings → Nodes → Add node**.
3. Give it a name (e.g. `windows-01`) and copy the bootstrap token —
   it's shown once.

**B. Panel API** (scriptable — swap in your Panel URL + password):

```powershell
$panel = "http://<panel-host>:<port>"
$token = curl.exe -sS -X POST "$panel/api/v1/auth/login" `
  -H 'Content-Type: application/json' `
  -d '{"username":"<your-admin>","password":"<your-password>"}' `
  | ConvertFrom-Json | Select-Object -ExpandProperty token

curl.exe -sS -X POST "$panel/api/v1/agents/bootstrap-tokens" `
  -H "Authorization: Bearer $token" `
  -H 'Content-Type: application/json' `
  -d '{"node_name":"windows-01","ttl_seconds":900}'
# → { "token": "<BOOTSTRAP_TOKEN>", "expires_at": "..." }
```

Tokens are **single-use** and expire in 15 minutes by default. Mint a
fresh one if the enroll step below drags out.

## 3. Enroll — swap the token for mTLS certs

Adjust `-hosts` so the Panel can reach this box (LAN hostname + IP work
well; both go into the cert SAN):

```powershell
cd C:\kraken\bin
.\kraken-krakenctl-windows-amd64.exe enroll `
  -panel http://<panel-host>:<port> `
  -token <BOOTSTRAP_TOKEN_FROM_STEP_2> `
  -hosts $env:COMPUTERNAME,192.168.1.42 `
  -out C:\kraken\certs
```

You want `Enrolled. Wrote agent.pem, agent-key.pem, ca.pem to C:\kraken\certs`.

## 4. Open the firewall

Run once, still admin PowerShell:

```powershell
New-NetFirewallRule -DisplayName "Kraken Agent gRPC" `
  -Direction Inbound -Protocol TCP -LocalPort 9090 -Action Allow
New-NetFirewallRule -DisplayName "Kraken SFTP" `
  -Direction Inbound -Protocol TCP -LocalPort 2022 -Action Allow
```

Per-game UDP/TCP ports are opened separately when you deploy each
server (by Kraken's UniFi / Cloudflare integration if you use it, or by
hand).

## 5. Write the config file

`--root` gives the Agent one directory and derives the rest, so the only
things left to state are this node's name and OS. The bundle from step 3
is picked up from `C:\kraken\certs` automatically — no TLS paths to
configure.

```powershell
@'
root: C:\kraken
node_id: windows-01
node_os: windows
windows_isolation: hyperv   # or "process" on a build-matched host
'@ | Set-Content -Encoding utf8 C:\kraken\agent.yaml
```

Confirm what the Agent resolved before starting it — this prints the full
layout and exits:

```powershell
C:\kraken\bin\kraken-agent-windows-amd64.exe --root C:\kraken --print-config
```

`state_dir`, `data_dir`, `backup_dir` and `sftp_host_key` should all sit
under `C:\kraken`, and `tls_cert` / `tls_key` / `tls_ca` should point into
`C:\kraken\certs`. If the TLS lines are missing, step 3 didn't land the
bundle where the Agent looks — all three files must be present before it
is adopted.

Every key also has a flag (`--node-id`) and an environment variable
(`KRAKEN_NODE_ID`); flags beat environment, environment beats the file. See
[`deploy/agent.example.yaml`](../agent.example.yaml) for the annotated
full set.

## 6. Run the Agent

### Foreground — smoke test

```powershell
C:\kraken\bin\kraken-agent-windows-amd64.exe --root C:\kraken
```

Expected: a log line `agent serving with mutual TLS  addr=:9090`. On
the Panel side, **Settings → Nodes** should flip `windows-01` to
**online** within a few seconds.

If it shows **partial** instead, the Agent is connected but can't reach
Docker (the log says so, with the daemon's own error). Start Docker
Desktop — the Agent re-probes on each Panel poll and promotes itself to
online without a restart. A partial node is deliberately not schedulable:
nothing placed there could start.

Ctrl-C to stop when you're done smoke-testing.

### Persistent — install as a Windows Service with `nssm`

`nssm` (Non-Sucking Service Manager) wraps a plain exe into a proper
Windows Service with log rotation and restart-on-failure. With the config
in a file there is no environment block to maintain — the service command
is the same one you just smoke-tested.

```powershell
# One-time install of nssm:
winget install nssm.nssm

# Service:
nssm install kraken-agent C:\kraken\bin\kraken-agent-windows-amd64.exe --root C:\kraken
nssm set kraken-agent AppDirectory   C:\kraken\bin
nssm set kraken-agent AppStdout      C:\kraken\state\agent.out.log
nssm set kraken-agent AppStderr      C:\kraken\state\agent.err.log
nssm set kraken-agent AppRotateFiles 1
nssm set kraken-agent AppRotateBytes 10485760
nssm set kraken-agent Start          SERVICE_AUTO_START

Start-Service kraken-agent
Get-Service   kraken-agent
Get-Content   C:\kraken\state\agent.out.log -Tail 20
```

To change a setting later, edit `C:\kraken\agent.yaml` and
`Restart-Service kraken-agent` — no `nssm set` calls.

## 7. Verify from the Panel

- **Settings → Nodes** shows `windows-01` **online**, `os: windows`.
- The deploy wizard offers this node as a placement target for
  Windows-native specs.
- Try deploying `windemo` (bundled catalog) as a smoke test — should
  install and reach `offline`, then `running` on start.

## 8. Rotate certs later

Mint a fresh bootstrap token, re-run `krakenctl enroll` with the same
`-out C:\kraken\certs` (overwrites the old bundle), and restart the
service:

```powershell
Restart-Service kraken-agent
```

Existing installed servers keep running the whole time; the Agent
process just reconnects to the Panel with the new cert.

## Common gotchas

| Symptom | Cause | Fix |
| --- | --- | --- |
| Panel logs `connect: connection refused` dialing the Agent | Windows Firewall closed, or `-hosts` in the enroll step doesn't match what the Panel resolves | Reopen `9090/tcp`; re-enroll with the actual LAN hostname/IP |
| `krakenctl enroll` returns `invalid bootstrap token` | Token already used or expired | Mint a fresh one; enrol within 15 minutes |
| Agent starts, Panel reports `cert verify failed` | Wrong CA, or clock skew > a few minutes between hosts | Re-enroll to refresh the bundle; check the Windows Time service is running |
| Panel shows the node **partial**, Agent logs `Docker daemon unreachable — serving in a degraded state` | Docker Desktop is off or misconfigured | Start Docker Desktop. The Agent re-probes on every Panel poll and flips itself back to online — no restart, no re-enroll |
| Node still reads **online** minutes after the Agent died | Panel predates the node-health poller | Upgrade the Panel; it now polls every node every 20s instead of only when you press Ping |
| Windows containers won't launch, Hyper-V error | Docker Desktop is in Linux-containers mode | Right-click tray → *Switch to Windows containers* |
