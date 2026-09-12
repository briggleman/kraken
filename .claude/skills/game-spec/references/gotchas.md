# Gotchas checklist — run before opening the PR

Every line here cost a real debugging cycle; the source is a comment in a bundled spec, Agent
code, or a `kraken-*` memory note. Tick each one against your YAML.

## Appids and assets
- [ ] `banner_url` / `icon_url` come from the **game** appid; `steam_app_ids` from the
  **dedicated-server** appid. The server appid has no store CDN entry — its URLs 404.
- [ ] Both URLs return 200 (`curl -I`) and are pinned verbatim with their `?t=` timestamp.
- [ ] `steam_app_ids.windows` is set when a `linux-wine` platform exists — wine placements
  resolve `APP_ID` from the `windows` key, not `linux`.

## SteamCMD install
- [ ] Two-step `app_update`: `cmd; cmd` under `/bin/sh`, `cmd & cmd` under `cmd.exe`, two
  lines under a `|-` block. The first pass fails with `Missing configuration` (exit 8 on Linux);
  the Agent's exit-0-but-app-failed guard is cleared by the second pass's `Success!` line.
- [ ] `+force_install_dir` precedes `+login`.
- [ ] Anonymous vs login: `Not for anonymous users` in the install log means
  `requires_steam_login: true` and a node with Steam credentials. `Missing configuration` does
  **not** mean that — it is the two-step race.
- [ ] `{{APP_ID}}` only referenced when `steam_app_ids` exists (non-Steam games leave it unrendered).
- [ ] The script is idempotent — reinstall reruns it on populated data.

## Startup — Linux
- [ ] Runs under `/bin/sh -c`, not bash: no `[[ ]]`, arrays, or `source`.
- [ ] UE5: `export HOME=/home/steam`, `mkdir -p $HOME/.steam/sdk64`, `ln -sf
  /home/steam/steamcmd/linux64/steamclient.so …`, `chmod +x` the `-Linux-Shipping` binary, `cd`
  to its directory. The runtime container is a fresh image copy — nothing from the install
  container's environment survives.
- [ ] Unity: `LD_LIBRARY_PATH` to the game's bundled libs and `SteamAppId=<GAME appid>` when
  the game needs it (Valheim).
- [ ] Binds `{{PORT_GAME}}` (and `{{PORT_QUERY}}` where applicable). The spec `default` is only
  the allocator's hint — a hard-coded port inside the container is a dead NAT remap.
- [ ] Stop signal is a real POSIX name (`SIGINT`/`SIGTERM`); a Windows name silently becomes
  SIGTERM on Linux.

## Startup — Windows-native
- [ ] Single line (`>-`), `cmd` syntax: `&` to chain, no `&&` reliance across newlines.
- [ ] Launch `<Project>\Binaries\Win64\<Project>Server-Win64-Shipping.exe` directly — the root
  wrapper exe is UE's bootstrapper and hangs on an invisible prerequisite dialog in Server Core.
- [ ] `-UserDir=C:\data\<Project>` so UE's `Saved/` stays inside the bind mount.
- [ ] PowerShell only as `powershell -NoProfile -NonInteractive -EncodedCommand <UTF-16LE
  base64>` — `-Command "…"` is silently echoed, not run (Docker escapes the inner quotes as `\"`).
  Put the decoded command in a comment.
- [ ] No hand-rolled SteamCMD wait loop; the Agent applies the self-update guard to every
  `windows-native` install (PR #283).
- [ ] `stop` declares the Linux signal (`SIGINT`); Windows daemons ignore custom signals anyway
  (shutdown event, 30 s, kill) and a Windows-only name is dead weight on a Linux agent.
- [ ] Static imports checked against Server Core (PE import-table scan in the container).
  `0xC0000135` = a DLL the image lacks — fix `images/steam-win/Dockerfile`, don't drop a DLL next
  to the exe (a desktop-Windows copy fails with its own missing dependencies).
- [ ] Headless V Rising's `EOS session creation failed! NotFound` is normal (server-browser
  listing), not a startup failure.

## Startup — linux-wine
- [ ] `wine-headless …`, never `xvfb-run` (PID-1 deadlock: Up at 0 % CPU forever) and never
  bare `wine` (32-bit loader).
- [ ] Launch the `-Win64-Shipping.exe`; the UE wrapper exe respawns badly under Wine.
- [ ] `install_script` uses the **Linux** `steamcmd` with `+@sSteamCmdForcePlatformType windows`
  and `/data`, not `steamcmd.exe` / `C:\data`.
- [ ] Verified RSS > 1 GB and real game log lines; ~24 MB silent = never started.
- [ ] `WINEDEBUG=-all` is baked into the image — do not re-enable fixmes in the command or the
  console and `ready_regex` scanner drown.

