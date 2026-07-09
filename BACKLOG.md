# Kraken — Backlog

Deferred features and enhancements, roughly in priority order.

## UI
- _All previously-listed UI items are done (2026-06-25): file-editor syntax
  highlighting, rename/copy + Trash (soft-delete/restore), single-file raw
  download, and the read-only settings-field flag._

## Project & community
- **Contribution support: PR template + `CONTRIBUTING.md`.** Deferred pending a decision on
  how open to contributions the project should be. Think through: whether to accept outside
  PRs at all (and under what bar), the contribution workflow (branch/fork, DCO or CLA, commit
  style, the CI gate that already runs on PRs), how new **Game Specs** get proposed/reviewed
  (likely the highest-volume contribution), a Code of Conduct, and issue templates (bug /
  feature / spec request). GPL-3.0 is the license context. Revisit once the direction is set.

## Platform
- **Pull game specs from an external GitHub repo.** Today the catalog is
  `go:embed`ded in the panel binary (`internal/panel/catalog/bundled/*.yaml`),
  so adding or updating a spec requires a Kraken release. Move to a
  pull-at-runtime model: point the panel at a spec-only repo (e.g.
  `briggleman/kraken-specs`) and have it fetch + validate on startup + on a
  schedule (weekly, or via a webhook / manual refresh action). Each entry
  becomes a versioned catalog item — an operator can update Palworld's spec
  without a Kraken redeploy. Design notes: (1) keep the current bundled/
  fallback for offline installs; (2) sign or hash-pin the manifest so a repo
  compromise can't inject a malicious startup command; (3) surface "N updates
  available" on the Catalog page so operators know when to re-import;
  (4) `GET /catalog?source=repo` filter so operators can distinguish bundled
  vs external. This is what the [[SPECS.md]] convention is for — spec authors
  contribute to that repo instead of the main Kraken repo. Highest-volume
  contribution surface in the project once opened up.
- **`steam-win` — swap the VC++ redist installer for a direct DLL side-load.**
  The runtime stage still invokes `vc_redist.x64.exe /quiet /norestart` to install
  `vcruntime140.dll` / `vcruntime140_1.dll` / `msvcp140.dll` / `concrt140.dll`
  plus SxS registration hooks. Extracting just those four DLLs into
  `C:\Windows\System32` is estimated to save **300–800 MB** on top of the
  safe-cleanup pass shipped earlier — but some games may need the SxS
  registration the installer performs, so the swap requires live A/B against
  V Rising (the known-working reference) on a Windows Docker daemon before it
  can ship. Approach: `vc_redist.x64.exe /extract:C:\vc` → `expand` the emitted
  CABs → copy the four DLLs → skip the MSI install. Track boot success + no
  `0xC0000135` under both process and Hyper-V isolation.
- **Wine: validate the remaining Windows-only games per-spec.** The
  `steam-wine` image + per-platform spec overrides shipped 2026-07-09
  (Abiotic Factor live-validated — joinable session under Wine 10). Each
  remaining Windows-only spec gets `linux-wine` only after it live-boots:
  Enshrouded (no confirmed reports, UE5 shader-compile risk), V Rising
  (IL2CPP + BepInEx-under-Wine historically flaky), Windrose (UE5 + P2P
  NAT-punch, unknown). Per game: add the platform entry with
  `install_script`/`startup_command` overrides (copy the abiotic-factor
  pattern: Linux SteamCMD + `+@sSteamCmdForcePlatformType windows`,
  launch via `wine-headless`), deploy, verify ready + a real client join.


### Done (2026-07-09)
- ~~**Wine runtime (`images/steam-wine`) for Windows-only games on Linux nodes.**~~ Shipped.
  Debian + pinned WineHQ 10.0 + primed SteamCMD + pre-initialized win64 prefix, built by
  `steam-images.yml`. Schema: per-platform `install_script`/`startup_command` overrides on
  `Platform` (BepInEx loader > platform override > spec-level). The image bakes in
  `wine-headless` (starts Xvfb itself and execs `wine64` — replaces `xvfb-run`, whose
  SIGUSR1 readiness handshake deadlocks as container PID 1). Abiotic Factor is the first
  validated `linux-wine` spec: live boot on Linux Docker reached a joinable session
  (`Session creation completed`). _Gotchas: WineHQ's 32-bit `wine` launcher can't
  bootstrap a win64 prefix (use `wine64`); non-POSIX stop signals are now ignored on
  Linux agents._
