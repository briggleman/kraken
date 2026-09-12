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

## Related

- Spec schema: [`internal/shared/spec/spec.go`](../../../shared/spec/spec.go)
- Settings field types + config-file formats: [`internal/shared/spec/settings.go`](../../../shared/spec/settings.go)
- Catalog import loader: [`internal/panel/catalog/catalog.go`](../catalog.go)
