---
title: Windows containers
description: What is genuinely different about running a game server in a Windows container — Hyper-V isolation and why, the bind-mount form that works, missing-DLL crashes and where to fix them, and the SteamCMD self-update trap the Agent handles for you.
section: game specs
order: 43
---

A Windows node runs Windows containers, which are a different animal from Linux
ones in four specific ways. Everything else about a `windows-native` placement
is the same: the same lifecycle, the same bind-mounted data directory, the same
file and backup paths.

The data root is mounted at `C:\data` rather than `/data`. Specs still write the
logical `/data` in `config_files`, and the Agent maps it.

## Hyper-V isolation, by default

`KRAKEN_WINDOWS_ISOLATION` defaults to `hyperv`.

Process isolation requires the host's build and the container image's build to
match. Running an `ltsc2022` image on a Windows 11 host, which is the common
homelab case, fails `CreateComputeSystem` with `The request is not supported`.
Hyper-V isolation works across that mismatch, which is why it is the default
rather than the faster option.

On a Windows Server host whose build matches the image, set
`KRAKEN_WINDOWS_ISOLATION=process` for a lighter container, or `default` to
defer to the daemon. On anything else, leave it alone.

## Bind mounts use the `-v` form

The Agent mounts a server's data directory with Docker's `Binds` field, the
`source:target` form, rather than the structured `Mounts` API. Under Hyper-V
isolation the structured form fails `CreateComputeSystem` with the same *request
is not supported*. This is internal, and it is here because it is the kind of
thing somebody reads the code and tries to modernise.

## Missing DLLs

The Server Core base image is not desktop Windows. A game that links against
something the image lacks does not start, and it does not explain itself: the
container exits with `0xC0000135`, `STATUS_DLL_NOT_FOUND`, and nothing in the
log.

Kraken translates the code for you in the drill-in, so a crashed server reads
`exit 3221225781 / 0xC0000135` with the explanation beside it. Its sibling
`0xC0000139` means a DLL is present but the wrong build, with an export missing,
and `0xC000007B` is a 32/64-bit mismatch.

**Fix it in the image, not next to the exe.** The path is to scan the binary's
import table inside the container, find what is unresolved, and add it to
[`images/steam-win/Dockerfile`](https://github.com/briggleman/kraken/blob/main/images/steam-win/Dockerfile).
Copying a DLL from a desktop Windows machine next to the executable does not
work reliably; that copy has its own dependencies the image also lacks, and you
end up chasing the chain one file at a time.

## The SteamCMD self-update trap

This one is worth understanding even though the Agent handles it, because the
failure it produces looks like success.

`steamcmd.exe` never updates itself in place. When Valve ships a newer client,
the running bootstrapper **spawns the new binary and exits**. A `windows-native`
install script runs as `cmd /S /C "<script>"`, so `cmd` is PID 1: it sees that
first `steamcmd.exe` return, walks the rest of the `&` chain, reaches the end,
and the container dies while the relaunched downloader is still writing
`steamapps/downloading`. Exit code 0, no game files, no error line, and a Panel
showing an offline server over an empty data directory.

**The Agent guards this.** On a Windows node, and only when the install script
mentions `steamcmd`, it rewrites the script to:

- prepend a bare `steamcmd.exe +quit` prime, which triggers the self-update
  before any real `app_update` pass can be cut short by it;
- follow the prime, each `steamcmd` segment of the `&` chain, and the end of the
  chain with a wait that blocks until no `steamcmd` process remains, so the
  relaunched child's `Success! App … fully installed` line still reaches the log
  stream and the existing success and failure patterns still decide the outcome.

An install-log line records that it fired:
`[kraken] windows SteamCMD guard applied: …`. Per-segment insertion is skipped,
with the prime and a trailing wait still applied, when the chain contains `"`,
`^`, `(`, `)`, `<`, `>`, `|` or `&&`, any of which makes splitting on `&` unsafe.

So **write the plain two-step script** and do not hand-roll the wait. Linux
scripts and non-Steam scripts are never touched.

:::warning
If you ever do write a wait by hand, it cannot be `powershell -Command "…"`.
Docker's Windows argument escaping rewrites inner double quotes as `\"`, so
PowerShell receives the loop as a quoted string literal, echoes it, and exits 0,
having done nothing at all and said nothing about it. It has to go through
`powershell -NoProfile -NonInteractive -EncodedCommand <UTF-16LE base64>`, which
carries no quotes. Put the decoded command in a comment beside it.
:::

## Writing a Windows startup command

- **One line, `cmd` syntax.** `&` to chain. Do not rely on `&&` across newlines.
- **Launch the shipping binary directly.** For an Unreal game that is
  `<Project>\Binaries\Win64\<Project>Server-Win64-Shipping.exe`. The root
  wrapper is UE's bootstrapper and hangs on an invisible prerequisite dialog
  under Server Core.
- **Pin the user directory.** `-UserDir=C:\data\<Project>` keeps UE's `Saved/`
  inside the bind mount, which is what makes backups and the file browser see
  it.
- **Declare the Linux stop signal anyway.** A Windows daemon ignores custom stop
  signals and sends its own shutdown event, waits, then kills. A Windows-only
  signal name is dead weight on a Linux Agent.

One log line that is not a failure: headless V Rising prints `EOS session
creation failed! NotFound`. That is the server-browser listing, not the server.
