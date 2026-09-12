---
name: game-spec
description: Author a new Kraken Game Spec — the YAML "egg" in internal/panel/catalog/bundled/ that tells the Panel + Agent how to install, launch, configure, query, and back up a dedicated game server — end to end, from researching the game's server to shipping a feat(specs) PR and updating the live Panel. Use this whenever someone asks to "add a game spec", "write a spec for <game>", "support <game>", "add <game> to the catalog", "write an egg", "make <game> run on Kraken", or wants an existing bundled spec extended to another platform (linux-wine / windows-native) — even when they only name the game and not the word "spec".
---

# Authoring a Game Spec

A spec is one YAML file under `internal/panel/catalog/bundled/<slug>.yaml`. The Panel
`go:embed`s that directory (`internal/panel/catalog/catalog.go`), so **adding a file means
rebuilding the Panel**; `TestLoadValidates` in `catalog_test.go` runs `spec.Validate()` on every
bundled file, so a malformed spec fails CI rather than an operator's import. At runtime the Agent
runs `install.script` once in a throwaway container and `startup.command` in the long-lived one —
via `/bin/sh -c` on Linux images and `cmd /S /C` on Windows — with the server's data dir
bind-mounted at `/data` (Linux) or `C:\data` (Windows) and every port published **1:1**
(container port = host port = the allocated port your `{{PORT_<NAME>}}` placeholder resolves to).