- ~~**Power actions must gate on install state.**~~ Already shipped (predates this pass):
  provisioning failures land in `install_failed` (`StateInstallFailed`), the power handler
  rejects start/restart for it, and reinstall recovery exists in `handlers_server.go`.

### Done (2026-07-01)
- ~~**SFTP server (power-user file access).**~~ Shipped. The Agent runs an SSH/SFTP
  listener (`internal/agent/sftpserver.go`, `KRAKEN_SFTP_ADDR` default `:2022`,
  persisted ed25519 host key) alongside gRPC. Each login uses **per-server credentials**
  (username = server id) and is **chrooted** to that server's data dir via a rooted
  request-server handler (traversal-guarded, cross-OS pure Go). **Password + public-key**
  auth: the Panel generates/stores credentials (`store.ServerSFTP`; bcrypt password hash,
  authorized keys) and pushes them to the Agent in the runtime spec (`SftpAccess`), so
  the Agent authenticates locally. Endpoints `GET /servers/{id}/sftp`,
  `POST …/sftp/password` (returns the secret once), `PUT …/sftp/keys`, `POST …/sftp/disable`
  — all **owner-scoped** (the 2026-07-01 IDOR checks) + `PermServerConfig`. The node's SFTP
  port rides `NodeInfo.sftp_port`; the **Files tab** shows an SFTP card (connection command,
  reset password, authorized keys, disable). Credential material is stripped from general
  server API responses (`serverView`). Verified: agent unit tests (round-trip, jail-escape,
  bad-password) + live key-auth connection listing the chrooted data dir.

### Done (2026-06-30)
- ~~**BepInEx (modded) game variants.**~~ Shipped. A per-spec **`install.bepinex_compatible`** flag
  (spec editor → Install section, a toggle above the Steam-login/2FA toggle) gates an opt-in **"Install
  BepInEx mod support"** toggle in the deploy wizard's Configure step. Choosing it persists the server's
  `bepinex` flag, appends **`install.bepinex_script`** (download + unzip the pack into `/data`) after the
  vanilla install, and launches via **`startup.bepinex_command`** (the Doorstop loader) — falling back to
  the vanilla command otherwise. No new image: BepInEx lands in `/data` (manageable in the Files tab,
  covered by backups/replication). Bundled example: **Valheim** (denikson BepInExPack_Valheim 5.4.2202,
  BepInEx 5 Mono). Verified end-to-end: modded Valheim boots through Doorstop with BepInEx 5.4.22 + a
  plugin loaded; the vanilla path is unchanged when the toggle is off. _Gotcha: the denikson pack ships
  `start_server_bepinex.sh` (not `run_bepinex.sh`) and no `unstripped_corlib`; we launch via the Doorstop
  env directly._
- ~~**BepInEx for V Rising (IL2CPP / Windows).**~~ Bundled 2026-07-01. `vrising.yaml` sets
  `bepinex_compatible: true` + a Windows `bepinex_script` (PowerShell via cmd) that fetches the latest
  **BepInExPack_V_Rising** (IL2CPP 6) + **ServerLaunchFix** (required for headless V Rising) from the
  Thunderstore API into `C:\data`. No `bepinex_command` — Windows auto-loads `winhttp.dll`, so the
  vanilla startup is used. The install-append separator in `provision` is now OS-aware (` & ` for
  windows-native, `\n` for POSIX). Spec validates + imports with the flag; **live boot needs a Windows
  node** (this dev env is Docker-Linux) — not verified end-to-end here.

### Done (2026-06-29)
- ~~**Network-share backup target (NAS without SFTP).**~~ The UniFi UNAS Pro officially serves only
  **SMB/NFS** (rsync/SFTP are unofficial, and rsync would break Windows agents). Since the backup
  target is already a directory written via native, cross-OS Go file ops, added a first-class
  **`share`** target: the operator mounts the NAS share on the host (SMB on Windows, SMB/NFS on Linux)
  and points Kraken at the mount path. `shareBackupTarget.verify()` requires the path to be a mounted,
  writable dir (never auto-created) so a bad/unmounted share fails loudly on save (`apply_ok=false`).
  Node settings gains a **"Network share (SMB/NFS)"** option + SHARE MOUNT PATH field. Verified:
  valid path → `primary=share` ok; unmounted path → clear "is the share mounted?" error. **rsync was
  intentionally dropped** (unofficial on UNAS + not cross-OS). SFTP target kept for SSH-capable remotes.
