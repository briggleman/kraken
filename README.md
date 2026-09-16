# Kraken

Kraken is a self-hosted control plane for dedicated game servers. One **Panel**
drives a lightweight **Agent** on every host you own, and each Agent runs your
servers as Docker containers — on Linux, on native Windows, or under Wine — all
from one declarative **Game Spec**. It is two binaries and Postgres: the Panel
embeds the web UI, so there is no static host, no message broker, no cache and
no Kubernetes. It works with no outbound internet at all. I built it for my own
hosting and kept the features I actually wanted, which is why it is small.

**Documentation: [krakenserver.io/wiki/](https://krakenserver.io/wiki/)**

<!-- SCREENSHOT: the fleet view — two node bands with host metrics, four server cards below them, one running, one installing, one crashed. Fictional server and node names only. -->
<!-- SCREENSHOT: a server drill-in — live console, the vitals row, the players roster. Fictional data only. -->
<!-- SCREENSHOT: the Game Spec editor with a bundled spec open, syntax-highlighted YAML. -->

_Screenshots to follow._

## Features

- **Multi-host fleet.** Deploy and manage servers across many machines from one
  Panel, with three-state node health: `online`, `partial` (the Agent answers,
  its container runtime does not) and `offline`.
- **Declarative Game Specs.** Install script, per-platform image, startup
  command, ports, settings, config templates, backup globs and a player query,
  in one YAML file. Nine ship in the binary.
- **Servers update themselves on start.** Every operator-initiated start or
  restart re-runs the install script first, so a server picks up game updates
  instead of staying on the build it was created with. A per-server "pin build"
  toggle opts one out.
- **Cross-OS.** Linux and native-Windows nodes, plus Wine for Windows-only games
  on a Linux host. Native builds always win.
- **Tunnel mode.** A node can dial *out* to the Panel over one reverse mTLS
  connection and be fully managed with zero inbound firewall rules, behind NAT
  you do not control. Default for new nodes.
- **Live console and stats**, bridged by the Panel, so the browser never talks
  to a node directly.
- **Files and SFTP.** Browse, upload and download a server's data directory in
  the UI, or connect over SFTP with per-server credentials chrooted to it.
- **Backups.** Save data rather than the reinstallable install tree, on demand
  or on cron, with optional off-node mirroring to SFTP or SMB.
- **BepInEx mod support** for Unity games, as a per-spec capability flag and an
  opt-in toggle at deploy time.
- **Auth and RBAC.** argon2id passwords, four roles, per-server object-level
  authorization, and AES-256-GCM encryption for every secret at rest.
- **Optional networking automation** with Cloudflare DNS and UniFi port
  forwards. Both degrade cleanly when unconfigured.

## Architecture

| Component | Tech | Role |
|---|---|---|
| **Panel** | Go (HTTP API + gRPC) | auth/RBAC, spec catalog, scheduling, state of record |
| **Agent** | Go (Docker Engine API) | per-host daemon; runs game servers as containers |
| **Web UI** | Svelte 5 + TS + Vite | embedded in the Panel binary |
| **Postgres** | — | source-of-truth state |

- **Browser ⇄ Panel:** REST (OpenAPI) + WebSocket. Console and stats terminate
  at the Panel, which bridges them to the Agent's gRPC streams.
- **Panel ⇄ Agent:** gRPC over mutual TLS, either direct (the Panel dials the
  Agent) or over a reverse tunnel (the Agent dials the Panel). See
  [docs/design/reverse-connections.md](docs/design/reverse-connections.md).
- Server data lives in a host directory **bind-mounted** into each container, so
  file operations and backups are native Go rather than the Docker archive API.

## Get started

| | |
|---|---|
| **Install** | [krakenserver.io/wiki/install/panel/](https://krakenserver.io/wiki/install/panel/) — Panel and Postgres in Docker Compose, Agents on bare metal |
| **Configure** | [krakenserver.io/wiki/configure/panel/](https://krakenserver.io/wiki/configure/panel/) — every `KRAKEN_*` variable, generated from the source |
| **Operate** | [krakenserver.io/wiki/operate/fleet/](https://krakenserver.io/wiki/operate/fleet/) — the fleet view, servers, files, backups, mods, the audit log |
| **API** | [krakenserver.io/wiki/reference/api/](https://krakenserver.io/wiki/reference/api/) — generated from the OpenAPI document |

## Development

Prerequisites: Go 1.26+, Node 20+, Docker, protoc and GNU make. `make help`
lists every target; the common ones:

```sh
make db-up            # start Postgres (persistent volume)
make dev-panel        # Panel on :8080 (in a second terminal)
make dev-agent        # Agent on :9090
make dev-web          # Vite dev server on :5173 (HMR + /api proxy)
make seed             # seed a node + Palworld spec + demo server
make check            # everything CI runs: fmt · vet · staticcheck · web build · test -race
make build            # web bundle + all Go binaries into bin/
make wiki             # regenerate docs/wiki from docs/wiki-src
```

The Panel binary embeds the web UI via `//go:embed`, so `go build ./cmd/panel`
on its own serves a "UI not built" stub. Run `make build` for a binary that
serves the real UI. On Windows: `winget install GnuWin32.Make`, or run the
recipes from Git Bash or WSL.

Dev login on a fresh database: `admin` / `admin`.

- **[CLAUDE.md](CLAUDE.md)** — commands and conventions
- **[PRODUCT.md](PRODUCT.md)** — who this is for and what it refuses to become
- **[DESIGN.md](DESIGN.md)** — the design language (the single source of truth)
- **[SECURITY.md](SECURITY.md)** — security posture and audit history
- **[CHANGELOG.md](CHANGELOG.md)** — shipped work

Documentation lives in [`docs/wiki-src/`](docs/wiki-src/) as markdown and is
built to [`docs/wiki/`](docs/wiki/) by `make wiki`. CI fails if the committed
output is stale. Edit the markdown, never the HTML.

## License

Kraken is licensed under the **GNU General Public License v3.0** — see
[LICENSE](LICENSE).