The house rule that shapes everything: **derive, don't guess**. Every path, port, flag, and log
line in a spec comes from a documented source (the game's server docs, SteamDB, a smoke-test log)
and a one-line comment above the block says which. A backup include list that misses the saves
produces a green backup with nothing in it — the worst outcome this system has.

## Read first

- `internal/panel/catalog/bundled/SPECS.md` — asset URLs, game-vs-server appid, the `backup:` rules.
- `internal/shared/spec/spec.go` + `settings.go` — the schema and `Validate()`; the source of truth
  when this skill and the code disagree.
- The two bundled specs closest to your game (same engine, same OS story). Their comments record
  *why* each line is there — reuse the wording where it still applies.
- References in this skill, on demand:
  - [references/schema.md](references/schema.md) — every block annotated, with a copyable template.
  - [references/patterns.md](references/patterns.md) — cookbook by engine/OS: Linux SteamCMD, UE5,
    Unity + BepInEx, Windows-native, linux-wine, non-Steam downloads, config-file vs launch-arg games.
  - [references/verify-and-ship.md](references/verify-and-ship.md) — tests, `docker run` smoke
    test, throwaway-DB Panel, live deploy, PR and live-instance update.
  - [references/gotchas.md](references/gotchas.md) — the checklist to run before opening the PR.

## Workflow

### 1. Research — fill the dossier before writing YAML

Find the official dedicated-server docs (publisher wiki/Steam guide), the community wiki page, and
the SteamDB pages, then answer all of these. Anything you cannot answer becomes a comment in the
spec or an open question in the PR — not a guess.

| Question | Why it matters in the spec |
| --- | --- |
| Game appid vs dedicated-server appid | Game appid → `banner_url`/`icon_url` (the server appid has no CDN art); server appid → `steam_app_ids` (SteamCMD) |
| Anonymous or login-required on SteamCMD | `install.requires_steam_login`; SteamDB's depot page + the `Not for anonymous users` install error tell you |
| Native Linux build? Native Windows build? | Declare every native build the game ships: `windows-native` and/or `linux-native`. Only when there is **no** Linux build add `linux-wine` so Linux nodes can still host it — wine is the fallback, never preferred over a native build (platform policy in SPECS.md) |
| Engine: Unreal / Unity (Mono vs IL2CPP) / other | Dictates startup flags, config-file location, save tree, backup excludes, and BepInEx eligibility (Unity only) |
| Ports: game / query / RCON, TCP or UDP, adjacency rules | `ports:` names, protocols, defaults; A2S query for player count |
| Config: launch args or a config file? Format? Path per OS? | `variables` (args) vs `settings` + `config_files` (file); UE uses `<Project>/Saved/Config/<LinuxServer|WindowsServer>/` |
| Where do saves live? | `backup.include` — from a config path, a `-savedir`/`-persistentDataPath` arg, or a rendered `saveDirectory` |
| A stable "server is up" log line | `startup.ready_regex` — only if one exists and you have seen it; otherwise omit and say so |
| Graceful stop: signal, or a console command like `quit`? | `startup.stop` |
| Memory floor and comfortable figure; player cap | `resources.min_memory_mb` / `recommended_memory_mb`; max-players field bounds |

### 2. Assets — the two Steam derivatives

Every card uses the same two images, sourced from the **game** appid. Ask the store API (no
scraping, no age gate); pin the URLs verbatim including `?t=<ts>`:

```bash
appid=<GAME_APPID>
curl -sS --get "https://api.steampowered.com/IStoreBrowseService/GetItems/v1/" \
  --data-urlencode "input_json={\"ids\":[{\"appid\":$appid}],\"context\":{\"language\":\"english\",\"country_code\":\"US\"},\"data_request\":{\"include_assets\":true}}" |
  python -c "
import sys, json
a = json.load(sys.stdin)['response']['store_items'][0]['assets']
base = 'https://shared.fastly.steamstatic.com/store_item_assets/' + a['asset_url_format']
hero = a.get('library_hero_2x') or a.get('library_hero')
print('banner_url:', base.replace('\${FILENAME}', hero))
print('icon_url:  https://shared.fastly.steamstatic.com/community_assets/images/apps/$appid/%s.jpg' % a['community_icon'])
"
```

`curl -I` both URLs and confirm `200`. No `assets` block in the response (some publishers hide
the store entry) → leave both fields unset; the UI falls back to a hatch.

### 3. Decide the shape

- **Launch-arg game** (Valheim, Abiotic Factor): `variables:` with `{{KEY}}` placeholders in the
  command. Values are shell-validated — no quotes, `$`, `&`, `;`, etc. — so they are safe to
  interpolate, but that also means a variable can never carry a sentence with punctuation.
- **Config-file game** (Palworld, Enshrouded, Factorio, V Rising): `settings.groups` + a
  `config_files` entry. Prefer `format: template` when the game's file has fixed structure
  (JSON documents, UE `OptionSettings=(...)` tuples); use an adapter format (`ini`, `json`,
  `properties`, …) only when the file really is flat key=value pairs.
- **Both** when the port must be a launch flag but everything else is a file (Palworld's `-port`).
- **Platforms — bias toward the native build for the node's OS.** A Windows node runs the
  native Windows build, a Linux node the native Linux build, so declare every native build the
  game ships (`windows-native` and/or `linux-native`); the node it lands on picks. Add
  `linux-wine` only when no Linux build exists, with per-platform `install_script` /
  `startup_command` overrides (Linux SteamCMD + `+@sSteamCmdForcePlatformType windows`,
  `wine-headless`). List order is scheduler priority: native kinds before `linux-wine`.
- **Same config, two OS-named folders?** `config_files` has no per-platform selector — declare the
  file once per path (`/data/<Proj>/Saved/Config/LinuxServer/X.ini` and
  `/data/<Proj>/Saved/Config/WindowsServer/X.ini`). Both get written; the game reads its own.

### 4. Write the YAML

Copy the template from [references/schema.md](references/schema.md) and the matching recipe from
[references/patterns.md](references/patterns.md). Rules that are easy to get wrong:

- File name = `slug` = lowercase, hyphenated. `name` is the display name (`Get("palworld")`
  resolves by slug; `Load()` sorts by name).
- **Config paths use the logical `/data/...` root**, never `C:\data\...`. The Agent's `safePath`
  maps `/data` → `C:/data` on a Windows node and leaves it alone on Linux/wine; a literal
  `C:\data\x` on a Linux node lands at `/data/C:/data/x` and the game never reads it.
- Placeholders available in commands: your `variables` keys, `{{APP_ID}}` (from
  `steam_app_ids` by OS family — `linux-native` → `linux`, everything else → `windows`), and
  `{{PORT_<NAME>}}` for each port (uppercased name). Unknown placeholders are left as-is, so a
  typo shows up in the rendered command rather than silently blanking.
- Templates see `.settings`, `.vars`, `.ports` (`missingkey=zero`). Bool settings render as the
  strings `true`/`false` — fine for JSON, but UE ini wants `True`/`False`:
  `{{if eq .settings.X "true"}}True{{else}}False{{end}}`.
- SteamCMD installs are **two-step** (`cmd; cmd` on POSIX, `cmd & cmd` on cmd.exe): the first
  pass against a fresh appinfo cache fails with `Missing configuration`; the Agent's log guard
  keys off the second pass's `Success! App ... fully installed` line.
- `startup.stop`: declare the **Linux** signal (`SIGINT`/`SIGTERM`) even for Windows-first
  games — it is the only value either agent can actually honour. Windows containers ignore
  custom signals (daemon shutdown event, 30 s, then kill) and a Linux agent drops a non-POSIX
  name such as `CTRL_SHUTDOWN_EVENT` and falls back to SIGTERM, so a Windows name is dead
  weight everywhere. `type: command` writes the value to the server's stdin.
- `ready_regex` absent → the watchdog marks the server running as soon as the container is up.
  Set it only for a line you have watched scroll by; write the reason either way in a comment.
- `restart: { on_crash: true, max_retries: 3 }` is the house default (0 → agent default of 3).
- `resources`: `min_memory_mb` is the boot floor; the scheduler allocates
  `recommended_memory_mb` when set. Valheim placed at its minimum was OOM-killed generating its
  first world — state the honest running figure.
- `backup:` — data-dir-relative POSIX globs, no leading `/`, no backslashes, `exclude` wins.
  Declaring the block **replaces** the built-in exclude policy, so add your own `Logs/**`,
  `Crashes/**`, `**/*.log` for a save tree that carries them (every Unreal game). Include
  operator-uploaded trees a reinstall would not restore (`BepInEx/**`, `mods/**`). Unsure where
  saves live? Omit the block and say so in a comment.

### 5. Validate — cheapest check first

1. `go test ./internal/panel/catalog/... ./internal/shared/spec/...` — parses, validates, and
   renders every `template` config (JSON paths must parse with defaults *and* with passwords set).
2. Smoke-test the **exact** install and startup strings against the image with plain
   `docker run` (recipes in verify-and-ship.md) before involving the Panel. A UE5 boot is >1 GB
   RSS; a quiet container at ~24 MB means the game never started.
3. Throwaway-DB Panel: build the Panel (embeds your file), run it against a scratch database, and
   deploy on the local node. A fresh DB auto-seeds the whole bundled catalog, so your spec is
   already imported — iterate with `PUT /api/v1/specs/{id}` (YAML body).
4. On the live node: install → start → join from a client if possible → change a setting and
   confirm the rendered file → take a manual backup and check the archive holds the save tree and
   nothing from `steamapps/`. If a boot dies with nothing in the console, read the crash exit
   code the server header shows (decimal + hex; `0xC0000135` = a DLL the image lacks) and open
   the retained install log (the install-log chip, or `GET /api/v1/servers/{id}/install-log`) —
   both survive the server going offline, though not a Panel restart.
5. `make check` only if you touched Go; `gofmt` does not apply to YAML.

### 6. Ship

- Docs: bump the bundled-spec count and list in `PRODUCT.md` ("Nine bundled Game Specs …");
  extend `SPECS.md` only when you introduced a *new convention* (a new format trick, a new engine
  pattern) — not for every game.
- Branch → PR → squash (never push `main`): `git fetch && git pull --ff-only`,
  `git switch -c feat/spec-<slug>`, PR title `feat(specs): add <game>` (Conventional Commits,
  lowercase subject). No `Co-Authored-By` trailer in this repo. Record in the PR body what was
  live-validated and what was not (platform, joinable session, backup contents).
- Live Panel: an existing instance does **not** re-seed. `POST /api/v1/specs` with the YAML body
  for a new spec (409 = slug exists → `GET /api/v1/specs`, find the id, `PUT /api/v1/specs/{id}`).
  Running servers pick the new spec up on their next start/restart (the Panel re-pushes it and
  re-renders config before launching).

## Top gotchas (full list in references/gotchas.md)

1. Image URLs from the **game** appid; `steam_app_ids` from the **server** appid.
2. Two-step SteamCMD always — a single-shot manual run passing is luck, not proof.
3. UE5 on Linux: pin `HOME=/home/steam`, symlink `steamclient.so` into `$HOME/.steam/sdk64`,
   `chmod +x` the `-Linux-Shipping` binary, `cd` into `Binaries/Linux`.
4. UE on Windows — native **or** wine — launch `<Project>/Binaries/Win64/<Project>Server-Win64-Shipping.exe`
   directly, never the root wrapper exe: it is UE's `BootstrapPackagedGame`, which pops a
   prerequisite dialog nobody can click in Server Core (container stuck in `starting` at 0 MB)
   and respawns badly under Wine. Under wine use `wine-headless` (never `xvfb-run` or plain
   `wine`).
5. Bind the allocated port (`{{PORT_GAME}}`) — the spec `default` is only the allocator's hint.
6. Config paths: logical `/data/...`. Backup globs: relative, POSIX, and *derived*.
7. A `backup:` block turns off the built-in excludes — bring your own `Logs/**`.
8. No `ready_regex` you have not seen with your own eyes; comment the omission.
9. Both `bepinex_*` fields only for Unity games; V Rising (IL2CPP, Windows) needs no
   `bepinex_command` because `winhttp.dll` auto-injects.
10. Non-editable variables ignore user overrides; a setting's `default` must pass its own
    type/min/max/enum check or `Validate()` rejects the spec.
11. PowerShell inside a Windows spec must be `powershell -NoProfile -NonInteractive
    -EncodedCommand <UTF-16LE base64>`. Docker's Windows argument escaping rewrites inner
    double quotes as `\"`, so `-Command "..."` reaches PowerShell as a string literal and is
    silently echoed instead of run (observed live; `Write-Host` output also arrives as CLIXML
    noise on stderr).
12. Windows SteamCMD self-updates by spawning a new process and exiting, which used to kill the
    install container mid-download. The Agent now guards every `windows-native` install
    (prime + wait after each `steamcmd.exe` pass, PR #283) — specs must NOT add their own loop.
13. Before declaring a `windows-native` entry, check the Shipping exe's static imports against
    Server Core (a PE import-table scan inside the container is quick; `dsound.dll`,
    `ResampleDmo.dll`, `msdmo.dll` were the Dragonwilds gap). A missing DLL is an image fix in
    `images/steam-win/Dockerfile`, not a spec hack — and a desktop-Windows copy of the DLL does
    not work, its own imports differ.
14. UE Windows builds may write `Saved/` to `%LOCALAPPDATA%\<Project>` instead of the install
    tree; pass `-UserDir=C:\data\<Project>` so saves, config and logs stay in the bind mount
    the `backup:` globs cover.