- ~~**Slim the Linux game image** (~558→463 MB).~~ Switched to the glibc built-in **`C.UTF-8`** locale
  (dropped the `locales` package + `locale-gen`) and removed game-specific **SDL2** from the base
  (Source-engine specs re-add it in a game layer). Distroless/Alpine ruled out — game servers need a
  shell + **glibc** + 32-bit libs + SteamCMD's userland, so the base stays `debian:bookworm-slim`.
- ~~**Async backups + "backup, then rsync the tar.gz".**~~ Backups were one
  synchronous Panel→Agent RPC (tar+gzip → local → SFTP upload) that timed out on
  real game servers (a 3.6 GB Palworld install blew past the deadline, leaving a
  truncated remote archive). Split into two steps: `CreateBackup` now returns
  immediately with a **PENDING** record and the Agent archives in a detached
  goroutine (`runBackup`, 2 h budget), flipping to **READY**, then mirrors the
  finished archive off-node as a separate best-effort step. New `BackupState` +
  `ReplicationState` (proto) ride on `BackupInfo`; an in-memory job tracker merges
  with the on-disk listing so `ListBackups` reports archiving + replication state.
  The SFTP `Put` is now **rsync-like**: skip-if-present (size match), atomic
  `.part`→rename publish, and resume-on-size for seekable sources. Panel returns
  `202`; the web Backups tab shows per-archive **Ready/Archiving/Failed** +
  **Mirrored/Replicating** chips and polls until settled. Verified E2E against a
  real SFTP remote: 3.6 GB server → create returns in 0.03 s → local READY (3.08
  GiB) → remote **byte-identical** mirror; agent/panel/shared build+vet+test green.

### Done (2026-06-27)
- ~~**Remote backup replication ("rsync").**~~ Pure-Go **SFTP `backupTarget`**
  (`internal/agent/sftp.go`, `crypto/ssh` + new `github.com/pkg/sftp`) alongside
  the local target: new backups land on the remote when the node's target is `sftp`, and
  a `replicate_to_sftp` flag mirrors every new backup off-node regardless of the
  primary target. A scheduled per-server **"replicate"** cron action
  (`store.ScheduleReplicate` → Agent `ReplicateBackups` RPC) mirrors existing
  archives (skips ones already present). Config (host/path/password or PEM key)
  comes from per-node settings (below). Verified: in-process SSH/SFTP round-trip +
  jail-escape test (`sftp_test.go`); live off-node landing needs a real SFTP remote
  (manual).
- ~~**Settings page — both tiers complete.**~~ _Panel-global remaining:_ session
  TTL, allowed WS origins, and a bootstrap-admin policy toggle now live on the admin
  Settings page (`store.Settings` + resolution helpers `Server.sessionTTL` /
  `allowedOrigins`); each field reports/locks as **ENV-MANAGED** when its `KRAKEN_*`
  env var is set (env always wins). _Per-node config:_ a new `node_config` JSONB
  store (`store.NodeConfig`, one row/node, credential fields AES-GCM encrypted at
  rest) holds the backup target (local/SFTP), dirs, and creds — the Agent's
  `KRAKEN_*` backup env vars are now pre-reconcile defaults only. The Panel **pushes**
  it via a new `ApplyNodeConfig` RPC on each node reconcile and on every save (which
  doubles as a reachability test); the Agent hot-swaps its backup target behind a
  mutex. Edited from a per-node **Node settings** panel on the Nodes page (gear →
  modal). Verified: encryption-at-rest (`TestPostgresNodeConfigEncryptionAtRest`),
  settings-precedence tests, typecheck + build.

