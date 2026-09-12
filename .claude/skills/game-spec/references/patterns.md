# Patterns cookbook — by engine and OS story

Each recipe names the bundled spec that proves it. Copy the proven block, then change only what
your game's docs tell you to. Where a line exists because something broke, the reason is stated;
keep that reason as a comment in your spec.

## Contents

1. Linux-native SteamCMD (any engine) — Valheim, Palworld, Dragonwilds
2. Unreal Engine servers — Palworld, Dragonwilds (Linux); Abiotic Factor, Windrose (Windows)
3. Unity servers + BepInEx — Valheim (Mono), V Rising (IL2CPP)
4. Windows-only game: windows-native + linux-wine — Abiotic Factor, Enshrouded
5. Non-Steam download — Factorio
6. Config-file games vs launch-arg games
7. Player-count query
8. Deriving the backup block

---

## 1. Linux-native SteamCMD

```yaml
steam_app_ids: { linux: <SERVER_APPID> }
platforms:
  - { kind: linux-native, image: ghcr.io/briggleman/kraken-steam-base:latest }
install:
  script: >-
    steamcmd +force_install_dir /data +login anonymous +app_update {{APP_ID}} validate +quit;
    steamcmd +force_install_dir /data +login anonymous +app_update {{APP_ID}} validate +quit
```

- `;` not `&&`: the second pass must run even though the first exits 8 with `Missing
  configuration`. A single manual run sometimes wins the race, which is misleading — always ship
  the two-step.
- `+force_install_dir` **before** `+login` (SteamCMD warns otherwise and may install elsewhere).
- The image runs as `steam` (uid 1000). The Agent creates the host data dir; if you smoke-test
  by hand, `chmod 777` the scratch dir or the install fails on permissions.
- Startup runs under `/bin/sh -c` (not bash) — avoid bashisms (`[[`, arrays, `source`).

## 2. Unreal Engine servers

UE layout: `/data/<Project>/Binaries/<Linux|Win64>/<Project>Server-<Linux|Win64>-Shipping[.exe]`,
plus a wrapper (`<Project>Server.sh` / `.exe`) at the install root. Config lives in
`<Project>/Saved/Config/<LinuxServer|WindowsServer>/*.ini`, saves in `<Project>/Saved/SaveGames`,
logs in `<Project>/Saved/Logs`, crash dumps in `<Project>/Saved/Crashes`.

### Linux (Palworld, Dragonwilds)

```yaml
startup:
  # UE5 servers dlopen steamclient.so from $HOME/.steam/sdk64; the runtime container is a
  # fresh image copy, so link the SteamCMD-baked lib in and pin HOME so the child binary
  # resolves it too. chmod: SteamCMD does not always preserve the mode bit through the depot.
  command: >-
    export HOME=/home/steam &&
    mkdir -p "$HOME/.steam/sdk64" &&
    ln -sf /home/steam/steamcmd/linux64/steamclient.so "$HOME/.steam/sdk64/steamclient.so" &&
    cd /data/<Project>/Binaries/Linux &&
    chmod +x ./<Project>Server-Linux-Shipping &&
    ./<Project>Server-Linux-Shipping <Project> -log -Port={{PORT_GAME}}
  restart: { on_crash: true, max_retries: 3 }
  stop: { type: signal, value: SIGINT }
```

- Palworld launches through its wrapper `./PalServer.sh` from `/data`; Dragonwilds launches the
  Shipping binary directly with the project name as the first argument. Check what the vendor's
  own Docker image / start script does and mirror it.
- Port flag spelling varies (`-port=`, `-Port=`, `-PORT=`); `-QueryPort=` where a query port
  exists. `-log` makes the server log to stdout (needed for the console + `ready_regex`).
- Config path for the template: `/data/<Project>/Saved/Config/LinuxServer/<File>.ini`. UE ini
  sections look like `[/Script/<Module>.<SettingsClass>]` — copy the header from the game's
  default ini, never invent it.
- Bool rendering: `{{if eq .settings.X "true"}}True{{else}}False{{end}}`.
- Both-OS specs: declare the same ini twice, once under `LinuxServer/` and once under
  `WindowsServer/`. `config_files` has no platform selector; writing both is harmless.

### Windows-native (Abiotic Factor, Windrose, Dragonwilds)

```yaml
startup:
  command: >-
    cd /d C:\data\<Project>\Binaries\Win64
    & <Project>Server-Win64-Shipping.exe <Project> -log -stdout -FullStdOutLogOutput -unattended -Port={{PORT_GAME}} -UserDir=C:\data\<Project>
  stop: { type: signal, value: SIGINT }   # the Linux signal is the convention; see SPECS.md
```

- Launch the **Shipping exe directly**, not the root `<Project>Server.exe`: that wrapper is UE's
  `BootstrapPackagedGame`, which checks DirectX/VC++ prerequisites and pops a modal dialog that is
  invisible and unclickable in Server Core — the container sat in `starting` at 0 MB forever
  (Dragonwilds, 2026-09-12). Abiotic Factor / Windrose still name the wrapper; treat that as a
  known risk, not a pattern to copy.
