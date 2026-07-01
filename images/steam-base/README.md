# kraken-steam-base

Hardened Debian + SteamCMD base image for **Linux-native** dedicated game servers, used by Kraken
Game Specs whose platform is `linux-native`. Published to GHCR as
**`ghcr.io/briggleman/kraken-steam-base`** so any node can pull it.

## What's inside / why

This image is the distillation of everything we learned getting real Steam servers (Palworld, V
Rising on the Windows side, etc.) running reliably:

- **SteamCMD, copied from a separate deps image.** SteamCMD (with the `steamcmd` symlink and the
  primed Steam client redistributable — incl. **`linux32/linux64/steamclient.so`**) is downloaded and
  primed in [`images/steam-deps`](../steam-deps) and `COPY --from`'d into this image. So **this build
  does no network download or self-update** — it's fast, reproducible, and the download tooling never
  lands in the shipped layers. UE/Unity servers `dlopen` `steamclient.so` at startup (Palworld looks
  in `$HOME/.steam/sdk64`), and the runtime container is a *fresh* copy of this image, so the lib must
  already be present — not just in the separate one-shot install container. SteamCMD self-updates when
  the Agent runs the install phase, and the **weekly CI rebuild** (below) keeps the base current.
- **Runtime libraries**: 64-bit `libstdc++6` (modern UE/Unity servers) + 32-bit `lib32gcc-s1`
  /`lib32stdc++6` (SteamCMD itself is 32-bit), plus `ca-certificates`, `curl`, `tar` (non-Steam
  installs, e.g. Factorio over HTTP) and `xdg-user-dirs` (SteamCMD shells out to `xdg-user-dir` at
  startup; without it Steam logs a harmless but noisy "not found"). Game-specific libs (e.g. SDL2 for
  Source-engine servers) are **not** in the base — add them in a game-specific layer when a spec needs them.
- **Built-in `C.UTF-8` locale** (`LANG`/`LC_ALL`) — UTF-8 text handling without the `locales` package.
- **Non-root** (`steam`, uid 1000) — SteamCMD refuses to run as root and servers shouldn't either.
- **Lean & labelled (~328 MB)** — `--no-install-recommends`, cleaned apt lists, stripped docs/man, and
  the primed SteamCMD tree is pruned of cruft not needed to install/run servers (`siteserverui`, the
  `package/` self-update cache, logs, crash dumps — SteamCMD re-fetches what it needs on first install).
  The hard floor is the Debian base + glibc + the dual-arch SteamCMD client; **distroless/Alpine are not
  viable** (game servers need a shell, glibc, and 32-bit libs that musl/distroless break).

## Usage in a Game Spec

```yaml
platforms:
  - { kind: linux-native, image: ghcr.io/briggleman/kraken-steam-base:latest }
install:
  # SteamCMD's first app_update on a fresh client can fail with "Missing
  # configuration" and exit nonzero — run it twice (the second pass succeeds).
  script: >-
    steamcmd +force_install_dir /data +login anonymous +app_update {{APP_ID}} validate +quit;
    steamcmd +force_install_dir /data +login anonymous +app_update {{APP_ID}} validate +quit
startup:
  command: cd /data && ./YourServer ...   # bind the allocated port: -port={{PORT_GAME}}
```

The Agent overrides the image's Entrypoint/Cmd per server with the rendered install/startup script
(run via `/bin/sh -c`), bind-mounts the server's data dir at `/data`, and publishes the allocated
port **1:1** (container port = host port = the port the server binds).

## Build & publish

Two images, built by [`.github/workflows/steam-images.yml`](../../.github/workflows/steam-images.yml):

1. **`kraken-steam-deps`** ([`images/steam-deps`](../steam-deps)) — downloads + primes SteamCMD.
2. **`kraken-steam-base`** (this image) — `COPY --from` the deps image, adds runtime libs.

Triggers: changes under `images/steam-{deps,base}/`, `steam-base-v*` tags, manual dispatch, **and a
weekly cron** (Mondays) so nodes never run a super-stale SteamCMD. Tags: `latest` (default branch),
`sha-<commit>`, `steam-base-v<semver>`. The `base` job passes `--build-arg STEAM_DEPS` pointing at the
freshly-pushed deps image.

Local build (deps first):

```sh
docker build -t kraken-steam-deps:latest images/steam-deps
docker build -t ghcr.io/briggleman/kraken-steam-base:latest images/steam-base
```

## Related

- Windows-native equivalent: [`images/steam-win`](../steam-win) (Server Core + SteamCMD).
- BepInEx (modded) variant: planned (queued next) — see [BACKLOG.md](../../BACKLOG.md). **No new
  image**: BepInEx installs into `/data` during the install phase (it's Unity-version/backend-specific)
  and the server boots through the Doorstop loader, so modded and vanilla share this same base.
