# Game Spec schema — annotated template + field notes

Source of truth: `internal/shared/spec/spec.go` (top-level blocks, `Validate()`),
`internal/shared/spec/settings.go` (settings fields, config formats, template rendering),
`internal/shared/spec/render.go` (placeholders, variable rules DSL). When this page and the code
disagree, the code wins — update this page.

## Contents

1. Copyable template (every block, annotated)
2. Field notes by block
3. What `Validate()` rejects
4. What the Panel injects at deploy time

---

## 1. Template

Delete the blocks you do not need. Keep the comments that explain a *derivation*; drop the ones
that only restate the schema.

```yaml
name: <Display Name>                      # shown in the UI; Load() sorts the catalog by this
slug: <slug>                              # == file name; lowercase-hyphen; the catalog id
description: >-
  <One paragraph an operator reads on the card: what this is, which appid, which OS story,
  anything they MUST do (paste an owner id, set a role password) before it will boot.>
# Game appid <G> for assets; server appid <S> for SteamCMD. See bundled/SPECS.md.
banner_url: https://shared.fastly.steamstatic.com/store_item_assets/steam/apps/<G>/<hash>/library_hero_2x.jpg?t=<ts>
icon_url: https://shared.fastly.steamstatic.com/community_assets/images/apps/<G>/<hash>.jpg
steam_app_ids:                            # omit entirely for non-Steam games (Factorio)
  linux: <S>                              # used when the server lands on linux-native
  windows: <S>                            # used on windows-native AND linux-wine

platforms:                                # scheduler priority order; each kind at most once
  - { kind: linux-native, image: ghcr.io/briggleman/kraken-steam-base:latest }
  # Windows-only game instead:
  # - { kind: windows-native, image: ghcr.io/briggleman/kraken-steam-win:ltsc2022 }
  # - kind: linux-wine
  #   image: ghcr.io/briggleman/kraken-steam-wine:latest
  #   install_script: |-                 # per-platform override of install.script
  #     steamcmd +@sSteamCmdForcePlatformType windows +force_install_dir /data +login anonymous +app_update {{APP_ID}} validate +quit
  #     steamcmd +@sSteamCmdForcePlatformType windows +force_install_dir /data +login anonymous +app_update {{APP_ID}} validate +quit
  #   startup_command: wine-headless /data/<Proj>/Binaries/Win64/<Server>-Win64-Shipping.exe -log -Port={{PORT_GAME}}

install:
  # Two-step: a fresh SteamCMD fails the first app_update with "Missing configuration";
  # ';' so the second always runs, and its "Success!" line clears the Agent's failure guard.
  script: >-
    steamcmd +force_install_dir /data +login anonymous +app_update {{APP_ID}} validate +quit;
    steamcmd +force_install_dir /data +login anonymous +app_update {{APP_ID}} validate +quit
  # requires_steam_login: true           # Panel injects STEAM_USER/STEAM_PASS/STEAM_GUARD into the
                                          # install env (never persisted); script must use them
  # bepinex_compatible: true             # Unity games only — surfaces the deploy-time toggle
  # bepinex_script: >-                   # appended after script when the operator opts in
  #   ...

startup:
  # <Why each flag exists; which doc says so.>
  command: >-
    cd /data &&
    ./<Server> -port={{PORT_GAME}} -name "{{SERVER_NAME}}"
  # bepinex_command: >-                  # modded launch through the Doorstop loader (Unity only)
  #   ...
  # ready_regex: 'Host_Online'           # ONLY a line you have watched; otherwise omit + comment why
  restart: { on_crash: true, max_retries: 3 }
  stop: { type: signal, value: SIGINT }   # always the Linux signal — see SPECS.md platform notes
                                          # console-quit games: { type: command, value: quit }

variables:                                # launch options → {{KEY}} in commands; shell-validated
  - { key: SERVER_NAME, label: Server name, default: "Kraken <Game>", rules: "string", user_editable: true }
  - { key: MAX_PLAYERS, label: Max players, default: "10", rules: "int|min:1|max:64", user_editable: true }

ports:                                    # name → {{PORT_<NAME>}}; default is the allocator's hint only
  - { name: game, protocol: udp, default: 7777, required: true }
  - { name: query, protocol: udp, default: 27015, required: true }

# query: { method: a2s, port: query }                                      # Steam A2S on a spec PORT name
# query: { method: palworld-rest, port: RESTAPIPort, password: AdminPassword }   # SETTING keys, not ports

settings:                                 # config-file games: groups become tabs in the Settings sheet
  # hot_reload: true                      # game re-reads its files live (only changes the UI copy)
  groups:
    - id: server
      label: Server
      description: >-
        <Only when the group needs a warning the fields can't carry.>
      fields:
        - { key: name, label: Server name, type: string, default: "Kraken <Game>" }
        - { key: maxPlayers, label: Max players, type: int, default: "10", min: 1, max: 64 }
        - { key: password, label: Server password, type: password, default: "" }
        - { key: pvp, label: PvP, type: bool, default: "false" }
        - { key: preset, label: Difficulty, type: enum, options: [Easy, Normal, Hard], default: Normal }
        - { key: publicPort, label: Public port, type: int, default: "7777", read_only: true }

config_files:
  # Logical /data root (never C:\data): safePath maps it per node OS.
  - path: /data/<file>.json
    format: template                      # or: ini | properties | keyvalue | json | env | source-cvar
    template: |
      {
        "name": "{{.settings.name}}",
        "port": {{.ports.game}},
        "maxPlayers": {{.settings.maxPlayers}},
        "pvp": {{.settings.pvp}}
      }
  # Adapter form (flat key=value files):
  # - path: /data/server.properties
  #   format: properties
  #   bindings:
  #     server-name: name                          # shorthand: output key ← setting key
  #     pvp: { from: pvp, map: { "true": "1", "false": "0" } }

# A backup is the save data, not the reinstallable install tree. Source: <the config_files
# path / the -savedir arg / the rendered saveDirectory above>. <Why each exclude.>
backup:
  include:
    - <Proj>/Saved/**
  exclude:
    - <Proj>/Saved/Logs/**
    - <Proj>/Saved/Crashes/**

resources:
  min_memory_mb: 4096                     # floor at which the game BOOTS
  recommended_memory_mb: 8192             # what the scheduler actually allocates when set
```