## Readiness, restart, stop
- [ ] `ready_regex` only for a line you observed in a smoke test; comment either the line's
  meaning or why there is none. Absent → running as soon as the container is up.
- [ ] Regex is Go syntax and YAML-quoted (`'to\(InGame\)'`).
- [ ] `restart: { on_crash: true, max_retries: 3 }` unless the game has a reason not to.
- [ ] Heavy games (Palworld) take longer than the Panel's power RPC deadline to stop
  gracefully; a UI "restart" can time out before the recreate. Note it in the description if the
  game is known to save slowly.

## Ports
- [ ] Unique names, correct protocol (most game traffic is UDP; RCON is TCP), sane defaults from
  the game's docs.
- [ ] A separate query port only when the game really has one (Enshrouded collapsed to a
  single port; Windrose has none). Reading `.ports.query` for a port that doesn't exist renders
  `0` in a template.
- [ ] `query: { method: a2s, port: <PORT NAME> }` vs `palworld-rest` with **setting** keys — the
  two methods resolve `port` from different namespaces.

## Variables and settings
- [ ] Variable defaults contain no shell metacharacters (they are interpolated unescaped); user
  overrides with `` `$;&|<>(){}[]!*?~"'\ `` are rejected, so a "description" belongs in a
  setting, not a variable.
- [ ] `user_editable: false` variables ignore overrides entirely.
- [ ] Every setting `default` satisfies its own field: bool is `"true"`/`"false"`, enum default
  is in `options`, numerics inside `min`/`max`. `Validate()` rejects otherwise.
- [ ] Setting keys mirror the game's own config keys when a template just echoes them (Palworld,
  Enshrouded) — operators grep the game's docs by those names.
- [ ] Fields the game auto-generates and rewrites at runtime (Windrose ids/invite code,
  Dragonwilds `ServerGuid`) either stay out of the template or get a "paste it back here" field
  and a description explaining that saves overwrite the file.

## Config files
- [ ] Paths are absolute under the logical `/data` root — `/data/...`, never `C:\data\...`
  (lands at `/data/C:/data/...` on a Linux/wine node).
- [ ] UE: the platform-named folder (`LinuxServer` vs `WindowsServer`) matches the platforms
  declared; both-OS specs declare the file twice.
- [ ] Bool → `True`/`False` conversion for UE ini; raw `true`/`false` for JSON.
- [ ] JSON templates render valid JSON with defaults and with all passwords set (conditional
  arrays need the `$prev` comma trick).
- [ ] The section header / document shape is copied from the game's real default file, not
  reconstructed from memory.

## Backup
- [ ] Globs are data-dir-relative, POSIX separators, no leading `/`, no backslashes (a
  backslash compiles and matches nothing).
- [ ] The include list is *derived* (config path, `-savedir`, rendered `saveDirectory`) and
  the comment above the block names the source.
- [ ] Declaring the block removed the built-in excludes — you re-added `Logs/**`, `Crashes/**`,
  `**/*.log` (or pointed `-logFile`/`logDirectory` outside the save tree).
- [ ] Operator-uploaded trees (`BepInEx/**`, `mods/**`, admin/ban lists) are included.
- [ ] Not sure where saves live → block omitted, with a comment saying so.

## BepInEx
- [ ] Only on Unity games. Mono → BepInEx 5 + Doorstop env launch; IL2CPP → BepInEx 6, and on
  Windows no `bepinex_command` (winhttp.dll auto-injects).
- [ ] Don't reference files the pack does not ship (`run_bepinex.sh` vs
  `start_server_bepinex.sh`); a broken `&&` chain is masked by SteamCMD's earlier `Success!`.
- [ ] `bepinex_script` fetches with tools present in the image (`curl` + `unzip` on Linux,
  PowerShell on Windows).

## Resources
- [ ] `min_memory_mb` = boot floor from the game's docs; `recommended_memory_mb` = the figure
  the game runs at for its default player cap. The scheduler allocates the recommended figure.
- [ ] For per-player scaling, say the formula in a comment (Dragonwilds: 2 GB + 1 GB/player).

## Repo hygiene
- [ ] File name == `slug`; `go test ./internal/panel/catalog/...` green.
- [ ] `PRODUCT.md` count/list updated; SPECS.md only for a new convention.
- [ ] Branch `feat/spec-<slug>`, PR title `feat(specs): add <game>`, no `Co-Authored-By`.
- [ ] PR body states exactly which platforms were live-validated and whether a client joined.
- [ ] Did not touch `specs/` at the repo root, `.claude/launch.json`, or another session's
  in-flight YAML.
