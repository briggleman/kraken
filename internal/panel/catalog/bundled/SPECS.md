# Kraken Game Spec authoring conventions

This directory is the bundled catalog — YAML specs `go:embed`ded into the
Panel binary. Each `*.yaml` describes one game the operator can import in
`/catalog`.

## Image assets — pick the right URL

Consistent Catalog / Specs cards mean **every spec uses the same two
derivatives**, always sourced from the **game** appid (not the dedicated-
server appid, which usually has no CDN assets).

### `banner_url` → the **library hero** (`library_hero_2x.jpg`)
Ultra-wide (~3840×1240) key art with no title text baked in — the right shape
for the full-bleed rows on `/specs`, which crop hard horizontally. Prefer
`library_hero_2x`; fall back to `library_hero` when a game has no 2x asset
(Factorio). Pattern (the hash directory differs per asset, so it can't be
derived from the capsule URL):
```
https://shared.fastly.steamstatic.com/store_item_assets/steam/apps/<game_appid>/[<hash>/]library_hero_2x.jpg?t=<ts>
```

### `icon_url` → the **community icon**
Small square icon used in the header + list rows. Hashed path — you can't
derive it from the appid alone.
```
https://shared.fastly.steamstatic.com/community_assets/images/apps/<game_appid>/<hash>.jpg
```

## How to find the URLs

Ask the store API for the asset manifest — no scraping, no age gate. It returns
every derivative plus the `asset_url_format` prefix they hang off:

```bash
appid=427520
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

Pin the exact URLs you get (including the `?t=<timestamp>` cache-buster) into
`banner_url` / `icon_url` verbatim — the timestamp keeps the CDN version
stable across builds. Confirm each one returns `200` (`curl -I`) before
committing.

A game with no public store entry has no `assets` block at all (Jagex-published
titles have done this) — leave `banner_url` / `icon_url` unset and the row falls
back to the no-image hatch.

## Appid selection

Games that ship a **separate dedicated-server appid** (Enshrouded, V Rising,
Abiotic Factor, Windrose, Dragonwilds) have TWO Steam appids:
- **Game appid** → `banner_url` / `icon_url` (assets)
- **Dedicated-server appid** → `steam_app_ids.linux` / `.windows` (SteamCMD)

Never point the image URLs at the server appid — it has no store CDN entry.

A game whose single server appid ships **both** OS depots (Dragonwilds) lists
the same id under `linux` and `windows` — SteamCMD picks the depot for the
platform it runs on, and the Panel injects `{{APP_ID}}` from the entry that
matches the placement's OS family (`buildVars` in
[`internal/panel/api/handlers_server.go`](../../api/handlers_server.go)).

## Dual-platform specs — per-platform overrides and config paths

### Platform policy

The goal is that **every game is deployable on either agent**, and that native
builds win over any OS preference of ours:

1. **Bias toward the NATIVE build for the node's OS.** A Windows node runs the
   game's native Windows build; a Linux node runs the native Linux build. The
   spec does not push a server onto one OS — the node it lands on picks.
2. **Most games ship a native Windows server. When a native Linux server ALSO
   exists, declare both** `windows-native` **and** `linux-native` **platform
   entries** so the game loads on either agent.
3. **When no Linux build exists, add a `linux-wine` entry** so Linux nodes can
   still host it. Wine is the fallback — never preferred over a native build.

The `platforms` list order is scheduler priority, not an OS preference: put the
native kinds before `linux-wine` so a native build is always chosen when the
node can run one.

### Per-platform overrides

`platforms[]` entries take optional `install_script` / `startup_command`
overrides (see `abiotic-factor.yaml`, `dragonwilds.yaml`); the spec-level
`install.script` / `startup.command` are the fallback. `config_files`, `stop`,
`ports` and `backup` have **no** per-platform variant, so:

- **Config paths always use the logical `/data/…` root** — the agent maps it to
  `C:\data` on a Windows node and leaves it alone on Linux. A literal
  `C:\data\…` breaks on a Linux/wine node (it resolves to a `C:` subdirectory).
- **Platform-named config folders** (Unreal reads `Saved/Config/LinuxServer/`
  on the Linux build and `Saved/Config/WindowsServer/` on the Windows build):
  declare one `config_files` entry per folder with the same template. Every save
  writes both; each build reads its own and ignores the other.
- **`stop`** — declare the Linux signal (`SIGINT`); a Windows daemon ignores
  custom stop signals and sends its own shutdown event, while a Linux agent
  ignores Windows-only names such as `CTRL_SHUTDOWN_EVENT` (falls back to
  SIGTERM). Either name is safe on both — declaring the Linux signal is the
  convention because it is the only one either agent can actually honour.
- **`backup` globs** are data-dir-relative and OS-neutral; make sure the
  Windows build actually writes its saves under the data dir (Unreal builds
  marked "installed" default to `%LOCALAPPDATA%\<Project>` — pin them with
  `-UserDir=C:\data\<Project>` in the Windows startup command).

### The Windows SteamCMD guard — the agent handles it, don't copy it into specs

`steamcmd.exe` never updates itself in place: when Valve ships a newer client
the running bootstrapper **spawns the new binary and exits**. Because a
`windows-native` install script runs as `cmd /S /C "<script>"` — cmd is PID 1 —
cmd sees that first `steamcmd.exe` return, walks the rest of the `&` chain,
reaches the end, and the container dies while the relaunched downloader is still
writing `steamapps/downloading`. Exit code 0, no game files, no error line, and
the Panel shows an offline server over an empty data dir.

**The agent guards this for you.** On a Windows node only, and only when the
install script mentions `steamcmd`, `internal/agent/docker.go`'s `Install()`
rewrites the script (see `internal/agent/steamguard.go`):

- prepends a bare `steamcmd.exe +quit` prime, which triggers the self-update
  before any real `app_update` pass can be cut short by it;
- follows the prime, every `steamcmd` segment of the `&` chain, and the end of
  the chain with a PowerShell loop that blocks until no `steamcmd` process
  remains — so the relaunched child's `Success! App … fully installed` line
  still reaches the log stream and the existing success/failure regexes still
  decide the outcome.

An install-log line (`[kraken] windows SteamCMD guard applied: …`) records that
it fired. Per-segment insertion is skipped — prime plus a trailing wait are
still applied — when the chain contains `"`, `^`, `(`, `)`, `<`, `>`, `|` or
`&&`, any of which makes splitting on `&` unsafe.

