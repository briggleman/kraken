---
title: Behind a reverse proxy
description: Nginx Proxy Manager, Caddy or Cloudflare in front of the Panel — what KRAKEN_TRUSTED_PROXIES actually does, why Docker Desktop erases every client address, and the exact Authenticated Origin Pulls configuration that works.
section: configure
order: 22
---

Putting the Panel behind a proxy is the normal thing to do, and it changes one
fact the Panel relies on: **who the caller is**. Every request now arrives from
the proxy. Get this page right before you expose anything.

Three things are decided here, in order: which proxy the Panel believes, how the
proxy reaches the Panel, and whether anything other than your proxy can reach
the Panel at all.

## What `clientIP` does

`clientIP` is one function used by the audit log, the `/setup/*`
internal-network gate and both rate limiters, so they cannot disagree about who
called. The semantics, exactly:

- **Empty `KRAKEN_TRUSTED_PROXIES` — the default — is the real TCP peer and
  nothing else.** `X-Forwarded-For` is a request header like any other, and
  believing it without a trusted set lets anyone claim any address.
- **When the peer is inside a trusted CIDR, the Panel takes the *rightmost*
  `X-Forwarded-For` entry that is not itself trusted.** Rightmost, because the
  list is appended to hop by hop: everything to the left of the last trusted hop
  is whatever the client chose to send, and only what a trusted proxy appended
  can be believed.
- **A malformed hop ends anything believable to its left** — the walk stops
  there and falls back.
- **`X-Forwarded-For` is the only header consulted, and deliberately so.**
  `CF-Connecting-IP` looks more direct, but only Cloudflare ever sets it: Caddy,
  nginx and Traefik pass a client-supplied one through untouched, and nothing in
  the request says which of them is in front. Believing it from any trusted
  proxy would let an internet client name its own address to `requireInternal`
  (which would put `/setup/*` and the unauthenticated local enrollment behind a
  header the caller writes), to both rate limiters, and to every audit row.
  Cloudflare appends to `X-Forwarded-For` as well, so the rightmost-untrusted
  rule serves the reference deployment without trusting anything a proxy did not
  append.
- **An unparseable entry is a startup error, not a warning.** A typo that
  silently emptied the list would leave the Panel running with `/setup/*` seeing
  the tunnel's loopback for the entire internet and both limiters back in one
  shared bucket — a misconfiguration that looks like a working Panel.
  Hostnames are refused too: a name is not source-verifiable.

The default is wrong for a proxied deployment, and not in the direction you
might assume. Behind a reverse proxy or a Cloudflare Tunnel the peer is the
*proxy* on every request, so **every audit row records the proxy and both
limiters collapse into a single shared bucket that one stranger can exhaust for
everybody**.

```sh
# a cloudflared sidecar, or any proxy on this host
KRAKEN_TRUSTED_PROXIES=127.0.0.0/8,::1/128
# a proxy elsewhere on the LAN — name its address
KRAKEN_TRUSTED_PROXIES=10.0.0.5/32
```

Name the proxy, not the network it sits in. Every address in that list can name
any client address it likes.

## Docker Desktop erases the client address

This is the failure that wastes an afternoon.

**Docker Desktop rewrites the source address of every published-port connection
to its gateway, `192.168.65.1`.** A proxy container and a Panel container on the
same machine, talking through published ports, means the Panel sees
`192.168.65.1` for the proxy — and for everything else that reaches a published
port, including any LAN client.

So:

:::warning
Trusting `192.168.65.1` to fix your audit log hands header-forging to anyone on
your LAN. Any client that can reach the published port arrives from the same
gateway address, is therefore "a trusted proxy", and its own `X-Forwarded-For`
is believed.
:::

The fix is not a wider trust list, it is a shorter path:

1. Put the proxy and the Panel on a **shared Docker network**.
2. Proxy to the **container name and internal port** —
   `http://kraken-panel:8080` — rather than to `host.docker.internal` or a
   published port.
3. Trust the **Docker network's subnet**, which only containers on it can
   originate from: `KRAKEN_TRUSTED_PROXIES=172.18.0.0/16` (read your own with
   `docker network inspect <name>`).

On plain Docker on Linux — which is what the reference compose stack runs — this
problem does not exist: the Panel takes `network_mode: host` and sees real
source addresses already. Then a proxy on the same host is
`KRAKEN_TRUSTED_PROXIES=127.0.0.0/8,::1/128` and you are done.

## Nginx Proxy Manager

Create a Proxy Host for the Panel's hostname, forwarding to the Panel's
container name and port, and:

