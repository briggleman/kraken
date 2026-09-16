---
title: Panel from source
description: Build the Panel yourself — the one build-order rule that decides whether you get the real web UI or a stub, and what a development run looks like.
section: install
order: 12
---

For contributors, and for an OS or architecture with no published build. This
is not the path to take for a normal install; use [Docker
Compose](/wiki/install/panel/).

## Requirements

- **Go 1.26+**
- **Node 24+** (any recent LTS builds the bundle; CI uses 24)
- Docker, for Postgres and for actually running game servers

## Build

```sh
git clone https://github.com/briggleman/kraken.git
cd kraken
make build
```

`make build` is web bundle first, then the three Go binaries into `bin/`:
`panel`, `agent`, `krakenctl`. `make help` lists every target.

:::warning
**Build the web bundle first, or you get a stub.** The Panel serves its UI from
a `//go:embed` of `internal/panel/webui/dist`, which a fresh checkout leaves
empty — so `make build-go` alone produces a Panel that serves a "UI not built"
page and nothing else. Run `make build`, or `make build-web` once and then
iterate with `make build-go`.
:::

Without `make` (Windows, say), the recipes are one-line shell blocks and can be
run directly:

```sh
npm --prefix web ci
npm --prefix web run build
go build -trimpath -o bin/ ./cmd/...
```

## Version stamping

Release builds stamp the version through `-ldflags`. `make build` mirrors the
release workflow, so a local build reports the same tag, commit and date a
release build would:

```sh
make version
```

An unstamped `go build` reports `dev`, which is honest and perfectly usable —
but the Panel compares its own version against each Agent's to decide when to
offer an update, so a fleet driven by an unstamped Panel will be quieter about
upgrades than a real one.

## Run it

```sh
docker compose -f deploy/docker-compose.yml up -d   # postgres
./bin/panel
```

With no `KRAKEN_DATABASE_URL` the Panel falls back to an **in-memory store**.
That is a development convenience and nothing else.

:::warning
The in-memory store is not a datastore. Nothing is persisted: every server,
node, user and audit row disappears when the process exits. Never leave an
install in that state and treat it as working — set `KRAKEN_DATABASE_URL`, or
configure the datastore in the first-run wizard.
:::

On a fresh database the dev bootstrap login is `admin` / `admin`, overridable
with `KRAKEN_BOOTSTRAP_ADMIN_USER` and `KRAKEN_BOOTSTRAP_ADMIN_PASSWORD`.

## Agent updates from a source build

The Panel pushes Agent upgrades by streaming a binary it carries inside itself —
it embeds the Agent builds matching its own version, so an Agent can only ever
be moved to the Panel's version. A plain `make build` does not cross-compile
those:

```sh
make embed-agents   # linux/amd64, linux/arm64, windows/amd64 into the embed dir
make build-go
```

Skip it and the update endpoint answers `503`, which is the honest response: the
Panel has nothing to push.

## Checks

```sh
make check
```

Everything CI runs: the web build and unit tests, `gofmt`, `go vet`,
`staticcheck`, `go test -race`, and the wiki staleness gate. Run it before
opening a PR — see
[CLAUDE.md](https://github.com/briggleman/kraken/blob/main/CLAUDE.md) for the
branch and PR-title conventions.