- `-UserDir=C:\data\<Project>` pins UE's user dir into the bind mount — UE builds marked
  "installed" otherwise write `Saved/` to `%LOCALAPPDATA%\<Project>`, which a fresh container
  loses. `-stdout -FullStdOutLogOutput` mirrors the log to the container console; `-unattended`
  suppresses crash dialogs.
- Check the exe's **static imports** against Server Core before shipping the entry: a missing
  import dies at load with exit `0xC0000135` and no output at all. Parse the PE import table in
  the container (PowerShell can do it in ~30 lines) rather than guessing; the fix goes in
  `images/steam-win/Dockerfile` (stage 1 copies the DLL from the full `windows/server` base),
  never a DLL dropped next to the exe — a desktop-Windows copy has different dependencies and
  fails too (LoadLibrary error 126).
- `cmd /S /C` runs it: no newlines inside the command (fold with `>-`), `&` to chain. PowerShell
  **must** be `powershell -NoProfile -NonInteractive -EncodedCommand <UTF-16LE base64>`: Docker's
  Windows argument escaping turns inner double quotes into `\"`, so `-Command "..."` arrives as a
  quoted string literal and PowerShell echoes it instead of executing it (verified live). Keep
  the decoded text in a comment next to the blob.
- Do **not** add a SteamCMD wait loop to the install script — the Agent guards every
  `windows-native` install against steamcmd.exe's self-update relaunch (prime + wait after each
  pass, PR #283). A plain two-step `steamcmd.exe … & steamcmd.exe …` is all the spec needs.

### Backup for any UE game

```yaml
backup:
  include: [ <Project>/Saved/** ]
  exclude: [ <Project>/Saved/Logs/**, <Project>/Saved/Crashes/** ]
```
`Saved/` holds SaveGames + Config + the server GUID/ids; `Logs` and `Crashes` are the files that
grow mid-archive (the `archive/tar: write too long` failures). Include list source: the
`config_files` path, which already proves `<Project>/Saved/` is inside `/data`.

## 3. Unity servers + BepInEx

- Headless flags: `-nographics -batchmode`. Valheim needs `LD_LIBRARY_PATH=/data/linux64` and
  `SteamAppId=<GAME appid>` exported (the *game* id, not the server id).
- `-savedir` (Valheim) / `-persistentDataPath` (V Rising) pin the save tree; `-logFile` (V
  Rising) moves the log out of the save tree. Set both and derive `backup` from them.
- BepInEx is Unity-only. Mono → BepInEx 5 (Valheim); IL2CPP → BepInEx 6 (V Rising). Never set
  `bepinex_compatible` on an Unreal game.