---

## 2. Field notes by block

### Identity + art
- `name`, `slug` required. `slug` doubles as the catalog id (`catalog.Get(slug)`) and must be
  unique across the live Panel — `POST /api/v1/specs` returns 409 on a duplicate.
- `description` is operator-facing prose. Bundled specs use it for the appid, the OS story, and
  the one thing that will otherwise bite (Dragonwilds' OwnerId, Enshrouded's role passwords).
- `banner_url` = `library_hero_2x.jpg` (fallback `library_hero.jpg`); `icon_url` = community
  icon. Both from the **game** appid. See SPECS.md for the exact curl.

### `steam_app_ids`
Map of OS family → SteamCMD appid. `AppIDFor` picks `linux` for `linux-native` and `windows`
for both `windows-native` and `linux-wine` (`osFamilyForKind` in `handlers_server.go`). Omit for
non-Steam games; `{{APP_ID}}` then stays unrendered, so do not reference it.

### `platforms`
- Kinds: `linux-native`, `linux-wine`, `windows-native`. Each at most once; `image` required.
- Order = scheduler preference. The Panel places on the first kind an eligible node supports.
- `install_script` / `startup_command` on a platform entry override the spec-level strings for
  servers placed on that kind (`InstallScriptFor` / `StartupCommandFor`). Placeholders are
  substituted the same way. Precedence for startup: BepInEx command (when the server is modded)
  > platform override > spec-level.