So **write the plain two-pass script**; do not hand-roll the wait. Linux
scripts and non-Steam scripts are never touched, and a script that already
carries the guard (`dragonwilds.yaml` predates it) is left alone.

**The `\"` gotcha, if you ever do need to write it by hand:** the wait cannot be
`powershell -Command "…"`. Docker's Windows argument escaping rewrites inner
double quotes as `\"`, so PowerShell receives the loop as a quoted string
literal, echoes it, and exits 0 — doing nothing, silently. It has to go through
`powershell -NoProfile -NonInteractive -EncodedCommand <UTF-16LE base64>`,
which carries no quotes at all.

## The `backup:` block — where the saves live

**A backup is the game's SAVE DATA, not the reinstallable install tree.** The
10–30 GB SteamCMD tree comes back with a reinstall; the world does not. Tarring
the whole data dir is what drove the multi-hour backups, the mirror bandwidth,
and the `archive/tar: write too long` failures (live logs inside the install tree
grow mid-capture).

An optional `backup:` block tells the platform what to capture. Patterns are
[doublestar](https://github.com/bmatcuk/doublestar) globs (`**` spans any number
of path segments) matched against **data-dir-relative POSIX paths** —
`savegame/world.db`, `Pal/Saved/SaveGames/0/Level.sav` — never `C:\data\…` and
never a leading `/`. `include` selects (omit it and everything is selected);
`exclude` then filters, so an exclude wins on a conflict.

```yaml
backup:
  include:
    - Pal/Saved/**
  exclude:
    - Pal/Saved/Logs/**
```

- **Omit the block when you are not sure.** A spec with no block gets the
  Panel's built-in policy: the whole data dir minus a conservative
  ephemeral-only exclude list (SteamCMD staging dirs, logs, crash dumps) —
  see `builtinBackupExcludes` in
  [`internal/panel/api/backupglobs.go`](../../api/backupglobs.go). An include
  list that misses the saves produces a **green backup with no save in it**,
  which is the worst outcome this system has; capturing too much is only slow.