- Linux/Mono (Valheim): `bepinex_script` fetches the Thunderstore pack (`denikson/
  BepInExPack_Valheim`) with `curl` + `unzip` into `/data`; `bepinex_command` launches through
  the Doorstop **env vars** (`DOORSTOP_ENABLE=TRUE`, `DOORSTOP_INVOKE_DLL_PATH=…/BepInEx.
  Preloader.dll`, `LD_LIBRARY_PATH=/data/doorstop_libs:…`, `LD_PRELOAD=libdoorstop_x64.so`) —
  the pack ships `start_server_bepinex.sh`, not `run_bepinex.sh`, so do not `chmod`/exec a file
  that is not there (a failed `&&` link is masked by SteamCMD's earlier `Success!`).
- Windows/IL2CPP (V Rising): `bepinex_script` pulls `BepInExPack_V_Rising` + `ServerLaunchFix`
  (required for headless) via the Thunderstore experimental API; **no** `bepinex_command` —
  `winhttp.dll` auto-injects on Windows. The bundled vrising.yaml still writes it as one
  `powershell -Command "..."` line, which the `\"` escaping gotcha says cannot execute in a
  Windows container — treat that script as unverified and write new ones as `-EncodedCommand`.
- Backup: add `BepInEx/**` (operator-uploaded plugins are not restored by a reinstall) and
  exclude `**/*.log`.

## 4. Windows-only game: windows-native + linux-wine

Ship both platforms unless wine is known-broken for the game. Pattern (Abiotic Factor,
Enshrouded):

```yaml
steam_app_ids: { windows: <SERVER_APPID> }
platforms:
  - { kind: windows-native, image: ghcr.io/briggleman/kraken-steam-win:ltsc2022 }
  # linux-wine: same Windows depot pulled by the LINUX SteamCMD with the forced platform type,
  # launched through Wine under headless X.
  - kind: linux-wine
    image: ghcr.io/briggleman/kraken-steam-wine:latest
    install_script: |-
      steamcmd +@sSteamCmdForcePlatformType windows +force_install_dir /data +login anonymous +app_update {{APP_ID}} validate +quit
      steamcmd +@sSteamCmdForcePlatformType windows +force_install_dir /data +login anonymous +app_update {{APP_ID}} validate +quit
    startup_command: >-
      wine-headless /data/<Project>/Binaries/Win64/<Project>Server-Win64-Shipping.exe -log -PORT={{PORT_GAME}}
install:
  script: >-
    steamcmd.exe +force_install_dir C:\data +login anonymous +app_update {{APP_ID}} validate +quit
    & steamcmd.exe +force_install_dir C:\data +login anonymous +app_update {{APP_ID}} validate +quit
startup:
  command: >-
    cd /d C:\data\<Project>\Binaries\Win64
    & <Project>Server-Win64-Shipping.exe -log -PORT={{PORT_GAME}} -UserDir=C:\data\<Project>
  stop: { type: signal, value: SIGINT }
```

- This shape is only for games with **no Linux build**: `windows-native` first, `linux-wine`
  second (native before wine is the platform policy in SPECS.md). A game that ships a Linux
  server gets `linux-native` + `windows-native` and no wine entry at all.

- `wine-headless`, never `xvfb-run` (deadlocks as PID 1 — Xvfb's SIGUSR1 is ignored by PID 1
  and wine never launches) and never bare `wine` (32-bit loader, `kernel32.dll c0000135`).
- Launch the `-Win64-Shipping.exe` on both kinds; UE's wrapper exe respawns itself in ways Wine
  dislikes and blocks on a prerequisite dialog under Server Core. Enshrouded's single
  `enshrouded_server.exe` is fine as-is.
- `install_script` uses `|-` (newlines) because it runs under `/bin/sh`; the Windows one uses
  `&` because it runs under `cmd`.
- The wine platform reads config from `/data/...` too — one `config_files` path serves both
  (which is exactly why the path must be logical `/data`, not `C:\data`).
- Validation bar: a real boot is >1 GB RSS and prints game logs; ~24 MB and silent means wine
  never started. Record in the PR whether a client joined.
- Backlog #99 tracks wine live-validation for V Rising and Windrose — check it before
  claiming a platform works.

## 5. Non-Steam download (Factorio)

```yaml
platforms:
  - { kind: linux-native, image: ghcr.io/briggleman/kraken-steam-base:latest }
install:
  script: >-
    curl -sSL <stable-headless-url> -o /tmp/game.tar.xz &&
    tar -xJf /tmp/game.tar.xz -C /data --strip-components=1 &&
    rm -f /tmp/game.tar.xz &&
    mkdir -p /data/saves &&
    { [ -f /data/saves/world.zip ] || /data/bin/x64/factorio --create /data/saves/world.zip; }
```
- No `steam_app_ids`, so never reference `{{APP_ID}}`.
- Make first-run world creation idempotent (`[ -f … ] ||`) — reinstalls rerun the script.
- `curl`, `tar`, `xz-utils`, `unzip` are in the image; nothing else is. If the game needs
  another runtime lib, that is an image change (own PR), not a spec `apt-get`.

## 6. Config-file games vs launch-arg games

| | `variables` + placeholders | `settings` + `config_files` |
| --- | --- | --- |
| Where the value lands | the shell command | a file in `/data` |
| Applies | at next start only | on save (pushed immediately) + before every start |
| Validation | shell-metachar guard + `rules` DSL | field type/min/max/enum + template render |
| Good for | port, world name, player cap when the game only takes flags | anything the game reads from a file; passwords; long lists |
| Watch out | no quotes or `$` in values; `user_editable: false` = fixed | overwrites hand edits; runtime-mutated files (ids, bans) get wiped |

Mixed is normal: Palworld passes `-port={{PORT_GAME}}` and templates everything else.

Template tricks seen in the bundle:
- Conditional blocks that must still yield valid JSON: track a `$prev` flag to place commas
  (enshrouded `userGroups`). The catalog test renders with defaults **and** with all passwords
  set — both shapes must parse.
- Unit conversion in the template (`{{.settings.seconds}}000000000` → nanoseconds).
- Only render an optional key when set: `{{if .settings.ServerGuid}}ServerGuid=…{{end}}`.
- Ports in files: `{{.ports.game}}`; variables: `{{.vars.KEY}}`.

## 7. Player-count query

- Most Steam servers answer A2S_INFO on their query port: `query: { method: a2s, port: query }`
  (Valheim). Some answer on the game port itself — test with a quick A2S client before
  declaring it.
- Palworld does not answer A2S: `query: { method: palworld-rest, port: RESTAPIPort,
  password: AdminPassword }` where both are **setting keys**; the Agent runs `curl` inside the
  container so the REST port stays unpublished.
- Anything else → omit `query`. Adding a new method is Agent code (`internal/agent/query.go`),
  not a spec change.

## 8. Deriving the backup block

Ask, in order:
1. Does a `config_files` path sit inside the save tree? (Palworld, Dragonwilds: `<Proj>/Saved/`)
2. Does the startup command pin the save dir? (`-savedir /data/save`, `-persistentDataPath
   C:\data\save`)
3. Does the rendered config name it? (Enshrouded `"saveDirectory": "./savegame"` relative to the
   working dir `/data`)
4. Does the game write anything else a reinstall would not restore? (`mods/`, `BepInEx/`,
   admin/ban lists next to the config)

Then include exactly those, exclude the log/crash trees that live inside them, and write the
source in the comment above the block. If none of 1–3 answers, **omit the block** — the
built-in policy (whole data dir minus ephemeral) is slow but never loses a save.
