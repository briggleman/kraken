---
title: Ports and firewall
description: Every port Kraken uses, who is allowed to reach it, and the firewall rules for Linux and Windows — including the one that is the most common reason a freshly enrolled node reads offline.
section: configure
order: 23
---

Four listeners and a pool. Game traffic is separate from everything else and
always goes straight to the node.

| port | where | what | who may reach it |
| --- | --- | --- | --- |
| `8080` | Panel | HTTP: web UI, REST + WebSocket API | browsers on your LAN or VPN; Agents during enrollment |
| `9443` | Panel | the mTLS reverse-tunnel listener | tunnel-mode Agents |
| `2222`+ | Panel | SFTP proxy, one port per tunnel node | SFTP clients on your LAN |
| `9090` | Agent | gRPC over mTLS | the Panel only, and only in direct mode |
| `2022` | Agent | per-server SFTP | SFTP clients on your LAN |
| `28000–28999` | Agent host | the game-port pool | players, via your router |

The Panel's HTTP port is `KRAKEN_HTTP_ADDR` / `KRAKEN_HTTP_PORT` and defaults to
`8080`; the reference deployment runs it on **9095**. Pick one and use it
everywhere the Panel URL appears. The port pool is per node and set in the
Panel's node settings; `28000–28999` is the reference range, not a default you
inherit.

Two Agents sharing one port space — the classic case being a WSL distro with
mirrored networking beside its Windows host — cannot both hold `:9090`/`:2022`.
Give the second `:9091`/`:2023` and split the pools into non-overlapping ranges.

## The rules that matter

**Keep the Panel and SFTP off the public internet.** LAN, VPN, or a reverse
proxy with TLS in front. Not a published port on a router.

**`:9443` must stay reachable from your Agents.** It is how every tunnel-mode
node talks to the Panel, and tunnel is the default for new nodes. It carries raw
mTLS, so it cannot be proxied — give it its own address or DNS name. Binding it
to loopback while putting the HTTP port behind a proxy takes the whole fleet
offline at once.

**Agent gRPC must not be reachable off-host without mTLS.** The default deploy
configs bind it to `127.0.0.1:9090` so only the co-located Panel can reach it.
The Agent refuses to serve plaintext gRPC on a non-loopback address unless
either `KRAKEN_TLS_CERT`/`KRAKEN_TLS_KEY`/`KRAKEN_TLS_CA` are configured — which
is what enrollment does — or `KRAKEN_ALLOW_INSECURE_GRPC=1` is set as an
explicit opt-in. Agent gRPC has no application-level auth; mTLS is the whole
trust boundary, and it fronts the Docker socket.

**Game ports are published 1:1.** The port the Panel assigns is the port on the
host and the port a player types. Forward them from your router to the node —
tunnel mode changes nothing here, because game traffic never touches the Panel.

## Direct-mode nodes need an inbound rule

:::warning
**The Panel dials *in* to a direct-mode Agent, so enrollment succeeding proves
nothing about reachability.** Enrollment is an outbound HTTP call; the gRPC
channel is inbound. A blocked inbound port is the most common reason a freshly
enrolled node sits **offline**, usually surfacing as a connection refused in the
Panel's logs when NAT is in the path.

Use a **port-based** rule, not a program-based one. Program rules silently stop
matching when the Agent binary is renamed or replaced — which a self-update does
by design.
:::

```powershell
# windows, elevated
New-NetFirewallRule -DisplayName "kraken-agent ports (TCP 9090 + 2022)" `
  -Direction Inbound -Action Allow -Protocol TCP -LocalPort 9090,2022
```

```sh
# linux — ufw
sudo ufw allow 9090/tcp && sudo ufw allow 2022/tcp
# linux — firewalld
sudo firewall-cmd --permanent --add-port={9090,2022}/tcp && sudo firewall-cmd --reload
```

Scope those to your LAN or VPN subnet where you can. They still must never be
internet-exposed: gRPC is mTLS-only, but SFTP is password and key auth.

## Or skip inbound ports entirely

A node enrolled with `--tunnel` — **Add node → the node dials the Panel** —
keeps an outbound mTLS connection open to `KRAKEN_TUNNEL_ADDR` and is fully
manageable with **zero inbound firewall rules**. It works behind NAT you do not
control, which is the case bare rules cannot solve at all.

The trade-off is per-server SFTP. The Panel fronts each tunnel node's SFTP on a
per-node port of its own, allocated upward from `KRAKEN_SFTP_PROXY_BASE_PORT`
(default 2222) and persisted on the node record, forwarding the raw SSH byte
stream to the Agent — so the Panel never terminates SSH, and credentials and
host keys stay Agent-side. Raw SSH carries no routing header a pass-through
proxy could read, which is why each node needs its own port rather than one
shared endpoint. The in-browser file manager works unchanged either way.

Design note:
[docs/design/reverse-connections.md](https://github.com/briggleman/kraken/blob/main/docs/design/reverse-connections.md).

## Game ports, end to end

1. Deploy a server. The scheduler allocates its ports from the node's pool.
2. Forward those ports — the protocol matters, and most games want UDP — from
   your router to the **node's** address, 1:1.
3. Players connect to your public address on that port.

The optional **UniFi** integration can publish the forward from the same screen
that assigned the port, and the **Cloudflare** integration can publish the DNS
record. Both are optional and degrade cleanly when unconfigured; neither is
required for a game to be joinable.

A node whose pool is empty is **online and unschedulable** — every deploy is
refused with "no node can host this spec" while the fleet looks healthy. Set the
range when you register the node.

:::shot
a node's settings showing its game-port pool range, with an assigned server port beneath it
:::