- **Declaring the block replaces that policy wholesale** — the built-in excludes
  are NOT merged underneath it. Add your own `**/*.log` / `Logs/**` excludes for
  a save tree that carries logs (every Unreal game does).
- **Derive the paths, don't guess them.** The spec usually proves them itself: a
  `config_files` path under the save tree, a `-savedir` / `-persistentDataPath`
  startup argument, a rendered `saveDirectory` in the game's own config. Leave a
  one-line comment above the block naming that source.
- Include operator-uploaded content that a reinstall would *not* restore (a
  BepInEx `plugins/` tree, a Factorio `mods/` folder) — mods are not install tree.
- The globs are spec-level only (no per-server override) and are resolved by the
  Panel on every backup, manual or scheduled. Restore is unaffected: an archive
  only ever contains what was included.

## The `query:` block — who is online

Optional. Without it the PLAYERS readout and the fleet's "n online" stay blank;
with it the agent reads the count (and, for one method, the names) while the
server runs. Three methods, in order of preference:

| method | when | declares | yields |
| --- | --- | --- | --- |
| `a2s` | the server answers Steam's A2S_INFO UDP query (most Source/Steam servers, Valheim) | `port:` — the spec **port name** to query (Valheim: its `query` port) | count + cap |
| `palworld-rest` | Palworld's admin REST API | `port:` / `password:` — the **setting keys** holding the REST port and admin password | count + cap |
| `log` | the game answers nothing but prints a line when a player arrives and another when one leaves (UE5 early-access servers, typically) | `join_regex:` / `leave_regex:` / `max_players:` | count + **names** |

```yaml
query:
  method: log
  join_regex: 'LogDominionPlayerController: RequestGameExit : .* for Account\[(?P<id>[^\]]+)\] Character Name\[(?P<name>[^\]]*)\]'
  leave_regex: 'LogDominionPlayerController: ClientRequestDisconnect : .*Account\[(?P<id>[^\]]+)\] Character Name\[(?P<name>[^\]]*)\]'
  max_players: 6
```

- The regexes are [RE2](https://github.com/google/re2/wiki/Syntax) (Go's
  `regexp`). Each must capture the player: `(?P<name>…)` is the display name,
  `(?P<id>…)` an optional stable account id the roster keys on — with an id, a
  renamed character is one player, not two; without one the name is the key.
  Quote them with **single quotes** in YAML so `\[` survives.
- The agent follows the container's console for the life of each run and
  replays the run's log from its start after an agent restart, so who is
  aboard survives the agent going down. Nobody is aboard a server that is not
  running; the count reads **unknown** while no follower is attached.
- The leave regex is tried first on every line, so a join pattern loose enough
  to match a departure line does not resurrect a player who just left.
- A log never states the cap. `max_players:` is the game's own constant
  (Dragonwilds: 6); where the operator picks it, `max_players_setting:` names
  the setting key holding it (Enshrouded: `slotCount`) and the Panel resolves
  the server's value, falling back to `max_players:` when it is blank. Omit
  both and the readout shows the count with no denominator.
- A line that carries single quotes (`Player 'name' logged in`) needs
  double-quoted YAML instead, with every `\` doubled: `"\\[server\\] Player
  '(?P<name>[^']+)'"`.
- Derive the lines from a real server log, quote them in a comment above the
  block with the date you saw them, and prefer the most specific prefix the
  game prints (`LogDominionPlayerController: ClientRequestDisconnect`, not
  `Account\[`) — a regex that also matches a chat line will invent players.
- `Validate` rejects an unknown method, a missing port/password for the query
  methods, and a `log` regex that does not compile or captures neither group,
  because every one of those fails silently at runtime as "players unknown".

## Related

- Spec schema: [`internal/shared/spec/spec.go`](../../../shared/spec/spec.go)
- Settings field types + config-file formats: [`internal/shared/spec/settings.go`](../../../shared/spec/settings.go)
- Catalog import loader: [`internal/panel/catalog/catalog.go`](../catalog.go)