- Images (all published to GHCR by CI):
  - `ghcr.io/briggleman/kraken-steam-base:latest` — Debian bookworm-slim, non-root `steam`
    (uid 1000), SteamCMD on PATH (`steamcmd` symlink), primed `linux64/steamclient.so`, `curl`,
    `tar`, `unzip`, `xz-utils`. No SDL2 or other game-specific libs.
  - `ghcr.io/briggleman/kraken-steam-wine:latest` — same base + WineHQ 10 (pinned) + Xvfb + the
    `wine-headless` wrapper; pre-initialised win64 prefix; `WINEDEBUG=-all`.
  - `ghcr.io/briggleman/kraken-steam-win:ltsc2022` — Server Core + `steamcmd.exe` on PATH +
    VC++ redist + the desktop DLLs Unity/UE import; PowerShell available.

### `install`
- `script` required. Runs once per install/reinstall in a fresh container of the platform's
  image, cwd `/data` (or `C:\data`), via `/bin/sh -c` or `cmd /S /C`. Keep it idempotent — a
  reinstall reruns it on top of existing data.
- The Agent scans install output for SteamCMD failures (`ERROR! Failed to install app`, `No
  subscription`, `Not for anonymous`) because SteamCMD exits 0 on those; a later `Success! App
  '<id>' fully installed` clears an earlier transient failure. That is what makes the two-step
  safe.
- `requires_steam_login: true` → the Panel refuses to provision unless the node has Steam
  credentials configured, then injects `STEAM_USER`, `STEAM_PASS`, `STEAM_GUARD` into the
  install container's environment only. No bundled spec exercises this yet; if you write the
  first one, reference them as shell env (`$STEAM_USER` / `%STEAM_USER%`), never as `{{...}}`.
- `bepinex_compatible` / `bepinex_script`: Unity games only. When the operator opts in at
  deploy, `bepinex_script` is appended after `script` with an OS-aware separator (`\n` on POSIX
  incl. wine, ` & ` on windows-native).

### `startup`
- `command` required. Runs as the container's PID 1 via the shell entrypoint; cwd is the data
  dir. Prefer `cd` into the binary's own directory when the game resolves relative paths (UE5).
- `ready_regex` optional Go regexp matched against console lines. Present → the watchdog holds
  STARTING until it matches. Absent → RUNNING as soon as the container is up. Invalid → warning,
  treated as absent.
