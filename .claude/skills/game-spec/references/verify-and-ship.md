# Verify and ship a Game Spec

Order matters: each step is cheaper than the next and catches a different class of mistake.
Skipping to the Panel with an untested startup string costs a multi-GB install per iteration.

## Contents

1. Static: tests
2. Smoke test the exact strings with `docker run`
3. Throwaway-DB Panel on the local node
4. Live node checklist
5. Docs touch-points
6. Branch, PR, merge
7. Update a running Panel

---

## 1. Static: tests

```sh
go test ./internal/panel/catalog/... ./internal/shared/spec/...
```

- `TestLoadValidates` — every bundled YAML parses (sigs.k8s.io/yaml, json tags) and passes
  `spec.Validate()`.
- `TestBundledConfigsRenderValidJSON` — every `format: template` config renders with default
  settings and again with every `password` field set to a value; any `.json` path must parse in
  both shapes.
- `TestGet` — catalog lookup by slug still works.
- The spec package tests cover the schema itself; run them when you touched anything there.

A YAML parse error names the file; a validation error names the slug and field. Fix and rerun.
`gofmt`/`make check` are only relevant if you changed Go (a new query method, a new format).

## 2. Smoke test with `docker run`

Substitute placeholders by hand (`{{APP_ID}}` → the server appid, `{{PORT_GAME}}` → a free
port, `{{SERVER_NAME}}` → a value) and run the **identical** strings the spec carries, under the
same shell the Agent uses. Pull the image first so a pull does not masquerade as a slow install.

### Linux-native / linux-wine (any Docker host in Linux-container mode)

```sh
S=/tmp/spec-smoke && mkdir -p "$S" && chmod 777 "$S"    # image runs as uid 1000
IMG=ghcr.io/briggleman/kraken-steam-base:latest         # or kraken-steam-wine:latest

# install — /bin/sh -c, exactly like the Agent (the image's own ENTRYPOINT is bash -c)
docker run --rm -v "$S:/data" --entrypoint /bin/sh "$IMG" -c \
  'steamcmd +force_install_dir /data +login anonymous +app_update <APPID> validate +quit; steamcmd +force_install_dir /data +login anonymous +app_update <APPID> validate +quit'

# startup — publish the port 1:1 and cap memory like the scheduler would
docker run --rm --name spec-smoke -m 8g -p 28000:28000/udp -v "$S:/data" \
  --entrypoint /bin/sh "$IMG" -c '<startup.command with placeholders substituted; port 28000>'
```

