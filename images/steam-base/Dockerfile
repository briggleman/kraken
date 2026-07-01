# syntax=docker/dockerfile:1
#
# Kraken steam-base — hardened Linux-native game-server base image.
# Published to GHCR as ghcr.io/briggleman/kraken-steam-base (see
# .github/workflows/steam-images.yml) and reusable by any node.
#
# SteamCMD (primed, incl. steamclient.so) is COPYed from the separate
# `kraken-steam-deps` image rather than downloaded here, so this build does no
# network fetch or self-update — it's fast, reproducible, and the download
# tooling never lands in the shipped layers. SteamCMD still self-updates when the
# Agent runs the install phase, and the weekly CI rebuild keeps the base current.
#
# Build locally:
#   docker build -t kraken-steam-deps:latest images/steam-deps
#   docker build -t ghcr.io/briggleman/kraken-steam-base:latest images/steam-base
ARG STEAM_DEPS=kraken-steam-deps:latest
FROM ${STEAM_DEPS} AS steamdeps

FROM debian:bookworm-slim

ARG VERSION=dev
LABEL org.opencontainers.image.title="kraken-steam-base" \
      org.opencontainers.image.description="Hardened Debian + SteamCMD base image for Linux dedicated game servers (Kraken)." \
      org.opencontainers.image.source="https://github.com/briggleman/kraken" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

# Root layer: minimal runtime libraries, then drop privileges. SteamCMD is 32-bit
# and many servers ship 32-bit binaries, so enable the i386 multiarch and install
# lib32gcc-s1/lib32stdc++6 (SteamCMD itself needs these). 64-bit libstdc++ covers
# modern UE/Unity servers; curl/tar stay for non-Steam installs (e.g. Factorio
# fetches its build over HTTP); unzip unpacks mod packs (BepInEx ships ZIPs). We
# use the glibc built-in C.UTF-8 locale instead
# of generating en_US.UTF-8 — no `locales` package, smaller image, still UTF-8.
# xdg-user-dirs provides `xdg-user-dir`, which SteamCMD shells out to at startup
# to locate user folders; without it Steam logs a harmless but noisy
# `sh: xdg-user-dir: not found`. Game-specific libs (e.g. SDL2 for Source-engine
# servers) are intentionally NOT in the base; add them in a game-specific layer
# when a spec needs them.
RUN dpkg --add-architecture i386 \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        tar \
        unzip \
        xdg-user-dirs \
        lib32gcc-s1 \
        lib32stdc++6 \
        libstdc++6 \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* /usr/share/doc/* /usr/share/man/*

ENV LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

# Non-root: SteamCMD refuses to run as root, and game servers shouldn't either.
RUN useradd -m -u 1000 steam

# Bring in the primed SteamCMD (with steamclient.so) from the deps image — no
# download happens in this build. UE/Unity servers dlopen steamclient.so at
# startup (e.g. Palworld -> $HOME/.steam/sdk64), and the runtime container is a
# fresh copy of this image, so it must already be present.
COPY --from=steamdeps --chown=steam:steam /home/steam/steamcmd /home/steam/steamcmd
ENV PATH="/home/steam/steamcmd:${PATH}"

USER steam

# Server data lives on a mounted volume here.
VOLUME ["/data"]
WORKDIR /data

# Default entrypoint is a shell; the Agent overrides Entrypoint/Cmd per server
# with the spec's install script or startup command.
ENTRYPOINT ["/bin/bash", "-c"]
CMD ["steamcmd +quit"]
