---
title: Platforms
description: The three placement kinds — Linux native, Windows native and Linux under Wine — the policy that decides which a spec declares, how per-platform overrides work, and the config-path rules that make one spec run on both operating systems.
section: game specs
order: 42
---

A spec declares one or more `platforms`, and each is a **kind** plus an
**image**, optionally with its own install script and startup command.

| kind | where it runs | image |
| --- | --- | --- |
| `linux-native` | a Linux node, the game's native Linux server | `kraken-steam-base` |
| `windows-native` | a Windows node, Windows containers | `kraken-steam-win` |
| `linux-wine` | a Linux node, the game's Windows build under Wine | `kraken-steam-wine` |

The list order is **scheduler priority**, not a statement about which operating
system we prefer.

## The policy

The goal is that every game is deployable on either kind of node, and that a
native build always wins.

1. **Bias toward the native build for the node's OS.** A Windows node runs the
   native Windows build; a Linux node runs the native Linux build. The spec does
   not push a server onto one OS. The node it lands on picks.
2. **Most games ship a native Windows server. When a native Linux server also
   exists, declare both.** Then the game loads on either Agent.
3. **When no Linux build exists, add a `linux-wine` entry**, so Linux nodes can
   still host it. Wine is the fallback, never the preference, so put the native
   kinds first in the list.

Dragonwilds is the case where one appid ships both depots: the same id sits
under `linux` and `windows` in `steam_app_ids`, SteamCMD picks the depot for the
platform it runs on, and the Panel injects `{{APP_ID}}` from the entry matching
the placement's OS family.

## Per-platform overrides

A `platforms[]` entry may override `install_script`, `startup_command` and
`skip_update_on_start`. The spec-level `install.script` and `startup.command`
are the fallback for every kind that does not override them.

`config_files`, `stop`, `ports` and `backup` have **no** per-platform variant.
That is a deliberate constraint rather than an omission, and it produces three
rules worth knowing before you write a dual-platform spec:

**Config paths always use the logical `/data` root.** The Agent maps it to
`C:\data` on a Windows node and leaves it alone on Linux. A literal `C:\data\…`
in a spec breaks on a Linux or Wine node, where it resolves to a `C:`
subdirectory.

**Platform-named config folders get one entry each.** Unreal reads
`Saved/Config/LinuxServer/` on the Linux build and `Saved/Config/WindowsServer/`
on the Windows build. Declare both `config_files` entries with the same
template. Every save writes both; each build reads its own and ignores the
other.

**Declare the Linux stop signal.** `stop: { type: signal, value: SIGINT }`. A
Windows daemon ignores custom stop signals and sends its own shutdown event,
and a Linux Agent ignores a Windows-only name such as `CTRL_SHUTDOWN_EVENT` and
falls back to `SIGTERM`. Either name is safe on both, but the Linux signal is
the only one either Agent can actually honour, which makes it the convention.

**Backup globs are data-directory-relative and OS-neutral**, so check that the
Windows build actually writes its saves under the data directory. An Unreal
build marked "installed" defaults to `%LOCALAPPDATA%\<Project>`; pin it with
`-UserDir=C:\data\<Project>` in the Windows startup command.

## Wine, specifically

The `kraken-steam-wine` image is a Linux image with WineHQ and a headless X
server. A `linux-wine` install pulls the **Windows** depot with the Linux
SteamCMD by forcing the platform type:

```sh
steamcmd +@sSteamCmdForcePlatformType windows +force_install_dir /data \
  +login anonymous +app_update {{APP_ID}} validate +quit
```

Two things about launching under it, both of which the image encodes so specs do
not have to rediscover them:

**Use `wine64`, not `wine`.** They are not interchangeable in this image.

**Use `wine-headless`, not `xvfb-run`.** `xvfb-run` waits for Xvfb's `SIGUSR1`
readiness signal, and when a spec's startup command execs it as container PID 1
that signal is ignored, because PID 1 default-ignores signals with no handler.
The container then sits there having started nothing. `wine-headless` starts
Xvfb and polls for its socket instead, and it is on `PATH` in the image:

```sh
wine-headless /data/<Game>/Binaries/Win64/<Server>-Win64-Shipping.exe -log
```

For a UE5 server whose launcher respawns itself in ways Wine dislikes, launch
the `Win64-Shipping` binary directly rather than the wrapper.

Wine placements are validated per game, not in general. Enshrouded's
`linux-wine` entry is live-validated to its ready line under Wine 10; Abiotic
Factor's carries the direct-binary note above. If you add a `linux-wine` entry,
boot it once and watch the console before you call it supported.

:::note
BepInEx under Wine is not validated. The bundled BepInEx-capable specs target a
Linux-native node and a Windows-native node respectively.
:::
