---
title: Start here
description: What Kraken is, the five words the rest of the documentation uses, how the pieces talk to each other, and where to go next.
section: start here
order: 0
---

Kraken is a self-hosted control plane for dedicated game servers. One **Panel**
drives a lightweight **Agent** on every host you own, and each Agent runs your
servers as Docker containers — on Linux, on native Windows, or under Wine — all
from one declarative **Game Spec**.

It is two binaries and Postgres. The Panel embeds the web UI, so there is no
separate static host to run, no message broker, no cache, no Kubernetes. It runs
with zero outbound internet; the Cloudflare and UniFi integrations are optional
and degrade cleanly when unconfigured.

Kraken is free software under GPL-3.0.

## The five words

Every page here uses these five, and means exactly this by them.

| word | what it is |
| --- | --- |
| **Panel** | the control plane. One per fleet. Go, REST + WebSocket, source of truth in Postgres. It embeds the web UI. |
| **Agent** | the per-host daemon. One per machine. It talks to that host's Docker daemon, owns the files on disk, takes the backups, and serves SFTP. |
| **Node** | a host the Panel manages — the machine, plus the Agent on it, plus its port pool. Health is exactly three states: `online`, `partial`, `offline`. |
| **Game Spec** | the declarative game definition (the "egg" equivalent): install script, per-platform image, startup command, ports, settings, config templates. |
| **Server** | one deployed game instance, running on one node from one spec. Every server across every node is the **fleet**. |

`partial` is worth knowing before you meet it: the Agent answers, but its Docker
daemon does not. A partial node is deliberately not schedulable — nothing placed
there could start.

## How it fits together

Four moving parts, one direction of trust. The browser only ever talks to the
Panel; the Panel is the only thing that talks to an Agent.

| hop | how |
| --- | --- |
| browser ⇄ Panel | REST over the published OpenAPI surface, plus WebSocket for console and stats. |
| Panel ⇄ Agent | gRPC over mutual TLS. The Panel dials in, or the node dials out and serves over a reverse tunnel with no inbound port. |
| Panel ⇄ Postgres | all fleet state, sessions and audit history in one durable datastore. |
| browser ⇄ Agent | never. Console and stats are bridged by the Panel, so an Agent needs no browser-facing surface at all. |

**Tunnel mode is the default for new nodes.** A tunnel node keeps one outbound
mTLS connection open to the Panel and is fully manageable with zero inbound
firewall rules — it works behind NAT you do not control. Direct mode, where the
Panel dials the node on port 9090, is still supported and still the faster path
on a LAN you own. Either way, **game traffic never goes through the Panel**:
players always connect straight to the node's ports.

The design note behind tunnel mode is
[docs/design/reverse-connections.md](https://github.com/briggleman/kraken/blob/main/docs/design/reverse-connections.md).

## Placement: one spec, three platforms

A Game Spec carries per-platform overrides — image, install script, startup
command. When a game ships a native Linux dedicated server, that is what gets
scheduled. When it only ships a Windows build, Kraken runs it on a
native-Windows node, or under Wine on a Linux one. The scheduler picks; the
operator does not have to.

Everything downstream stays identical either way: the file browser, the editor,
the backups and the restore path are native Go filesystem work against a host
bind mount, not the Docker archive API.

## How to read the rest of this

- **[Install](/wiki/install/panel/)** is the main line: Panel plus Postgres in
  Docker Compose, Agents on bare metal. Start at the Panel, then add nodes, then
  read the upgrade page *before* your first release lands.
- **[Configure](/wiki/configure/panel/)** is reference: every `KRAKEN_*`
  variable the Panel reads (generated from the source, so it cannot drift), the
  Agent's config file and flags, and the three pages you will actually need —
  reverse proxy, network, limits and logging.

If the Panel will sit behind Nginx Proxy Manager, Caddy or Cloudflare, read
**[behind a reverse proxy](/wiki/configure/reverse-proxy/)** before you expose
it. Getting `KRAKEN_TRUSTED_PROXIES` wrong is not a cosmetic mistake: it decides
what the audit log records and whether the rate limiters protect anybody.

:::note
This wiki is generated from markdown in
[`docs/wiki-src/`](https://github.com/briggleman/kraken/tree/main/docs/wiki-src)
and committed under `docs/wiki/`. Every page carries an edit link at its foot.
:::
