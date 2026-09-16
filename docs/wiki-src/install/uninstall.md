---
title: Uninstalling
description: Take a node out of the fleet, or remove Kraken entirely — in the order that leaves nothing orphaned, with the two things worth keeping called out before you delete them.
section: install
order: 15
---

Nothing here is reversible, so read the order before running any of it.

:::security
Two things are worth keeping even if you are sure: **`KRAKEN_SECRETS_KEY`** and
the **Postgres data**. Without the key, a database restored later is a database
of secrets nobody can open. Without the database, the key opens nothing. Take
both, or accept that this install is gone.

Game-server data is a third: `KRAKEN_DATA_DIR` on each node holds the worlds.
Back up what you want before removing a node — the install tree comes back with
a reinstall; the save does not.
:::

## Remove one node

Delete the server or move it elsewhere first, then delete the node in **Settings
→ Nodes**. Deleting the node record does not stop the Agent or free the disk —
do that on the host.

**Linux:**

```sh
sudo systemctl disable --now kraken-agent
sudo rm -f /etc/systemd/system/kraken-agent.service
sudo systemctl daemon-reload
sudo rm -rf /opt/kraken/bin /usr/local/bin/kraken-agent /etc/kraken/agent.env
# state, server data and local backups — check before you run this
sudo rm -rf /var/lib/kraken
```

**Windows:**

```powershell
C:\kraken\bin\kraken-agent.exe --service stop
C:\kraken\bin\kraken-agent.exe --service uninstall
Remove-NetFirewallRule -DisplayName "Kraken Agent (TCP 9090 gRPC + 2022 SFTP)"
# binaries, state, server data and backups — check before you run this
Remove-Item -Recurse -Force C:\kraken
```

Game containers the Agent created stay on the host's Docker daemon. Remove them
yourself if you want the space back:

```sh
docker ps -a --filter "name=kraken-" 
```

## Remove the Panel: compose

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.full.yml down
```

That keeps the volumes. To delete the fleet with them:

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.full.yml down -v
```

`-v` destroys `pgdata` (every server, node, user and audit row) and
`panel-state` (the config file, the generated CA, the secrets key if it was
generated rather than supplied). There is no undo and no prompt.

## Remove the Panel: bare metal

```sh
sudo systemctl disable --now kraken-panel
sudo rm -f /etc/systemd/system/kraken-panel.service
sudo systemctl daemon-reload
sudo rm -f /usr/local/bin/kraken-panel /usr/local/bin/kraken-krakenctl
sudo rm -rf /etc/kraken /var/lib/kraken
sudo userdel kraken
```

Postgres is separate — it was either a container you brought up from
`deploy/docker-compose.yml` or a database of your own. Drop it deliberately.

## What is left behind

- **Cloudflare DNS records and UniFi port forwards** the integrations created
  are not removed by uninstalling. They live in those systems; delete them
  there.
- **Router port forwards** you made by hand for game ports.
- **Off-node backups** on a NAS share or SFTP remote. Kraken never reaches back
  to clean them up, which is the behaviour you want right up until you forget
  they are there.
