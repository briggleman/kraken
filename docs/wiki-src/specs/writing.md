---
title: Writing a spec
description: The Game Spec schema field by field, the idempotency rule an install script now has to satisfy, the two-step SteamCMD pattern and why it exists, and what the validator refuses to save.
section: game specs
order: 41
---

A spec is one YAML file. The Panel validates it on save, so most mistakes are
caught before a server ever exists; the ones that are not are the subject of the
second half of this page.

The canonical reference for the conventions the bundled catalog follows is
[`internal/panel/catalog/bundled/SPECS.md`](https://github.com/briggleman/kraken/blob/main/internal/panel/catalog/bundled/SPECS.md).
This page is the orientation.

## The shape

```yaml
name: Valheim
slug: valheim
description: >-
  One or two sentences. Shown on the catalog card.
banner_url: https://…       # library hero art
icon_url: https://…         # community icon
steam_app_ids:
  linux: 896660

platforms:
  - { kind: linux-native, image: ghcr.io/briggleman/kraken-steam-base:latest }

install:
  script: >-
    steamcmd +force_install_dir /data +login anonymous +app_update {{APP_ID}} validate +quit

startup:
  command: >-
    cd /data && ./valheim_server.x86_64 -nographics -batchmode
    -name "{{SERVER_NAME}}" -port {{PORT_GAME}} -savedir /data/save
  stop: { type: signal, value: SIGINT }
  restart: { on_crash: true, max_retries: 3 }

variables:
  - { key: SERVER_NAME, label: Server name, default: "Kraken Valheim", rules: "string", user_editable: true }

ports:
  - { name: game,  protocol: udp, default: 2456, required: true }
  - { name: query, protocol: udp, default: 2457, required: true }

resources:
  min_memory_mb: 2048
  recommended_memory_mb: 4096
```

Two placeholders are injected for you rather than declared. `{{APP_ID}}` is the
Steam appid for the placement's OS family, taken from `steam_app_ids`, and
`{{PORT_<NAME>}}` is the allocated host port for each declared port, with the
name upper-cased: a port called `game` is `{{PORT_GAME}}`. Everything else in
`{{…}}` comes from `variables`.

Required, and refused without: `name`, `slug`, at least one platform (each with
a valid `kind` and an `image`, and no kind twice), `install.script`,
`startup.command`, a `startup.stop` of type `signal` or `command` with a value,
and at least one port. Every port needs a unique name, a `tcp` or `udp`
protocol, and a default inside 1 to 65535.

The optional blocks are `variables`, `settings`, `config_files`, `query` and
`backup`.

### Platforms

`linux-native`, `windows-native` and `linux-wine`. The list order is **scheduler
priority**, so native kinds go before `linux-wine`: Wine is the fallback for a
game with no Linux build, never a preference. Each entry may override
`install_script` and `startup_command`, and may set `skip_update_on_start` for
itself. [Platforms](/wiki/specs/platforms/) has the whole policy.

### Variables and settings

They are different things, and picking the wrong one is the most common
authoring error.

**Variables** are substituted into the install script, the startup command and
the container environment. They are baked in when the container is created, so a
change takes effect on the next start and never sooner. A variable marked
`user_editable` can be overridden per server, and its `rules` are enforced.
Rules are pipe-separated: `string`, `int`, `float`, `bool`, `min:N`, `max:N` and
`in:a,b,c`, so `int|min:0|max:30` is a whole clamp in one field. On top of them
sits a hard rejection of shell metacharacters and control characters, which is
neither optional nor configurable, because the value ends up in a shell command.

**Settings** render into the game's own config files. They are grouped, typed
(`string`, `text`, `int`, `float`, `bool`, `enum`, `password`), and can carry
`min`, `max`, `pattern`, `options` and `help`. A setting never reaches the shell
command; it reaches a file. That is why the bundled specs expose almost
everything as settings and almost nothing as an editable variable.

A setting marked `required: true` is one the game will not start without, such
as Dragonwilds' `OwnerId`. The Panel refuses to start or restart a server while
any required setting is empty, with a `409` that names the fields and points at
the Settings tab, instead of launching it into a crash its own spec predicts. The
deploy form stops offering to start that game as soon as its install lands, and
the Settings tab marks the field. Saving is never blocked, so settings can be
filled in any order. It states an unconditional requirement only: a value needed
just when another setting is on, like Factorio's token for a publicly listed
server, cannot be marked required and belongs in its `help`. A field cannot be
both `required` and `read_only`, because an operator could never satisfy it.

`settings.hot_reload` declares that the game re-reads its config while running.
It changes only what the Panel tells the operator after a save. The files are
written either way.

### Config files

One entry per file, each with a `path` under the logical `/data` root, and a
`format`:

| format | what it writes |
| --- | --- |
| `template` | your `template` string, rendered. Needs the template. |
| `ini` | `[section]` then `key=value` |
| `properties` / `keyvalue` | `key=value` |
| `env` | `KEY=value` |
| `json` | a JSON object |
| `source-cvar` | `key "value"` |

`bindings` map a file key to a setting key, with an optional value `map` for the
cases where the game spells a boolean `1`/`0` or an enum differently from the
dropdown you want to show.

## The rule that changed

:::warning
**The install script runs on every operator-initiated start, so it must be
idempotent.** It runs against a fully installed, fully configured data directory
holding live save games.

`steamcmd … +app_update <id> validate +quit` already satisfies this: a no-op on
a current tree, a repair on a damaged one, and it only touches depot-manifest
files, so uploaded mods and rendered config survive it.

A script that wipes the data directory, re-seeds a config file the operator has
since edited, or unconditionally re-downloads a large unversioned artifact does
not. Rewrite it: guard the seeding step with `[ -f … ] ||`, and probe the
installed version before downloading. The Factorio spec is the worked example,
comparing `factorio --version` against the version in the download redirect.

`install.skip_update_on_start: true` is the opt-out, and it costs that game its
updates. I would treat it as a last resort rather than a convenience.
:::

Two things never re-run on that pass: `bepinex_script`, because those overlays
copy over the tree and pull unpinned builds, and the Agent's crash-restart,
which never involves the Panel at all.

## The two-step SteamCMD pattern

Write `app_update` **twice**.

```sh
steamcmd +force_install_dir /data +login anonymous +app_update {{APP_ID}} validate +quit;
steamcmd +force_install_dir /data +login anonymous +app_update {{APP_ID}} validate +quit
```

A fresh SteamCMD fails the first pass against an empty appinfo cache with
`Missing configuration` and exits non-zero; the second pass succeeds. Every
Kraken install runs in a fresh container, so both passes run every time. The
separator matters: `;` under `/bin/sh`, `&` under `cmd.exe`, because both mean
"run the next one regardless" in their own shell while `&&` does not.

A single manual run that worked is luck rather than proof. Write the two-step.

On a Windows node there is a second problem and **the Agent solves it for you**:
`steamcmd.exe` never updates itself in place, it spawns the new binary and
exits, which a `cmd` chain reads as "that step finished". The Agent rewrites a
Windows install script that mentions `steamcmd` to prime the self-update first
and to wait for the downloader between segments, and logs a line saying it did.
Write the plain two-step script and do not hand-roll the wait.
[Windows containers](/wiki/specs/windows-containers/) has the detail.

## The query block

Optional, and validated. `a2s` needs a `port` naming a **spec port**;
`palworld-rest` needs a `port` and a `password` naming **setting keys**; `log`
needs a join and a leave regex that compile as RE2 and capture at least one of
`(?P<name>…)` or `(?P<id>…)`. An unknown method is refused.

Derive log patterns from a real server log, quote the line you saw in a comment
above the block with the date, and prefer the most specific prefix the game
prints. A regex that also matches a chat line invents players.
[Who is online](/wiki/operate/players/) covers the runtime behaviour.

## The backup block

Data-directory-relative POSIX globs, never `C:\data\…` and never a leading `/`.
`include` selects, `exclude` filters, an exclude wins.

**Omit the block when you are not sure.** A spec with no block gets the Panel's
built-in policy, the whole data directory minus an ephemeral-only exclude list.
An include list that misses the saves produces a green backup with no save in
it, which is the worst outcome in the system; capturing too much is only slow.

Declaring the block **replaces** that policy rather than adding to it, so a save
tree that carries logs needs its own log excludes. Derive the paths from
something the spec already proves: a `config_files` path under the save tree, a
`-savedir` argument, a rendered `saveDirectory`. Leave a comment naming the
source. And include operator-uploaded content a reinstall would not restore, a
mods folder or a BepInEx `plugins/` tree, because that is not install tree.

## Validating

The Panel validates on `POST /specs` and on `PUT /specs/{id}`, and an update
increments the version. Both accept JSON or YAML, and a `400` names the field.

Before you ship one, deploy from it on a real node and watch the install log.
The validator can tell you the spec is well-formed. Only a real install can tell
you the script was idempotent.