Watch for: the `Success! App '<id>' fully installed` line on the second pass; the startup log
reaching whatever you intend as `ready_regex`; `docker stats spec-smoke` showing a realistic RSS
(UE5 > 1 GB — a quiet ~24 MB container means the game never launched, typical of wine/`xvfb`
mistakes); `ss -lun` inside the container (`docker exec spec-smoke ss -lun` if present, else
check the game's own "listening on" log line) binding the port you passed. Then test the stop
path: `docker stop -s SIGINT -t 30 spec-smoke` and look for the game's save/shutdown lines.

For wine: same commands with the wine image and `wine-headless /data/...-Win64-Shipping.exe`.
Install via the Linux SteamCMD with `+@sSteamCmdForcePlatformType windows`.

### windows-native (Docker in Windows-container mode, Hyper-V isolation)

```powershell
New-Item -ItemType Directory -Force C:\spec-smoke | Out-Null
docker run --rm --isolation=hyperv -v C:\spec-smoke:C:\data ghcr.io/briggleman/kraken-steam-win:ltsc2022 `
  "steamcmd.exe +force_install_dir C:\data +login anonymous +app_update <APPID> validate +quit & steamcmd.exe +force_install_dir C:\data +login anonymous +app_update <APPID> validate +quit"
docker run --rm --isolation=hyperv -m 8g -p 28000:28000/udp -v C:\spec-smoke:C:\data ghcr.io/briggleman/kraken-steam-win:ltsc2022 `
  "<startup.command with placeholders substituted>"
```
The image's ENTRYPOINT is already `cmd /S /C`, matching the Agent. `0xC0000135` on start means a
DLL the image lacks — an image change, not a spec change (see `images/steam-win/Dockerfile`).

Clean up the scratch dir afterwards; a 20 GB depot on the dev box is easy to forget.

## 3. Throwaway-DB Panel on the local node

Verifies the whole path — import, deploy wizard, install stream, config render, console, backup
— without touching the persistent dev DB or a production Panel. Postgres from `make db-up` hosts
a scratch database alongside `kraken`. The local Agent (`:9090`, real Docker) must be running;
see the `run-kraken` skill for starting it.

```sh
docker exec kraken-postgres psql -U kraken -d kraken -c "CREATE DATABASE kraken_spectest;"
go build -o bin/panel-spectest.exe ./cmd/panel          # embeds your new YAML
KRAKEN_ENV=dev KRAKEN_QUICKSTART=true KRAKEN_HTTP_ADDR=:8080 KRAKEN_TUNNEL_ADDR=off \
KRAKEN_STATE_DIR="$TEMP/kraken-spectest-state" \
KRAKEN_DATABASE_URL='postgres://kraken:kraken@localhost:5432/kraken_spectest?sslmode=disable' \
./bin/panel-spectest.exe
```

- Stop any stale Panel on `:8080` first (run-kraken, phase 1). A fresh DB **logs a generated
  bootstrap admin password** (there is no admin/admin unless `KRAKEN_BOOTSTRAP_ADMIN_PASSWORD` is
  set); the account is `must_change_password`.
- Log in and rotate:
  ```sh
  P=http://localhost:8080
  TOKEN=$(curl -s -X POST $P/api/v1/auth/login -H 'Content-Type: application/json' \
    -d '{"username":"admin","password":"<logged>"}' | python -c "import sys,json;print(json.load(sys.stdin)['token'])")
  TOKEN=$(curl -s -X POST $P/api/v1/auth/change-password -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{"current_password":"<logged>","new_password":"<new 8+ chars>"}' | python -c "import sys,json;print(json.load(sys.stdin)['token'])")
  ```
- A fresh DB with zero specs **auto-seeds the entire bundled catalog** from the binary you just
  built (`SeedCatalog`, latched via `catalog_seeded`). Your spec is therefore already present:
  `curl -s $P/api/v1/specs -H "Authorization: Bearer $TOKEN"` → find it by slug, note `id`.
- Iterate without rebuilding the Panel:
  ```sh
  curl -s -X PUT $P/api/v1/specs/<id> -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/yaml' --data-binary @internal/panel/catalog/bundled/<slug>.yaml
  ```
  (`POST /api/v1/specs` with the same body creates; a duplicate slug is a 409.) The body limit
  is 1 MiB; both accept YAML or JSON.
- Deploy: `POST /api/v1/servers {"spec_id":"<id>","name":"<slug>-01"}` (add `"node_id"` to pin,
  `"install_bepinex":true` to exercise the modded path). Poll `GET /api/v1/servers/<sid>` until
  `state` is `offline` (installed) — `install_failed` carries `last_error`. Then
  `POST /api/v1/servers/<sid>/power {"action":"start"}`.
- Or drive it through the web UI: `preview_start` the `kraken-web` launch config (Vite `:5173`
  proxies `/api` → `:8080`) and set `localStorage.kraken_token` to the token, then reload.
- Teardown: kill the Panel, `docker exec kraken-postgres psql -U kraken -d kraken -c "DROP
  DATABASE kraken_spectest;"`, delete `bin/panel-spectest.exe`, remove the server's data dir
  under `server-data/`. Leave `.claude/launch.json` untouched.

If the deploy is refused with `no node can host this spec`, work the diagnosis order: node
online? → spec platforms vs node OS/wine (a Linux-only spec never lands on a Windows node) →
memory → `ports.ranges` on `GET /api/v1/nodes` (an empty pool is the silent killer).

## 4. Live node checklist

Record each outcome in the PR body; "not validated" is an acceptable answer, silence is not.

- [ ] Install completes on every declared platform (Linux, Windows, wine as applicable).
- [ ] Server reaches `running`; if `ready_regex` is set, it flipped on the expected line.
- [ ] A client joined (or: why not — e.g. P2P-only game, no client available).
- [ ] Console shows game output; stop from the UI produces the game's save/shutdown lines and the
  server reads `offline` (not `crashed`).
- [ ] Change a setting → the rendered file on disk (Files tab / SFTP) shows the new value at the
  path the spec declares (not under a stray `C:/` directory).
- [ ] Manual backup (`POST /api/v1/servers/<sid>/backups` or the Backups drill-in) succeeds and
  the listed captured globs / archive size are consistent with a save tree, not a 20 GB depot.
- [ ] PLAYERS card shows `0/<max>` when a `query` block is declared.
- [ ] `docker port kraken_<sid>` shows `<port>/udp -> 0.0.0.0:<same port>`.

### When a Windows boot dies with nothing in the console

The server header shows the last crash exit code (decimal and hex) and the pane keeps the last
install log behind an "install log" chip (`GET /api/v1/servers/{id}/install-log`); both survive
the server going offline but not a Panel restart (PR #284). Read them before redeploying:

- `0xC0000135` `STATUS_DLL_NOT_FOUND` — a static import the steam-win image lacks. Parse the
  exe's PE import table inside the container to name it, then fix `images/steam-win/Dockerfile`.
- `0xC0000139` entry point not found, `0xC000007B` bad image format — wrong DLL build shadowing
  the real one (e.g. a desktop-Windows copy dropped next to the exe).
- `starting` forever at 0 MB — the UE wrapper exe is blocked on an invisible dialog; launch the
  Shipping exe directly.
- Install "succeeded" but the data dir holds only `steamapps/downloading` — the steamcmd.exe
  self-update relaunch race; the Agent guard (PR #283) should have prevented it, so check the
  install log for its `windows SteamCMD guard applied` line.

Two throwaway diagnostics that paid for themselves: temporarily point `startup_command` at a
PowerShell `-EncodedCommand` script that runs the exe via `Start-Process -Wait -PassThru`,
prints `ExitCode` in hex, and sleeps long enough to read the console; and the same trick to
list the exe's static imports with `Test-Path` against `System32`. Revert the spec afterwards.

## 5. Docs touch-points

- `PRODUCT.md` → "Evidence on Hand": the sentence "Nine bundled Game Specs in
  `internal/panel/catalog/bundled/` — Palworld, …" — bump the count and add the game.
- `internal/panel/catalog/bundled/SPECS.md` — only when the spec introduced a convention future
  authors need (a new format trick, a new engine's save-tree rule). Per-game detail belongs in
  the YAML's own comments.
- `README.md` names a few example games in the "Declarative Game Specs" bullet; update only if
  the new game is a headline.
- The top-level `specs/` directory holds stale copies of `palworld.yaml` and `windemo.yaml` used
  by `scripts/seed-dev.sh`. It is **not** the catalog; do not add your spec there unless Ben asks.
- Memory: if a new gotcha cost real time, add it to the relevant `kraken-*` memory note so the
  next session does not rediscover it.

## 6. Branch, PR, merge

```sh
git switch main && git fetch && git pull --ff-only
git switch -c feat/spec-<slug>
git add internal/panel/catalog/bundled/<slug>.yaml PRODUCT.md
git commit -m "feat(specs): add <game>"           # no Co-Authored-By trailer in this repo
git push -u origin HEAD
gh pr create --fill                               # title must be Conventional Commits, lowercase subject
```

PR body: the dossier (appids, platforms, engine), what was live-validated where, what was not,
and the derivation of the backup block. `feat(specs): …` is the established scope (see `git log
-- internal/panel/catalog/bundled/`). After CI is green: `gh pr merge --squash --delete-branch`.
If the merge is blocked while checks are green, `.github/RULESET.md` has the diagnosis.

## 7. Update a running Panel

The bundled catalog only seeds an **empty** database; an existing Panel never picks a new file
up on its own. After the PR merges and the Panel is redeployed with the new binary:

- New spec: `POST /api/v1/specs -H 'Content-Type: application/yaml' --data-binary @<slug>.yaml`
  (or `POST /api/v1/catalog/<slug>/import`, which reads the bundled copy inside the running
  binary). 409 → it already exists.
- Existing spec (a fix): `GET /api/v1/specs` → id by slug → `PUT /api/v1/specs/<id>` with the
  YAML body. `version` increments server-side; the body replaces the spec wholesale, so send the
  whole file, not a patch.
- Effect on servers: the Panel re-pushes the spec and re-renders config before every
  start/restart, so a running server adopts the change on its next restart. Variables a server
  already stored keep their values; new settings fields take their defaults until saved.
  Backup globs are read on every backup, so those apply immediately.