- **Websockets Support: on.** The console and live stats are WebSockets that
  terminate at the Panel. Without this the UI signs in and then sits there with
  a dead console and no stats, which looks like a broken Panel rather than a
  proxy setting.
- **Block Common Exploits** is fine. Caching is not: leave it off.

Then, on the Panel:

```sh
KRAKEN_ALLOWED_ORIGINS=https://kraken.example.com
KRAKEN_TRUSTED_PROXIES=172.18.0.0/16
```

`KRAKEN_ALLOWED_ORIGINS` is the WebSocket origin allowlist. Same-origin is
always permitted, so this only matters once the browser's origin is not the
Panel's own address — which is exactly what a proxy makes true.

## Caddy

```text
kraken.example.com {
	reverse_proxy kraken-panel:8080
}
```

Caddy passes `X-Forwarded-For` and upgrades WebSockets without being asked.
Trust its address and set the origin as above.

## Cloudflare in front

With an orange-clouded record, Cloudflare terminates TLS and your proxy is the
origin. Two extra moves:

1. **Trust the gateway as well**, because the tunnel or the Cloudflare-facing
   proxy is what now reaches the Panel. Cloudflare appends the real client to
   `X-Forwarded-For`, and the rightmost-untrusted rule picks it out.
2. **Bind the HTTP port to `127.0.0.1`** so the only path in is through the
   proxy. An origin that is also directly reachable is an origin whose
   protection is optional.

:::warning
Bind the **HTTP** port to loopback. Not `:9443`.

`KRAKEN_TUNNEL_ADDR` is the mTLS listener every tunnel-mode Agent dials, it
carries raw mTLS that no proxy can pass through, and binding it to loopback
takes every tunnel node in the fleet offline at once. Give it its own DNS name
or address and point Agents at it with `--tunnel-addr`.
:::

### Authenticated Origin Pulls

This is what makes "only Cloudflare may reach my origin" true rather than
hopeful. The origin refuses any TLS connection that does not present a client
certificate signed by Cloudflare's Origin Pull CA.

1. Turn on the zone-wide toggle: **SSL/TLS → Origin Server → Authenticated
   Origin Pulls**, the *Global* switch.
2. Install Cloudflare's **public Origin Pull CA** on the proxy —
   `https://developers.cloudflare.com/ssl/static/authenticated_origin_pull_ca.pem`.
3. In NPM, on the Proxy Host, **Advanced → Custom Nginx Configuration**:

```text
ssl_client_certificate /data/cloudflare-origin-pull-ca.pem;
ssl_verify_client on;
ssl_verify_depth 2;
```

:::warning
**This is the public Origin Pull CA, not a dashboard-generated Origin
Certificate.** They are different objects that both arrive as a `.pem` from
Cloudflare's dashboard, and using the wrong one fails in a way that names
neither: requests come back as **400 "The SSL certificate error"**.

If you see that 400, you installed an Origin Certificate where the Origin Pull
CA belongs. Replace the file, reload the proxy, and it clears.
:::

`ssl_verify_depth 2` is required because Cloudflare's client certificate chains
through an intermediate; depth 1 rejects it.

## Checking your work

- **Audit rows** should show real client addresses, not one repeated proxy
  address. That is the whole point of the setting, and it is the only check that
  actually proves it.
- **Console and stats** should stream. If they do not, it is WebSockets on the
  proxy or `KRAKEN_ALLOWED_ORIGINS`, in that order.
- **`/setup/*`** should be unreachable from outside your network.
  `KRAKEN_SETUP_ALLOWED_CIDRS` gates it on the resolved client address, so
  trusted proxies configured wrong is exactly what would open it. A Panel behind
  a co-located tunnel otherwise sees `127.0.0.1` for the entire public internet.
- **Rate limits** should refuse a burst of bad logins from one address without
  refusing everybody: [limits and
  logging](/wiki/configure/limits-and-logging/).

:::shot
the audit log after a proxy is configured, showing distinct client addresses per row rather than one repeated proxy address
:::

## While you are here

A Panel that is internet-reachable should also have:

- **`KRAKEN_CSP`** left at `enforce`. Run `report-only` for a day after putting
  a new proxy or CDN in front, check the browser console for violations, then
  switch back. `off` is for the case where a proxy already sets its own policy —
  two CSP headers intersect, which is usually stricter than either author
  intended.
- **`KRAKEN_CSP_SCRIPT_SRC`** set only if your CDN injects a script. The shipped
  policy is same-origin only; Cloudflare Web Analytics is the common exception
  and needs `https://static.cloudflareinsights.com` named here rather than in
  the default everyone else inherits.
- The **game ports left alone**. They are not proxied, never were, and players
  connect straight to the node: [ports and firewall](/wiki/configure/network/).
