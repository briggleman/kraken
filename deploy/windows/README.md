# Kraken — Windows Agent install

For hosts running **Windows-native game servers** (V Rising, Palworld
Xbox variant, other Windows-only titles). The Panel and its Postgres
stay on your Linux / Docker host; the Windows machine runs the Agent
bare-metal as a Windows service and reports back over mTLS gRPC.

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

## One-command install

In the Panel, open **Settings → Nodes → Connect a remote node**, generate
an enrollment token, and pick the **Windows** tab — it renders this
command with the real values filled in. In an **elevated PowerShell**:

```powershell
iwr -useb https://raw.githubusercontent.com/briggleman/kraken/main/deploy/windows/install.ps1 -OutFile $env:TEMP\kraken-install.ps1
powershell -ExecutionPolicy Bypass -File $env:TEMP\kraken-install.ps1 `
  -PanelUrl http://<panel-host>:<port> `
  -Token <BOOTSTRAP_TOKEN> `
  -CaFingerprint <SHA256_FROM_THE_DIALOG>
```

That single run:

1. downloads the latest release binaries and **verifies their SHA-256
   checksums** (hard fail on mismatch),
2. installs them as `C:\kraken\bin\kraken-agent.exe` +
   `kraken-krakenctl.exe`,
3. writes `C:\kraken\agent.yaml` (`node_id` = this computer's name,
   lowercased; `node_os: windows`),
4. opens inbound **TCP 9090 (gRPC) + 2022 (SFTP)** with a **port-based**
   firewall rule (port rules survive binary upgrades; program rules
   silently stop matching),
5. registers `kraken-agent` as a **native Windows service** (delayed
   auto-start, restart-on-failure) — or re-asserts that configuration when
   the service already exists — and starts it,
6. waits for the log to report `agent serving with mutual TLS`.

On first start the agent **enrolls itself**: it generates a key, exchanges
the one-time token for a signed certificate — refusing any CA that doesn't
match the pinned fingerprint — persists the bundle under
`C:\kraken\state`, and reports this host's IPs so the Panel's registration
form prefills itself. Back in the Panel dialog, confirm the address and
register the node.

Useful switches: `-Version v0.17.0` pins a release, `-Root D:\kraken`
relocates everything, `-NoFirewall` / `-NoService` skip those steps.

### Token expired?

Tokens are single-use and expire in 15 minutes (a Panel restart also
invalidates them). Mint a fresh one in the Panel and re-run the same
command — the installer updates the enrollment settings in place and the
agent retries on its next start.

## Day-2 operations

**Upgrading**: the easiest path is the Panel itself — when a node's agent
version drifts from the Panel's, the Nodes page shows an **UPDATE** action
that pushes the Panel's own agent build to the node over mTLS; the agent
verifies the checksum, swaps its binary, restarts, and reverts on its own if
the new build fails to start. Re-running the installer works too:

```powershell
# Upgrade to the latest release (stops, swaps binaries, restarts):
powershell -ExecutionPolicy Bypass -File $env:TEMP\kraken-install.ps1

# Service control (or use services.msc / Get-Service):
C:\kraken\bin\kraken-agent.exe --service stop
C:\kraken\bin\kraken-agent.exe --service start

# Logs (JSON, rotated at 10 MiB):
Get-Content C:\kraken\state\agent.log -Tail 30 -Wait

# Inspect the resolved configuration:
C:\kraken\bin\kraken-agent.exe --root C:\kraken --print-config

# Uninstall the service (binaries + data stay):
C:\kraken\bin\kraken-agent.exe --service uninstall
```

### Existing installs: bring the service's recovery config current

Self-update relies on **SCM recovery actions** to restart the service after it
swaps its binary and exits. Those actions used to be written once, when the
service was first registered — a service registered by an older agent kept its
original (legacy) recovery config through every upgrade, since an upgrade only
replaces the `.exe`. The symptom is a node that goes **offline after the Nth
agent update of a busy day**: the old config restarts the service a few times,
then stops trying, and the new binary sits on disk with nothing to start it
(#184).

`--service install` is idempotent now, so one elevated command re-asserts the
current policy (restart after 5s / 30s / 60s, counter reset after a day,
**including non-crash failures** — self-update's clean exit is one):

```powershell
# Pass the same flags the service was installed with: this also rewrites the
# service's command line. install.ps1 always passes --root.
C:\kraken\bin\kraken-agent.exe --service install --root C:\kraken
```

Re-running `install.ps1` does this for you on every run, so the next upgrade
heals the config with no extra step.

On an agent **older** than this fix, `--service install` refuses while the
service exists; use `sc.exe` directly instead (the #114 drill):

```powershell
sc.exe failure kraken-agent reset= 86400 actions= restart/5000/restart/30000/restart/60000
sc.exe failureflag kraken-agent 1
sc.exe qc kraken-agent          # confirm
```

Configuration changes: edit `C:\kraken\agent.yaml`, then
`Restart-Service kraken-agent`. Every key has a `--flag` and a `KRAKEN_*`
environment variable too (flags beat env, env beats the file) — see
[`deploy/agent.example.yaml`](../agent.example.yaml) for the annotated
full set. `windows_isolation: hyperv` (default) or `process` on a
build-matched host.

## Verify from the Panel

- **Settings → Nodes** shows the node **online**, `os: windows`.
- **Partial** instead of online means the Agent is connected but can't
  reach Docker (the log says so, with the daemon's own error). Start
  Docker Desktop — the Agent re-probes on each Panel poll and promotes
  itself without a restart. A partial node is deliberately not
  schedulable: nothing placed there could start.
- Try deploying `windemo` (bundled catalog) as a smoke test — should
  install and reach `offline`, then `running` on start.

## Rotating certificates

The Panel rotates agent certs automatically as they approach expiry (no
action needed). To force a re-enroll — say, after replacing the Panel's
CA — stop the service, delete `C:\kraken\state\agent.pem`,
`agent-key.pem` and `ca.pem`, mint a fresh token, re-run the installer
with `-PanelUrl/-Token/-CaFingerprint`, and the agent enrolls anew on
start.

## Manual install (appendix)

Prefer the one-command path above. If you need to assemble things by
hand — air-gapped host, custom layout — the moving parts are:

1. **Binaries**: download `kraken-agent-windows-amd64.exe`,
   `kraken-krakenctl-windows-amd64.exe`, and `SHA256SUMS` from a
   [release](https://github.com/briggleman/kraken/releases), verify with
   `Get-FileHash`, and place them under `C:\kraken\bin`.
2. **Enroll**: mint a bootstrap token in the Panel, then either set
   `panel_url` + `enroll_token` (+ `ca_fingerprint`) in `agent.yaml` and
   let the agent enroll itself on start, or run
   `kraken-krakenctl.exe enroll -panel <url> -token <token> -hosts <ip> -out C:\kraken\certs`
   (a complete bundle under `<root>\certs` is adopted automatically).
3. **Config**: `C:\kraken\agent.yaml` with at least `node_id` and
   `node_os: windows`; run with `--root C:\kraken`.
4. **Firewall**: allow inbound TCP 9090 + 2022 (port-based rule).
   Per-game UDP/TCP ports are opened separately when you deploy each
   server (by Kraken's UniFi / Cloudflare integration if you use it, or
   by hand).
5. **Service**: `kraken-agent.exe --service install --root C:\kraken`,
   then `--service start`. (nssm still works if you prefer it, but is no
   longer required.)

A foreground smoke test is just
`C:\kraken\bin\kraken-agent.exe --root C:\kraken` — expect
`agent serving with mutual TLS  addr=:9090`, Ctrl-C to stop.
