# Kraken

<!-- OVERVIEW: intentionally left blank — fill this in. -->

## Overview

Kraken is a personal project with the goal of being something I've built to use for my own hosting needs. It has the features I want and need in a gaming server without all the complexity of the ones that exist today. It's been developed for people like me who want something easy and secure to setup with some nice features to help manage your home stack if you have one. I plan on adding features and fixing things as I go along adding support for both Windows _and_ Linux containers for more games. If you love it drop a star and if you would like to see a feature let me know!

## Features

- **Multi-host game-server fleet.** Deploy and manage dedicated game servers
  across many machines from one Panel. Each host runs a lightweight **Agent**
  that launches servers as Docker containers.
- **Declarative Game Specs** (the "egg" equivalent) — install script, per-platform
  Docker image, startup command, ports, settings, and config-file templates in one
  YAML file. Bundled specs ship for Valheim, V Rising, Palworld, and any
  SteamCMD title from Valve's
  [Dedicated Servers List](https://developer.valvesoftware.com/wiki/Dedicated_Servers_List).
- **Cross-OS.** Linux and native-Windows nodes (Windows containers). The scheduler
  prefers a Linux dedicated server when one exists, and falls back to Windows.
- **Steam auth.** Anonymous installs by default; per-node encrypted Steam credentials
  and a deploy-time Steam Guard code for titles that require an owning account.
- **BepInEx mod support** for Unity games (Valheim / V Rising) — a per-spec capability
  flag plus an opt-in "Install BepInEx" toggle at deploy time.
- **Live console & stats.** Stream a server's console and CPU/memory/player counts in
  the browser over a WebSocket authorized by a short-lived Panel-issued token.
- **File manager + SFTP.** Browse, edit, upload, and download a server's data dir in
  the UI, or connect over **SFTP** with per-server credentials chrooted to that server.
- **Backups & replication.** On-demand and scheduled `tar.gz` backups with dynamic,
  per-game destination paths (`{{SLUG}}` templating) and optional replication to an
  SFTP/NAS target.
- **Scheduling.** Cron-style schedules for power actions and backups.
- **Networking automation.** Optional Cloudflare DNS and UniFi port-forward integration.
- **Auth & RBAC.** argon2id passwords, optional 2FA per spec, role-based access
  (Owner / Admin / Operator / Read-only) with per-server object-level authorization.
- **Encryption at rest.** All infrastructure secrets (API tokens, CA key, SFTP/Steam
  credentials) are AES-256-GCM sealed; session tokens are SHA-256 digests. See
  [SECURITY.md](SECURITY.md).

## Architecture

| Component        | Tech                         | Role                                                        |
|------------------|------------------------------|-------------------------------------------------------------|
| **Panel**        | Go (HTTP API + gRPC)         | Auth/RBAC, game spec catalog, scheduling, state of record   |
| **Agent**        | Go (Docker Engine API)       | Per-host daemon; runs game servers in Docker containers     |
| **Web UI**       | React + TS + Vite            | Manage games, servers, nodes, users                         |
| **Postgres**     | —                            | Source-of-truth state                                       |

- **Browser ⇄ Panel:** REST (OpenAPI) + WebSocket
- **Panel ⇄ Agent:** gRPC over mutual TLS
- **Browser ⇄ Agent:** direct WebSocket for console/stats, authorized by a short-lived Panel-issued JWT

Server data lives in a host directory **bind-mounted** into each container, so the
Agent has native filesystem access for the file browser and backups (no Docker archive
API). The Panel and Agent run as **host processes** (they need the Docker socket);
only Postgres and the game servers themselves are containers.

## Configuration (environment variables)

All configuration is via `KRAKEN_*` environment variables. Nothing below is required
to start in dev — sensible defaults apply — but production deployments should set the
database URL, secrets key, and a bootstrap admin.

### Panel

| Variable | Default | Purpose |
|---|---|---|
| `KRAKEN_HTTP_ADDR` | `:8080` | Panel HTTP/API listen address. |
| `KRAKEN_DATABASE_URL` | _(unset → in-memory)_ | Postgres DSN. **Unset means an in-memory store — data is not persisted.** |
| `KRAKEN_STATE_DIR` | `data` | Directory that groups Panel state (config file, generated CA, secrets key). Set to `/var/lib/kraken` in production; `KRAKEN_CONFIG_FILE` defaults under this. |
| `KRAKEN_CONFIG_FILE` | `${STATE_DIR}/panel.json` | On-disk file (mode `0600`) holding the DSN and the auto-generated secrets key — kept **outside** the DB it protects. |
| `KRAKEN_SECRETS_KEY` | _(auto-generated)_ | base64 of 32 bytes — the AES-256 master key for secrets at rest. Auto-generated to the config file if unset (a warning is logged). **Set this in production.** |
| `KRAKEN_BOOTSTRAP_ADMIN_USER` | `admin` | First admin username (created on first run). |
| `KRAKEN_BOOTSTRAP_ADMIN_PASSWORD` | _(random, logged once)_ | First admin password. If unset, a strong password is generated and logged once. |
| `KRAKEN_SESSION_TTL` | `24h` | Session lifetime (Go duration). |
| `KRAKEN_ALLOWED_ORIGINS` | _(localhost dev)_ | Comma-separated allowed origins for CORS + WebSocket upgrades. Same-origin is always allowed. |
| `KRAKEN_QUICKSTART` | `true` in dev | Auto-register the co-located Agent as the `local` node. |
| `KRAKEN_ENV` | _(empty)_ | `dev` enables quickstart and dev conveniences. |
| `KRAKEN_LOCAL_AGENT_ADDR` | `127.0.0.1:9090` | Address the Panel dials for the co-located Agent (quickstart). |
| `KRAKEN_CA_CERT` / `KRAKEN_CA_KEY` | _(self-signed)_ | Agent-enrollment CA. If unset, a self-signed CA is generated (a warning is logged). |
| `KRAKEN_TLS_CERT` / `KRAKEN_TLS_KEY` / `KRAKEN_TLS_CA` | _(unset)_ | mTLS material for the Panel↔Agent channel. |

### Agent

| Variable | Default | Purpose |
|---|---|---|
| `KRAKEN_AGENT_ADDR` | `:9090` | Agent gRPC listen address (mutual TLS). |
| `KRAKEN_SFTP_ADDR` | `:2022` | Agent SFTP listen address. **Expose only to trusted networks** — it's authenticated but externally reachable. |
| `KRAKEN_STATE_DIR` | `.` (cwd) | Directory that groups Agent state (SFTP host key today). Set to `/var/lib/kraken` in production; `KRAKEN_SFTP_HOST_KEY` defaults under this. |
| `KRAKEN_SFTP_HOST_KEY` | `${STATE_DIR}/sftp_host_key` | Path to the SSH host key (ed25519, generated on first run). |
| `KRAKEN_DATA_DIR` | `server-data` | Host directory bind-mounted into containers as `/data` (or `C:\data`); one subdir per server. |
| `KRAKEN_BACKUP_DIR` | `backups` | Local backup destination (before optional replication). |
| `KRAKEN_NODE_ID` | _(hostname)_ | Stable node identity. |
| `KRAKEN_NODE_OS` | _(detected)_ | `linux` or `windows` — the OS this node runs containers for. |
| `KRAKEN_RUNTIME` | _(real Docker)_ | Set to `fake` to run without Docker (dev/testing). |
| `KRAKEN_WINDOWS_ISOLATION` | `hyperv` | Windows container isolation: `hyperv` (default), `process`, or `default` (defer to the daemon). |
| `KRAKEN_NODE_WINE` | _(off)_ | Enable the Wine code path for the node. |
| `KRAKEN_TLS_CERT` / `KRAKEN_TLS_KEY` / `KRAKEN_TLS_CA` | _(unset)_ | mTLS material presented/verified by the Agent. |

## Deploy

Kraken ships as one binary each for the Panel and Agent — the Panel embeds the
built web UI, so there's no separate static host. Pick a path:

### Path 1 — Docker Compose (recommended)

One command brings up Postgres + Panel + Agent on a Linux host. Panel and Agent
use `network_mode: host` so game ports bind directly on the host. Two flavors:

**Quickstart — copy, edit two lines, run.** `deploy/docker-compose.example.yml`
is a self-contained template with heavy comments explaining every knob. Copy
it wherever you want to run Kraken, replace `CHANGE_ME_openssl_rand_base64_32`
with the output of `openssl rand -base64 32`, optionally set a bootstrap
admin password, then:

```sh
docker compose -f docker-compose.example.yml up -d
```

**Production — secrets separated from the compose file.**
`deploy/docker-compose.full.yml` reads secrets from a git-ignored `.env` so the
compose file itself stays safe to commit. Slightly more setup, cleaner for
real deployments:

```sh
cp deploy/.env.example deploy/.env
echo "KRAKEN_SECRETS_KEY=$(openssl rand -base64 32)" >> deploy/.env
docker compose --env-file deploy/.env -f deploy/docker-compose.full.yml up -d
```

Either way, open `http://<host>:8080`, sign in with the bootstrap admin
(default `admin` + the generated password printed in `docker compose logs
panel`), rotate the password, and deploy a server. Images are published to
`ghcr.io/briggleman/kraken-panel` and `-agent` on every release.

**Mixed mode — containerized Panel + bare-metal Agent.** Skip the compose
`agent` service and run the Agent as a systemd unit instead — handy when you
want systemd-managed lifecycle, run the Agent on a different host, or prefer
to keep the Docker socket out of a container. Because Panel uses host
networking, no compose-file edits are needed:

```sh
# 1) bring up just Postgres + Panel:
docker compose -f docker-compose.example.yml up -d postgres panel

# 2) install the Agent bare-metal (same host, or a remote one):
curl -fsSL https://raw.githubusercontent.com/briggleman/kraken/main/deploy/install.sh \
  | sudo bash -s -- --role agent
sudo systemctl enable --now kraken-agent
```

For a remote Agent, additionally enroll it with the Panel using a bootstrap
token minted from **Settings → Nodes → Add node** in the UI:

```sh
sudo krakenctl enroll -panel http://<panel-host>:8080 -token <one-time-token>
```

### Path 2 — Bare metal + systemd

For hosts that prefer a service-managed binary over a container (or Windows
Agents — Docker Compose is Linux-only). One command downloads the release
binaries, verifies their checksums, drops in a `kraken` system user, and
installs the systemd units:

```sh
curl -fsSL https://raw.githubusercontent.com/briggleman/kraken/main/deploy/install.sh \
  | sudo bash
sudoedit /etc/kraken/panel.env   # optional — a KRAKEN_SECRETS_KEY was generated
docker compose -f deploy/docker-compose.yml up -d       # or your own Postgres
sudo systemctl enable --now kraken-panel kraken-agent
```

Second host? Same command with `--role agent`:

```sh
curl -fsSL .../deploy/install.sh | sudo bash -s -- --role agent
sudo systemctl enable --now kraken-agent
# then enroll it: krakenctl enroll -panel https://panel:8080 -token <one-time>
```

The installer is idempotent — re-running upgrades to the latest release
without clobbering `/etc/kraken/*.env`.

### Path 3 — Build from source

For contributors or anyone on an unsupported OS/arch. Requires Go 1.26+ and
Node 20+.

```sh
npm --prefix web ci && npm --prefix web run build   # populates the panel's embed
go build -o bin/ ./cmd/...                          # panel, agent, krakenctl
docker compose -f deploy/docker-compose.yml up -d   # Postgres only
./bin/panel &                                       # reads data/panel.json by default
./bin/agent &
```

### Firewall notes

- Forward each game's **UDP/TCP ports** (shown per server) from your router to the host.
- Keep the **Panel** (`:8080`) and **SFTP** (`:2022`) on your LAN / behind a VPN — do
  not expose them to the public internet without a reverse proxy + TLS.
- Set `KRAKEN_ALLOWED_ORIGINS` to your Panel's real origin if you serve it off-localhost.

## Repository layout

```
cmd/panel/        Panel entrypoint (control-plane API)
cmd/agent/        Agent entrypoint (node daemon; Linux + Windows builds)
cmd/krakenctl/    CLI (agent bootstrap, admin ops, spec import)
internal/panel/   api, auth, rbac, scheduler, cron, specs, servers, backups, store
internal/agent/   docker (OS-aware runtime), fileops (native host file ops),
                  backups (local/SFTP), monitor (crash watchdog)
internal/shared/  domain types, spec schema, gRPC client/server glue
proto/            .proto definitions (Panel <-> Agent)
internal/panel/store/migrate/sql/   goose SQL migrations
web/              React + TS + Vite UI (design-system/ + src/)
images/           Dockerfiles: steam-base, steam-win
deploy/           docker-compose (Postgres)
```

## Development

Prerequisites: Go 1.26+, Node 20+, Docker, protoc, and GNU make. `make help`
lists every developer target; the common ones:

```sh
make db-up            # start Postgres (persistent volume)
make dev-panel        # Panel on :8080 (in a second terminal)
make dev-agent        # Agent on :9090
make dev-web          # Vite dev server on :5173 (HMR + /api proxy)
make seed             # seed a node + Palworld spec + demo server
make check            # everything CI runs: fmt · vet · staticcheck · test -race
make build            # web bundle + all Go binaries into bin/
make images           # build Panel + Agent Docker images locally
```

The Panel binary embeds the web UI via `//go:embed` — so `go build ./cmd/panel`
on its own serves a "UI not built" stub. Run `make build` (or `npm --prefix web
run build` once, then `go build`) for a binary that serves the real UI at `:8080`.

Windows: `winget install GnuWin32.Make` or run recipes from Git Bash / WSL.

Dev login: `admin` / `admin`. See [deploy/](deploy/) for the full-stack Docker
compose, install script, and systemd units; **[CLAUDE.md](CLAUDE.md)** has the
full command + convention reference, and **[SECURITY.md](SECURITY.md)** documents
the security posture.

## License

Kraken is licensed under the **GNU General Public License v3.0** — see [LICENSE](LICENSE).
</content>
</invoke>
