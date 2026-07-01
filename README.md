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
| `KRAKEN_CONFIG_FILE` | `data/panel.json` | On-disk file (mode `0600`) holding the DSN and the auto-generated secrets key — kept **outside** the DB it protects. |
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
| `KRAKEN_SFTP_HOST_KEY` | `sftp_host_key` (cwd) | Path to the SSH host key (ed25519, generated on first run). |
| `KRAKEN_DATA_DIR` | `server-data` | Host directory bind-mounted into containers as `/data` (or `C:\data`); one subdir per server. |
| `KRAKEN_BACKUP_DIR` | `backups` | Local backup destination (before optional replication). |
| `KRAKEN_NODE_ID` | _(hostname)_ | Stable node identity. |
| `KRAKEN_NODE_OS` | _(detected)_ | `linux` or `windows` — the OS this node runs containers for. |
| `KRAKEN_RUNTIME` | _(real Docker)_ | Set to `fake` to run without Docker (dev/testing). |
| `KRAKEN_WINDOWS_ISOLATION` | `hyperv` | Windows container isolation: `hyperv` (default), `process`, or `default` (defer to the daemon). |
| `KRAKEN_NODE_WINE` | _(off)_ | Enable the Wine code path for the node. |
| `KRAKEN_TLS_CERT` / `KRAKEN_TLS_KEY` / `KRAKEN_TLS_CA` | _(unset)_ | mTLS material presented/verified by the Agent. |

## Deploy at home

Kraken's control plane runs as **host processes** (they need the Docker socket), so a
home deployment is: one machine running Docker, Postgres in a container, and the Panel +
Agent as background processes. For a second machine, run just another Agent and enroll it.

**Prerequisites:** Docker, and either the built `panel`/`agent` binaries or Go 1.26+ to
build them. (The web UI is served as static assets from a production build.)

1. **Get the code and build the binaries:**
   ```sh
   go build -o bin/ ./cmd/...          # panel, agent, krakenctl
   npm --prefix web ci && npm --prefix web run build
   ```

2. **Start Postgres:**
   ```sh
   docker compose -f deploy/docker-compose.yml up -d
   ```

3. **Generate a secrets key** (32 random bytes, base64) so secrets survive restarts:
   ```sh
   export KRAKEN_SECRETS_KEY="$(openssl rand -base64 32)"
   ```

4. **Run the Panel** (persisted store + your own admin login):
   ```sh
   export KRAKEN_DATABASE_URL="postgres://kraken:kraken@localhost:5432/kraken?sslmode=disable"
   export KRAKEN_BOOTSTRAP_ADMIN_USER=admin
   export KRAKEN_BOOTSTRAP_ADMIN_PASSWORD='choose-a-strong-password'
   export KRAKEN_QUICKSTART=true                      # auto-registers the local Agent
   ./bin/panel &
   ```

5. **Run the Agent** on the same host (it will be picked up by quickstart):
   ```sh
   export KRAKEN_DATA_DIR=/srv/kraken/server-data     # where game data lives
   export KRAKEN_NODE_OS=linux
   ./bin/agent &
   ```

6. **Open the UI** at `http://localhost:8080`, log in, pick a game, and deploy a server.

**Firewall notes for a home network:**
- Forward each game's **UDP/TCP ports** (shown per server) from your router to the host.
- Keep the **Panel** (`:8080`) and **SFTP** (`:2022`) on your LAN / behind a VPN — do
  not expose them to the public internet without a reverse proxy + TLS.
- Set `KRAKEN_ALLOWED_ORIGINS` to your Panel's real origin if you serve it off-localhost.

To add a **second machine**, run only the Agent there and enroll it with the Panel's CA
using `krakenctl` (see `cmd/krakenctl`); the new node then appears in the fleet.

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

Prerequisites: Go 1.26+, Node 20+, Docker, protoc.

```sh
# (from repo root) — bring up the datastore (Postgres):
docker compose -f deploy/docker-compose.yml up -d

# Run Panel (:8080) and Agent (:9090) on the host (they need the Docker socket).
# Panel uses an in-memory store unless KRAKEN_DATABASE_URL is set.
go run ./cmd/panel
go run ./cmd/agent

# Web UI (Vite on :5173):
npm --prefix web run dev

# Tests / build / proto:
go test ./...
go build -o bin/ ./cmd/...
scripts/genproto.sh        # regenerate gRPC from proto/
scripts/seed-dev.sh        # seed a node + demo server (needs Panel + Agent up)
```

Dev login: `admin` / `admin`. See [deploy/](deploy/) for the datastore stack;
**[CLAUDE.md](CLAUDE.md)** has the full command + convention reference, and
**[SECURITY.md](SECURITY.md)** documents the security posture.

## License

Kraken is licensed under the **GNU General Public License v3.0** — see [LICENSE](LICENSE).
</content>
</invoke>