### Done (2026-06-26)
- ~~**Encryption at rest for stored secrets.**~~ Every secret persisted to Postgres
  is now protected. AES-256-GCM (`internal/panel/secrets`) seals the Cloudflare token,
  UniFi API key, and the CA private key; session tokens are SHA-256-hashed
  (`store.HashToken`); user passwords stay argon2id. Master key resolves from
  `KRAKEN_SECRETS_KEY` (base64, 32 bytes) or auto-generates to `data/panel.json`
  (0600, outside the DB). Marker prefixes (`enc:v1:` / `ENC1`) migrate legacy
  plaintext transparently on next write. Wired into the Postgres store only (in-memory
  store is RAM). Verified E2E (`TestPostgresEncryptionAtRest`): raw columns hold
  ciphertext / digests, all round-trip on read. See SECURITY.md.
- ~~**Bring-your-own Postgres (setup + settings).**~~ Operators connect their own
  Postgres from the UI: the setup wizard's new first **Database** step takes
  host/port/user/password/db/sslmode, **Test connection** (`Probe`), then **Connect &
  restart** — which creates the `kraken` DB if missing (`migrate.EnsureDatabase`), runs
  goose migrations, persists the DSN to a local config file (`config.SaveDatabaseURL`,
  `KRAKEN_CONFIG_FILE`, 0600), and exits cleanly so a supervisor restarts onto Postgres
  (`api.WithRestart` + main's restart channel). DSN precedence: `KRAKEN_DATABASE_URL`
  env (locks the UI) → config file → in-memory. Settings shows a **view-only** Database
  card. Endpoints `GET/POST /setup/database` + `/setup/database/test`. Verified E2E:
  auto-create + migrate (integration test), in-memory→connect→restart→Postgres, data
  persisted, env-locked → 409. _Note:_ the DB password lives in the config file
  (`data/panel.json`, 0600), not the DB — it bootstraps the DB connection, so it
  can't be encrypted at rest in the DB it configures. _Removed unused **Redis**_
  (compose/README/CLAUDE.md).
- ~~**External-IP detection + UniFi port forwarding.**~~ Agents report their WAN IP
  via an egress echo (`NodeInfo.external_ip`; `agent.ExternalIP`), adopted on node
  reconcile and overridden by the UniFi gateway's WAN IP when configured; used for
  the connect address / PRIVATE-PUBLIC pill and as the Cloudflare DNS target (LAN IP
  stays the port-forward target). UniFi integration: settings (URL/key/site) +
  `internal/panel/unifi` client (`X-API-KEY`, port-forward CRUD, WAN IP), per-port
  open/close toggles on the server **Networking** tab (`POST /servers/{id}/forwards/{port}`),
  and rule cleanup on server delete (`cleanupServerExternal`). Verified E2E: real WAN
  IP detected + pill flips to PUBLIC, 503 gating, empty states; live forward creation
  needs a real UniFi gateway (manual). _Risk noted:_ assumes the API key works on the
  classic `/rest/portforward` endpoint (surfaced via Test connection).
- ~~**Cloudflare DNS for game servers.**~~ Admin Settings page stores a scoped
  Cloudflare API token (`panel_settings` JSONB store; `settings.view/manage` perms;
  `GET/PUT /settings` + `POST /settings/cloudflare/test`). A per-server **Networking**
  tab publishes a DNS name via a small Cloudflare client (`internal/panel/cloudflare`):
  an A/CNAME (host, DNS-only) + optional **SRV** record for the game port, pointing at
  the node's public host. `GET/PUT/DELETE /servers/{id}/dns`; `Server.DNS` stored in
  JSONB (record IDs for update/remove). Verified: settings round-trip, 503 gating, empty
  state, form; live record creation needs a real token+zone (manual). _Stale-record
  reconcile added 2026-06-27:_ when a node's external/public host changes on reconcile,
  `reconcileNodeDNS` re-points the host A/CNAME of every server on that node
  (`cloudflare.UpdateHostRecord`, PUT in place; SRV is name-based so unaffected).
- ~~**Initial setup / first deploy.**~~ Guided first-run experience: forced admin
  password change off the bootstrap credential (`users.must_change_password` +
  `requirePasswordCurrent` gate + `POST /auth/change-password`); auto-generated +
  persisted Agent-enrollment CA when `KRAKEN_CA_CERT/KEY` are unset (`cluster_ca`
  table, `ensureCA` in `cmd/panel`); single-host **quickstart** that auto-registers
  the co-located Agent as the `local` node and brings it online (`AutoRegisterLocalNode`,
  `KRAKEN_QUICKSTART`); loopback-gated `POST /setup/local-enroll`; bundled starter
  **catalog** (`internal/panel/catalog`, `GET /catalog` + `POST /catalog/{id}/import`);
  `GET /setup/status`; and the web onboarding wizard (`web/src/pages/Setup.tsx`,
  `ChangePassword.tsx`) — secure admin → connect node → import game → deploy. Verified
  E2E: fresh install → running server, `setup_complete:true`.

### Done (2026-06-25, cont.)
- ~~**Windows file management** (the Docker archive-API/volume limitation).~~ Root
  cause: `CopyTo/FromContainer` on Windows targets the container layer, not the
  mounted volume. Fixed by moving server data off Docker named volumes onto **host
  bind mounts** (`KRAKEN_DATA_DIR/<serverID>`, default `./server-data`): the Agent
  now does ALL file ops + backups as **native Go filesystem operations**
  (`internal/agent/fileops.go`) — no archive API, no busybox/cmd helpers, no `du`
  exec. Works identically on Linux and Windows; recursive copy, move, delete, zip,
  read/write, and backup/restore all persist to the host dir. Unit-tested without
  Docker (`fileops_test.go`) + verified live on a Windows daemon (ground-truth host
  dir). This also removed the busybox copy/move/delete helper entirely.

### Done (2026-06-25)
- ~~**Windows-native** game servers (Windows nodes / containers).~~ Agent runtime
  is OS-aware (`internal/agent/docker.go`): `cmd /S /C` entrypoint, `C:\data`
  mount, no custom stop signal, disk-sampling skipped, busybox file-helpers
  guarded. `images/steam-win/Dockerfile` (Server Core + SteamCMD) is the prod
  image; `specs/windemo.yaml` (nanoserver) is the demo. Verified E2E on a Windows
  daemon: windows-native placement → install → ready→running → console → stop →
  crash-watchdog auto-restart. _(Windows **file management** is its own open item
  above — a Docker archive-API/volume limitation, not a quick helper.)_
- ~~Crash watchdog + auto-restart; `ready_regex` → state transitions.~~ Agent
  watchdog (`internal/agent/monitor.go`) distinguishes operator stop from crash,
  auto-restarts up to `max_restarts`; Panel reconciler syncs state to the UI.
- ~~Scheduled tasks (cron: restart, backup, command broadcast).~~ `internal/panel/cron`
  parser + Panel scheduler + server **Schedules** tab.
- ~~Audit log + Prometheus metrics.~~ Audit middleware + admin Audit page +
  `GET /audit`; `GET /metrics` (Prometheus text format).
- ~~Backups → object storage (S3).~~ _Removed 2026-06-27_ — the SFTP `backupTarget`
  (remote replication, above) covers off-node backups for NAS/self-hosted setups, so
  the hand-rolled SigV4 S3 client + `KRAKEN_S3_*` env were deleted. The `backupTarget`
  interface remains (local + SFTP).
- ~~mTLS cert rotation + Agent bootstrap/enrollment (`krakenctl`).~~ One-time
  bootstrap tokens + `POST /agents/enroll`; `krakenctl enroll` (re-enroll = rotation);
  Panel signs via `KRAKEN_CA_CERT`/`KRAKEN_CA_KEY`.
- ~~OpenAPI spec for the Panel API.~~ `internal/panel/api/openapi.yaml`, served at
  `/openapi.yaml` + Swagger UI at `/docs`.

## Security (see SECURITY.md)
- ~~**Object-level authz / IDOR**~~ **Fixed 2026-07-01.** Per-server `OwnerID` +
  ownership checks on every server-scoped endpoint (`authorizeServer`/`mayAccessServer`);
  owner or `server.any` role (Owner/Admin) reaches a server, Operator/Read-only are
  scoped to their own. List filtered; denials 404. Test `TestServerOwnershipIsolation`.
  _Follow-up (deferred): a full team/org model (shared ownership beyond the creator)._
  _(The 2026-06-24 audit's other items — WS origin, token-in-URL, mTLS default,
  bootstrap password, per-variable Rules — were fixed 2026-06-25.)_