- `restart.on_crash` + `restart.max_retries` (0 → agent default 3). Graceful stops never count.
- `stop` required: `type: signal` with a value the target daemon accepts, or `type: command`
  which attaches to stdin and writes the value. On Windows daemons the signal is ignored (the
  daemon sends its shutdown event, waits the Agent's 30 s timeout, then kills); on Linux a
  non-`SIG*`, non-numeric value falls back to SIGTERM.
- `bepinex_command`: alternate startup used when the server is modded. Empty → `command`.

### `variables`
- Launch options substituted as `{{KEY}}` into install/startup strings (`Render` in render.go;
  `{{ KEY }}` with spaces also matches). Keys must be unique.
- `user_editable: false` variables always use `default` — overrides are ignored.
- Overrides are rejected if they contain any of `` `$;&|<>(){}[]!*?~"'\ `` or a control
  character (CWE-78 guard), then checked against `rules`.
- `rules` mini-DSL, pipe-separated: `string|text|password` (no constraint), `int`,
  `float|number`, `bool`, `min:<n>`, `max:<n>`, `in:a,b,c` (alias `enum:`). Unknown tokens are
  ignored.

### `ports`
- At least one; unique names; `protocol` `tcp`|`udp`; `default` 1–65535; `required` flag.
- The allocated host port is exposed as `{{PORT_<NAME>}}` (name uppercased) and published 1:1.
  `default` is only where the allocator starts looking.
- For a `query` port the A2S code queries `127.0.0.1:<allocated>`; games that hard-wire
  query = game + 1 have worked because the allocator hands out adjacent ports, but nothing
  enforces it — say so in a comment if your game depends on it.

### `query`
- `method: a2s` — `port` names a spec **port**; the Panel resolves it to the allocated host port.
- `method: palworld-rest` — `port` and `password` name **setting keys**; the Agent curls the
  REST metrics endpoint inside the container.
- Omit for games with neither; the UI then shows no PLAYERS card.

### `settings` + `config_files`
- Field types: `string`, `text`, `int`, `float`, `bool`, `enum` (needs `options`), `password`.
  `min`/`max` for numerics, `read_only` to display-but-lock, `help` for a tooltip, `pattern`
  (regex; carried to the UI — server-side validation checks type/min/max/enum).
- Every `default` is validated against its own field (`"true"`/`"false"` for bool, a listed
  option for enum, within bounds for numerics) or the spec fails `Validate()`.
- `hot_reload: true` only changes what the UI says after a save; files are pushed either way.
- `config_files[].path` must be absolute under the logical `/data` root. `format`:
  - `template` — Go `text/template`, data = `{settings, vars, ports}` maps, `missingkey=zero`.
    Required `template` body. A `.json` path must render valid JSON with defaults **and** with
    every password field set (both are tested).
  - `ini` (`[section]` header via `section`), `properties`/`keyvalue` (`k=v`), `env`
    (`K=v`, uppercased), `json` (flat object), `source-cvar` (`k "v"`). These take `bindings`:
    output key → setting key (string shorthand) or `{from, map}` to remap values. Keys are
    emitted sorted; every `from` must be a declared setting.
- Files are rendered and pushed on every settings save and before every start/restart (the
  Panel re-pushes the spec, then `ApplyConfig`). The Agent normalises `\` → `/`, runs
  `safePath`, and writes onto the host-side data dir. Hand-edits to a templated file are
  overwritten — say so in the description when the game writes runtime state into the same file
  (Windrose is deliberately *not* templated for this reason).

### `backup`
- Optional. `include` (empty = everything) then `exclude` (wins), doublestar globs against
  data-dir-relative POSIX paths.
- `Validate()` rejects an empty pattern, a leading `/`, any backslash, or an uncompilable glob.
- Declaring the block replaces `builtinBackupExcludes` (`internal/panel/api/backupglobs.go`:
  `steamapps/{downloading,temp,workshop/downloads,shadercache}/**`, `**/logs/**`, `**/Logs/**`,
  `**/*.log`, `**/*.dmp`, `**/*.mdmp`, `**/CrashDumps/**`, UE `Saved/Crashes`) wholesale.

### `resources`
- `min_memory_mb` ≥ 0 required-ish (0 allowed but pointless). `recommended_memory_mb` optional;
  `AllocMemoryMB()` returns it when > 0, else the minimum, and that becomes the container's memory
  limit. An operator-specified `memory_mb` below the minimum is rejected.

---

## 3. What `Validate()` rejects (so you can pre-empt it)

Missing `name`/`slug`; no platforms; unknown or duplicate platform kind; platform without
`image`; empty `install.script`; empty `startup.command`; `stop.type` not `signal`/`command` or
empty `stop.value`; no ports; duplicate/blank port names; bad protocol; port default out of
range; duplicate variable keys; negative `min_memory_mb`; settings group without `id`; duplicate
or blank setting keys; unknown field type; enum without options; a default that fails its own
field check; config file without `path`, with an unknown `format`, `template` format without a
body, or a binding to an undeclared setting; any malformed backup glob.

## 4. What the Panel injects at deploy time

`buildVars` (handlers_server.go): spec variable defaults → user overrides (editable only) →
`APP_ID` (by placement OS family) → `PORT_<NAME>` per allocated port. The same map is the
runtime container's environment (`Env: server.Vars`), so a startup command may also read them
as `$PORT_GAME` if a wrapper script needs it.
