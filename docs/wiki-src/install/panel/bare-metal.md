---
title: Panel on bare metal
description: The alternative install — release binaries, a kraken system user, and the hardened systemd unit, for hosts that would rather run a service than a container.
section: install
order: 11
---

An alternative to [Docker Compose](/wiki/install/panel/), not a replacement for
it. Take this path when you want a service-managed binary, when Docker on the
Panel host is not something you want, or when the machine is already under
systemd and you would like the Panel to look like everything else on it.

Postgres still has to live somewhere. The bundled
[`deploy/docker-compose.yml`](https://github.com/briggleman/kraken/blob/main/deploy/docker-compose.yml)
runs one and nothing else; any Postgres 17 will do.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/briggleman/kraken/main/deploy/install.sh \
  | sudo bash -s -- --role panel
```

`--role panel` installs the Panel and `krakenctl`. `--role agent` installs the
Agent; `--role both` (the default) does the single-host install. Other flags:
`--version vX.Y.Z` pins a release, `--no-systemd` skips the units, `--prefix`
moves the install prefix from `/usr/local`.

What the installer does:

- downloads the release binaries for your OS and arch and **verifies their
  SHA-256 checksums**, treating a mismatch as a hard stop;
- installs `kraken-panel` and `kraken-krakenctl` into `/usr/local/bin`;
- creates the `kraken` system user, `/var/lib/kraken` (state) and
  `/etc/kraken` (config), with the config directory `0750 root:kraken`;
- writes `/etc/kraken/panel.env` **with a freshly generated
  `KRAKEN_SECRETS_KEY`**, but only if that file does not already exist;
- installs the systemd unit.

Re-running it is safe: the binaries are upgraded, `/etc/kraken/*.env` is left
alone.

:::security
The generated `KRAKEN_SECRETS_KEY` in `/etc/kraken/panel.env` seals every
at-rest secret: Steam credentials, the CA private key, SFTP keys, integration
tokens. Back that file up. Losing the key means every stored secret becomes
unrecoverable. Keep it `0640 root:kraken`, which is how the installer leaves it.
:::

## Configure

`/etc/kraken/panel.env` is an ordinary systemd `EnvironmentFile`, so anything in
the [Panel configuration reference](/wiki/configure/panel/) works here. What the
installer writes:

```sh
KRAKEN_HTTP_ADDR=:8080
KRAKEN_DATABASE_URL=postgres://kraken:kraken@127.0.0.1:5432/kraken?sslmode=disable
KRAKEN_SECRETS_KEY=<generated>
KRAKEN_BOOTSTRAP_ADMIN_USER=admin
KRAKEN_BOOTSTRAP_ADMIN_PASSWORD=
KRAKEN_QUICKSTART=true
KRAKEN_LOCAL_AGENT_ADDR=127.0.0.1:9090
```

Set `KRAKEN_QUICKSTART=false` when no Agent will run on this host, or the Panel
registers a `local` node that never comes up. Leave the bootstrap password empty
so the Panel generates one and logs it once.

## Start it

```sh
docker compose -f deploy/docker-compose.yml up -d   # or your own postgres
sudo systemctl enable --now kraken-panel
journalctl -u kraken-panel -f
```

The generated bootstrap password is in that journal, once:

```sh
journalctl -u kraken-panel | grep -i password
```

## The unit

[`deploy/systemd/kraken-panel.service`](https://github.com/briggleman/kraken/blob/main/deploy/systemd/kraken-panel.service)
runs as the `kraken` user and is hardened: `NoNewPrivileges`, `PrivateTmp`,
`ProtectSystem=strict`, `ProtectHome`, `ProtectKernelTunables`,
`ProtectKernelModules`, `ProtectControlGroups`, `RestrictSUIDSGID`,
`LockPersonality`. Writable paths are `/var/lib/kraken` and `/etc/kraken`,
nothing else.

**The Panel does not need the Docker socket.** Handling containers is the
Agent's job, and a Panel holding the socket has more authority than the design
intends. Edit the unit as you like; do not add that.

Point state somewhere else and you have to say so in both places:

```sh
sudo systemctl edit kraken-panel
```

```ini
[Service]
Environment=KRAKEN_STATE_DIR=/srv/kraken
ReadWritePaths=/srv/kraken /etc/kraken
```

`ProtectSystem=strict` makes the rest of the filesystem read-only, so a state
directory that is not in `ReadWritePaths` fails at startup rather than at the
first write.

## Upgrading

Re-run the installer. Nothing is stopped, nothing is clobbered:

```sh
curl -fsSL https://raw.githubusercontent.com/briggleman/kraken/main/deploy/install.sh \
  | sudo bash -s -- --role panel
sudo systemctl restart kraken-panel
```

Read [upgrading](/wiki/install/upgrade/) first. **Agents go before Panels** when
a release changes the Agent↔Panel protocol.
