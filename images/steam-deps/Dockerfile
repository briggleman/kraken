# syntax=docker/dockerfile:1
#
# Kraken steam-deps — SteamCMD, downloaded and primed, isolated from the shipped
# game image. Built occasionally (weekly CI); `steam-base` COPYs the primed
# SteamCMD from here, so the shipped image's build does no network download or
# self-update and the download tooling/layers never land in the shipped image.
#
# Build locally (before steam-base):
#   docker build -t kraken-steam-deps:latest images/steam-deps
FROM debian:bookworm-slim

LABEL org.opencontainers.image.title="kraken-steam-deps" \
      org.opencontainers.image.description="SteamCMD download + prime, copied into kraken-steam-base." \
      org.opencontainers.image.source="https://github.com/briggleman/kraken"

# i386 + 32-bit libs are required for SteamCMD to *run* during the prime below
# (the steamcmd binary is 32-bit); ca-certificates/curl/tar fetch it.
RUN dpkg --add-architecture i386 \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl tar lib32gcc-s1 lib32stdc++6 \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* /usr/share/doc/* /usr/share/man/*

# Non-root, matching steam-base (uid 1000) so the copied tree owns correctly.
RUN useradd -m -u 1000 steam
USER steam
WORKDIR /home/steam

# Download SteamCMD, add a `steamcmd` symlink (the tarball ships only
# `steamcmd.sh`, but Game Specs invoke `steamcmd`), and prime it once: the first
# run self-updates and unpacks the Steam client redistributable, baking
# linux32/linux64/steamclient.so in. steam-base copies this whole tree verbatim.
RUN mkdir -p /home/steam/steamcmd \
    && curl -sqL "https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz" \
       | tar zxvf - -C /home/steam/steamcmd \
    && ln -s /home/steam/steamcmd/steamcmd.sh /home/steam/steamcmd/steamcmd \
    && /home/steam/steamcmd/steamcmd.sh +quit \
    # Prune prime cruft not needed to install/run game servers (done in the same
    # layer so it never lands in the tree steam-base COPYs). siteserverui is the
    # SteamCMD site-server web UI; package/ is the self-update download cache (the
    # Agent's install self-updates and repopulates it on first run). The 32-bit
    # bootstrapper (linux32) and the baked client incl. steamclient.so (linux64)
    # are kept — those are required to run steamcmd and for UE/Unity dlopen.
    && rm -rf /home/steam/steamcmd/siteserverui \
              /home/steam/steamcmd/package \
              /home/steam/steamcmd/logs \
    && find /home/steam/steamcmd -name '*.dmp' -delete
