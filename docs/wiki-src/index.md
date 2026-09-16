---
title: Start here
description: What Kraken is, the five words the rest of the documentation uses, how the pieces talk to each other, and where to go next.
section: start here
order: 0
---

Kraken is a self-hosted control plane for dedicated game servers. One **Panel**
drives a lightweight **Agent** on each host you own, and every Agent runs your
servers as Docker containers: on Linux, on native Windows, or under Wine, all
from one declarative **Game Spec**.

Two binaries and Postgres. The Panel embeds the web UI, so you are not also
running a static host, a message broker, a cache, or Kubernetes. It works with
no outbound internet at all; the Cloudflare and UniFi integrations are optional
and degrade cleanly when unconfigured.

Kraken is free software under GPL-3.0.

## The five words

The rest of the wiki leans on these five, and means this by them.

| word | what it is |
| --- | --- |
| **Panel** | the control plane. One per fleet. Go, REST + WebSocket, source of truth in Postgres. It embeds the web UI. |
| **Agent** | the per-host daemon. One per machine. It talks to that host's Docker daemon, owns the files on disk, takes the backups, and serves SFTP. |
| **Node** | a host the Panel manages: the machine, plus the Agent on it, plus its port pool. Health is three states — `online`, `partial`, `offline`. |
| **Game Spec** | the declarative game definition (the "egg" equivalent): install script, per-platform image, startup command, ports, settings, config templates. |
| **Server** | one deployed game instance, running on one node from one spec. All the servers across all the nodes are the **fleet**. |

Learn `partial` before you meet it. The Agent answers, its Docker daemon does
not, and the node is deliberately not schedulable, because nothing placed there
could start.

## How it fits together

Four moving parts, one direction of trust. The browser talks to the Panel; the
Panel is the only thing that talks to an Agent.

| hop | how |
| --- | --- |
| browser ⇄ Panel | REST over the published OpenAPI surface, plus WebSocket for console and stats. |
| Panel ⇄ Agent | gRPC over mutual TLS. The Panel dials in, or the node dials out and serves over a reverse tunnel with no inbound port. |
| Panel ⇄ Postgres | all fleet state, sessions and audit history in one durable datastore. |
| browser ⇄ Agent | never. Console and stats are bridged by the Panel, so an Agent needs no browser-facing surface at all. |

**Tunnel mode is the default for new nodes.** A tunnel node holds one outbound
mTLS connection to the Panel and is fully manageable with zero inbound firewall
rules, which is what makes a machine behind somebody else's NAT a node at all.
That is why it is the default: the inbound rule is where most setups fail, and
removing it removes the failure. On a LAN you control, direct mode — the Panel
dialling the node on port 9090 — is one less moving piece, and I would still
reach for it there. Game traffic is unaffected
either way. Players connect straight to the node's ports; nothing about a game
session goes through the Panel.

The design note behind tunnel mode is
[docs/design/reverse-connections.md](https://github.com/briggleman/kraken/blob/main/docs/design/reverse-connections.md).

## Placement: one spec, three platforms

A Game Spec carries per-platform overrides for the image, the install script and
the startup command. When a game ships a native Linux dedicated server, that is
what gets scheduled. When it only ships a Windows build, Kraken runs it on a
native-Windows node, or under Wine on a Linux one. The scheduler picks; you do
not have to.

Downstream, the platform stops mattering. The file browser, the editor, the
backups and the restore path are native Go filesystem work against a host bind
mount rather than the Docker archive API, so they behave the same on both
operating systems.

## How to read the rest of this

- **[Install](/wiki/install/panel/)** is the main line: Panel plus Postgres in
  Docker Compose, Agents on bare metal. Start at the Panel, add nodes, then read
  the upgrade page *before* your first release lands rather than during it.
- **[Configure](/wiki/configure/panel/)** is reference: the `KRAKEN_*` variables
  the Panel reads (generated from the source, so it cannot drift), the Agent's
  config file and flags, and the three pages you will reach for in anger —
  reverse proxy, network, limits and logging.

Planning to put Nginx Proxy Manager, Caddy or Cloudflare in front? Read
**[behind a reverse proxy](/wiki/configure/reverse-proxy/)** before you expose
anything. `KRAKEN_TRUSTED_PROXIES` decides what the audit log records and
whether the rate limiters protect anybody, and the wrong value still looks like
a working Panel.

:::note
This wiki is generated from markdown in
[`docs/wiki-src/`](https://github.com/briggleman/kraken/tree/main/docs/wiki-src)
and committed under `docs/wiki/`. There is an edit link at the foot of each
page.
:::
