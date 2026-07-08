# CLAUDE.md

Orientation for Claude Code sessions. Keep this lean — it's an index, not a manual.
Deep docs live in the files linked below.

## What this is

**Kraken** — a self-hosted, Pelican-style platform to deploy and manage dedicated
game servers across many hosts via Docker. Go control-plane (Panel) + Go node daemon
(Agent) + React/TS web UI. Postgres = source of truth (sessions/state live here too).

- **Browser ⇄ Panel:** REST (OpenAPI) + WebSocket
- **Panel ⇄ Agent:** gRPC over mutual TLS
- **Browser ⇄ Agent:** direct WebSocket for console/stats, authorized by a short-lived Panel-issued JWT

## Layout (verified)

```
cmd/{panel,agent,krakenctl}/      entrypoints (agent has Linux + Windows builds)
internal/panel/                   api (chi), auth (argon2id), rbac, scheduler, cron,
                                  specs, servers, backups, store (pgx/goose/JSONB)
internal/panel/store/migrate/sql/ goose SQL migrations (NOT a top-level migrations/)
internal/agent/                   docker.go (OS-aware runtime), fileops.go (native host
                                  file ops), backupstore.go, sftp.go, monitor.go (watchdog)
internal/shared/                  domain types, spec schema, gRPC glue (agentpb)
proto/                            .proto definitions (Panel <-> Agent)
web/                              React + TS + Vite UI (design-system/ + src/)
images/                           steam-base, steam-win  (no steam-wine)
specs/                            Game Specs (the "egg" equivalent)
deploy/                           docker-compose.yml (Postgres only),
                                  docker-compose.full.yml (Postgres + Panel +
                                  Agent), panel/agent Dockerfiles, install.sh,
                                  systemd/ units
scripts/                          genproto.sh, seed-dev.sh
```

Gitignored runtime dirs (don't commit, don't treat as source): `bin/`, `certs/`,
`server-data/`, `agent-backups/`, `data/`.

## Commands

There's a `Makefile` at the repo root — `make help` lists every target with
a one-line description. The most useful ones:

| Target | What it does |
| --- | --- |
| `make` (default: `build`) | web bundle + all three Go binaries |
| `make check` | everything CI runs — `fmt` · `vet` · `staticcheck` · web build · `test -race` |
| `make db-up` | start Postgres (persistent volume, safe to re-run) |
| `make dev-panel` / `dev-agent` / `dev-web` | run the three dev processes |
| `make seed` | seed node + Palworld spec + demo server (Panel + Agent must be up) |
| `make images` | build Panel + Agent Docker images locally |
| `make up` / `down` | bring the full compose stack up / down (needs `deploy/.env`) |
| `make clean` | remove `bin/` + generated web assets (committed markers preserved) |

On Windows: `winget install GnuWin32.Make`, or run the recipes from Git Bash /
WSL, or execute the raw commands under the target (any recipe is a one-liner
shell block).

The Panel binary embeds the web UI via `//go:embed` — so `make build-go` alone
serves a "UI not built" stub. Run `make build` (or `make build-web` once, then
iterate on `make build-go`) for the real UI.

Dev login on a fresh DB: `admin` / `admin` (override via
`KRAKEN_BOOTSTRAP_ADMIN_USER` / `_PASSWORD`).

## Conventions that bite

- **Go:** 1.26, module `github.com/briggleman/kraken`. Run `gofmt` before done. Static
  analysis stack: `go vet`, `staticcheck`, `deadcode`, `gosec`, `govulncheck` (all clean).
- **Web:** React + TS + Vite — **no Tailwind**. Never hard-code hex/sizes; use the
  Abyssal CSS variables (`var(--accent)`, `var(--bg-surface)`, `var(--status-running)`…).
  Design-system components are imported via the `@ds` alias (`@ds/components/core/<Name>`),
  each `.jsx` + a sibling `.d.ts`. See **[web/DESIGN.md](web/DESIGN.md)** — the single
  source of truth for the design language.
- **Storage:** server data is host-native via **bind mounts** (`KRAKEN_DATA_DIR/<serverID>`,
  default `./server-data`, mounted to `/data` or `C:\data`). All file ops + backups are
  **native Go** (`internal/agent/fileops.go`) — no Docker archive API.
- **Cross-OS:** the Docker runtime is OS-aware (Linux + Windows containers). Keep agent
  changes working on both; don't assume Linux-only stats/paths/signals.

## Branching & PR workflow

**Never `git push origin main` directly.** Every change lands on a feature
branch, opens a PR, and squash-merges. Direct pushes bypass the `pr-title`
CI gate and leave release-please's release PR in a confusing state.

```sh
git switch -c <type>/<short-name>   # feat/spec-external-repo, fix/off-by-one, docs/…
# … edits + one or more commits …
git push -u origin HEAD
gh pr create --fill                  # title must conform to Conventional Commits
# review → gh pr merge --squash --delete-branch
```

`main`'s branch-protection ruleset is documented in
[`.github/RULESET.md`](.github/RULESET.md). If `gh pr merge` fails with
"base branch policy prohibits the merge" while every CI check is green,
that doc has the diagnosis (Required approvals drifted above 0 on a
solo-maintainer repo).

## Commits & PR titles (Conventional Commits + release-please)

**PRs are squash-merged, so the PR title BECOMES the commit message on `main`.**
release-please reads those commits to compute SemVer bumps and generate the
`CHANGELOG.md`. Non-conforming PR titles fail CI (`pr-title` job).

Format: `type(optional-scope): lowercase subject`

- `feat: add BepInEx toggle` — new user-facing capability → **minor** bump
- `fix: crash watchdog off-by-one` — user-facing bug fix → **patch** bump
- `feat!: rewrite spec schema` — breaking change (also `feat:` + `BREAKING CHANGE:`
  footer) → **minor** bump while pre-1.0, **major** after 1.0
- `docs:`, `chore:`, `ci:`, `refactor:`, `test:`, `build:`, `perf:`, `style:`,
  `revert:` — no version bump, still included in release notes

Subject must start lowercase. Scope (`feat(agent):`) is optional but useful for
mono-repo-style changes. On merge to `main`, release-please maintains an open
`chore(main): release X.Y.Z` PR — merging that PR tags the release, publishes
GitHub notes, and rewrites the annotated line in
[`internal/shared/version/version.go`](internal/shared/version/version.go).
See [`release-please-config.json`](release-please-config.json) for the config.

**Do NOT add a `Co-Authored-By: Claude …` trailer** to commit messages here —
the git log and release-please changelog stay clean without it.

## Pointers

- Design language → **[web/DESIGN.md](web/DESIGN.md)**
- Roadmap / deferred work → **[BACKLOG.md](BACKLOG.md)**
- Security posture & audit history → **[SECURITY.md](SECURITY.md)**
- Project overview → **[README.md](README.md)**
