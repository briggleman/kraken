---
name: run-kraken
description: Launch, restart, or preview the Kraken stack (Panel + Agent + web) on a persistent Postgres datastore. Use whenever asked to run/start/boot/spin up/redeploy the app or bring up a live preview. ALWAYS stop the stale Panel first, then rebuild, then redeploy — never start on top of a running instance. Postgres and its data are persistent: keep them intact across restarts.
---

# Running Kraken (clean Panel → persistent Postgres)

Kraken's control plane (**Panel** `:8080`, **Agent** `:9090`) runs as **host processes**
(they need the Docker socket); **Postgres** and the **game servers** are containers.

**State lives in Postgres** (servers, specs, nodes, users, port allocations). The Panel uses an
in-memory store *only* when `KRAKEN_DATABASE_URL` is unset — avoid that for real testing, because
losing Panel state forces game **re-installs** (multi-GB re-downloads) and desyncs the still-running
Agent. **Default to Postgres.**

> **The golden rule for iterating:** to test a change, spin up a **clean Panel** (stop → rebuild →
> restart the Panel process) but **leave Postgres _and_ the Agent running**. Postgres keeps all
> state; the Agent keeps its in-memory server specs (it has no spec-resync, so restarting it orphans
> installed servers). Clean code, intact data, no re-downloads.

## Phase 1 — Stop the stale Panel (only)

The Panel binds `:8080`; a second one fails to bind, and a stale binary serves *old* code (a
"preview" silently shows the previous build). Stop just the Panel for the common case:

**Windows (PowerShell):**
```powershell
Get-NetTCPConnection -LocalPort 8080 -State Listen -ErrorAction SilentlyContinue |
  Select-Object -Expand OwningProcess -Unique |
  ForEach-Object { Stop-Process -Id $_ -Force -ErrorAction SilentlyContinue }
```
**Linux/macOS (bash):**
```sh
pids=$(lsof -ti tcp:8080 2>/dev/null); [ -n "$pids" ] && kill $pids 2>/dev/null
```
Confirm the holder is really the stale `panel` before killing (`Get-CimInstance Win32_Process
-Filter "ProcessId=<pid>"` / `ps -p <pid>`).

**Do NOT** stop Postgres, and **do NOT** `docker compose down -v` (that deletes the `pgdata`
volume and wipes all state). Leave the Agent (`:9090`) and `kraken_*` game containers running.

**Restart the Agent too only when its code changed** — then also stop `:9090`, and be aware its
in-memory specs are lost: servers that were installed need a re-deploy (or live with the no-resync
limitation). When the Agent stays up, its watchdog will resurrect any `kraken_*` container you
`docker rm`, so don't bother force-removing game containers unless you've stopped the Agent.

## Phase 2 — Rebuild (current code)

```sh
go build -o bin/ ./cmd/...      # panel, agent, krakenctl
```
For a live preview the Vite dev server (phase 3) serves web source directly — no web build needed.
Run `go vet ./...` / `gofmt -l` when the goal is verification rather than a quick spin-up.

## Phase 3 — Redeploy

1. **Datastore (persistent, idempotent):**
   `docker compose -f deploy/docker-compose.yml up -d postgres`
   The `pgdata` named volume persists across `up`/`down`/host reboots, so this is safe to run every
   time — it starts Postgres if down and is a no-op if already up. State survives.
   Connection string:
   `KRAKEN_DATABASE_URL=postgres://kraken:kraken@localhost:5432/kraken?sslmode=disable`
2. **Agent** (background, only if not already running): `KRAKEN_NODE_OS=linux KRAKEN_AGENT_ADDR=:9090 ./bin/agent.exe`
   (set `KRAKEN_NODE_OS=windows` when Docker is in Windows-container mode; `KRAKEN_RUNTIME=fake`
   skips Docker entirely). The runtime auto-detects the daemon OS regardless.
3. **Panel** (background): set the DB URL and start it.
   ```sh
   KRAKEN_ENV=dev KRAKEN_QUICKSTART=true KRAKEN_HTTP_ADDR=:8080 \
   KRAKEN_DATABASE_URL=postgres://kraken:kraken@localhost:5432/kraken?sslmode=disable \
   KRAKEN_BOOTSTRAP_ADMIN_USER=admin KRAKEN_BOOTSTRAP_ADMIN_PASSWORD=admin ./bin/panel.exe
   ```
   `KRAKEN_QUICKSTART=true` registers the co-located Agent as the `local` node — **idempotent**, a
   no-op once a node exists. The bootstrap admin is created **once**; after the first run the login
   persists (e.g. `admin` / `admin12345` once rotated) and `KRAKEN_BOOTSTRAP_ADMIN_PASSWORD` is
   ignored. On a brand-new DB the bootstrap admin must rotate its password on first login via
   `POST /api/v1/auth/change-password` (`current_password`/`new_password`, min 8 chars).
4. **Web** (live preview): start via the preview tool using `.claude/launch.json`'s `kraken-web`
   config (Vite `:5173`, proxies `/api` → `:8080`). Use the preview tools, not Bash, so
   screenshots/snapshots work. After a Panel restart the stored auth token is invalid — log in again.
5. **Verify**: `curl :8080/health` → `{"status":"ok"}`, then drive the UI and confirm
   `NODES ONLINE 1/1` and that previously-installed servers are still present.

Report the preview URL (http://localhost:5173) and the working login when done.

## Full reset (rare — only when you *want* to wipe state)

```sh
docker compose -f deploy/docker-compose.yml down -v   # deletes pgdata — all state gone
```
Then bring everything up fresh per Phase 3. Expect to re-import specs and re-deploy/re-install
servers.
