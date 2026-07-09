# syntax=docker/dockerfile:1
#
# Kraken steam-wine — Linux container that runs WINDOWS-only dedicated game
# servers via Wine. Published to GHCR as ghcr.io/briggleman/kraken-steam-wine
# (see .github/workflows/steam-images.yml) and used by Game Specs' `linux-wine`
# platform entries.
#
# Layout mirrors steam-base: SteamCMD (primed, incl. steamclient.so) is COPYed
# from `kraken-steam-deps` so this build never downloads or self-updates Steam
# tooling. Installs use the LINUX SteamCMD with `+@sSteamCmdForcePlatformType
# windows` to pull the Windows depot; startup runs the .exe through Wine under
# a headless X server via the baked-in wrapper — e.g.:
#   wine-headless /data/<Game>/Binaries/Win64/<Server>-Win64-Shipping.exe …
#
# Wine comes from the WineHQ apt repo, version-pinned via WINE_BRANCH /
# WINE_VERSION so a weekly CI rebuild can't silently jump a Wine major and
# break game compatibility. The 64-bit prefix is pre-initialized at build time
# so first server boot skips the ~30s wineboot & the "wine: created prefix"
# noise; each container's copy-on-write layer gives every server its own
# writable prefix copy.
#
# Build locally (deps first, same as steam-base):
#   docker build -t kraken-steam-deps:latest images/steam-deps
#   docker build -t ghcr.io/briggleman/kraken-steam-wine:latest images/steam-wine
ARG STEAM_DEPS=kraken-steam-deps:latest
FROM ${STEAM_DEPS} AS steamdeps

FROM debian:bookworm-slim

ARG VERSION=dev
# WineHQ branch + exact package version to pin. Discover available pins with:
#   apt-cache madison winehq-stable   (inside a bookworm container with the repo)
ARG WINE_BRANCH=stable
ARG WINE_VERSION=10.0.0.0~bookworm-1

LABEL org.opencontainers.image.title="kraken-steam-wine" \
      org.opencontainers.image.description="Debian + Wine + SteamCMD image for Windows-only dedicated game servers on Linux nodes (Kraken)." \
      org.opencontainers.image.source="https://github.com/briggleman/kraken" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

# Root layer 1: the same minimal runtime set as steam-base (SteamCMD is 32-bit;
# curl/tar/unzip for non-Steam fetches and mod packs; xdg-user-dirs silences
# SteamCMD's xdg-user-dir lookup).
RUN dpkg --add-architecture i386 \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        gnupg \
        tar \
        unzip \
        xdg-user-dirs \
        lib32gcc-s1 \
        lib32stdc++6 \
        libstdc++6 \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* /usr/share/doc/* /usr/share/man/*

# Root layer 2: WineHQ (pinned) + xvfb for headless. --install-recommends is
# deliberate for the winehq package only: Wine's recommends are the font/GL/
# audio stubs many game servers dlopen at boot; without them UE/Unity servers
# die on missing DLL translations.
RUN curl -fsSL https://dl.winehq.org/wine-builds/winehq.key \
        | gpg --dearmor -o /usr/share/keyrings/winehq-archive.key \
    && echo "deb [signed-by=/usr/share/keyrings/winehq-archive.key] https://dl.winehq.org/wine-builds/debian/ bookworm main" \
        > /etc/apt/sources.list.d/winehq.list \
    && apt-get update \
    && apt-get install -y --install-recommends \
        "winehq-${WINE_BRANCH}=${WINE_VERSION}" \
        "wine-${WINE_BRANCH}=${WINE_VERSION}" \
        "wine-${WINE_BRANCH}-amd64=${WINE_VERSION}" \
        "wine-${WINE_BRANCH}-i386=${WINE_VERSION}" \
    && apt-get install -y --no-install-recommends xvfb xauth \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* /usr/share/doc/* /usr/share/man/*

ENV LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

# Non-root: SteamCMD refuses root, game servers shouldn't have it either.
RUN useradd -m -u 1000 steam

# wine-headless — run a Windows binary under Wine with a headless X server.
# This replaces xvfb-run, which waits for Xvfb's SIGUSR1 readiness signal:
# when a spec's startup command execs xvfb-run as container PID 1, that signal
# is ignored (PID 1 default-ignores signals without handlers) and xvfb-run
# waits forever without ever launching the game. We start Xvfb ourselves and
# poll for its socket instead. Specs use: wine-headless /data/<Server>.exe …
RUN printf '%s\n' \
      '#!/bin/sh' \
      'set -e' \
      ': "${DISPLAY:=:99}"' \
      'Xvfb "$DISPLAY" -screen 0 1024x768x24 -nolisten tcp >/dev/null 2>&1 &' \
      'i=0' \
      'while [ ! -S "/tmp/.X11-unix/X${DISPLAY#:}" ] && [ "$i" -lt 100 ]; do i=$((i+1)); sleep 0.1; done' \
      'export DISPLAY' \
      'exec wine64 "$@"' \
      > /usr/local/bin/wine-headless \
    && chmod 0755 /usr/local/bin/wine-headless

# Primed SteamCMD from the deps image (no network fetch in this build).
COPY --from=steamdeps --chown=steam:steam /home/steam/steamcmd /home/steam/steamcmd
ENV PATH="/home/steam/steamcmd:${PATH}"

USER steam

# Pre-initialize the 64-bit Wine prefix at build time. WINEDEBUG=-all keeps
# game-server console output readable (Wine fixmes would otherwise swamp the
# ready_regex scanner and the UI console). NOTE: use `wine64`, not `wine` —
# WineHQ's Debian packaging ships the 32-bit loader as `wine`, and it fails to
# bootstrap a 64-bit prefix with `could not load kernel32.dll, status c0000135`.
# Specs must launch their .exe via `wine64` for the same reason.
ENV WINEPREFIX=/home/steam/.wine \
    WINEARCH=win64 \
    WINEDEBUG=-all
RUN xvfb-run -a wine64 wineboot --init \
    && wineserver --wait

# Server data lives on a mounted volume here.
VOLUME ["/data"]
WORKDIR /data

# Default entrypoint is a shell; the Agent overrides Entrypoint/Cmd per server
# with the spec's install script or startup command.
ENTRYPOINT ["/bin/bash", "-c"]
CMD ["wine64 --version && steamcmd +quit"]
