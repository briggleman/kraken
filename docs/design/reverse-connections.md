# Reverse connections: eliminating the inbound-agent-port requirement

**Status:** accepted 2026-08-19 — phase 1 implemented (Option A, per-node certs
included). **Tunnel is the default for new nodes since 2026-08-20**, after the
live Behemoth drill (issue #89) proved the full surface — enrollment, deploys,
console/stats, backups, self-update, and the failure drills — over a tunnel
with zero inbound firewall rules, across the 0.22.x release cycle. **Phase 2
(panel-side SFTP proxy) implemented 2026-08-20** (issue #90): the Panel fronts
each tunnel node's SFTP on a per-node port (base `KRAKEN_SFTP_PROXY_BASE_PORT`,
default 2222), forwarding the raw SSH byte stream over a discriminated yamux
stream to the agent's local SFTP server — the Panel never terminates SSH, so
credentials and host keys stay agent-side. Direct nodes keep their own
endpoint.
**Scope:** Panel ⇄ Agent transport direction. Game traffic is explicitly out of
scope — players always connect directly to node ports, tunnel or not.

## Problem

Today the Panel dials into every Agent. Each node must therefore accept two
inbound TCP connections:

| Port | What | Who connects |
| --- | --- | --- |
| 9090 | gRPC/mTLS (`NodeService`) | the Panel only |
| 2022 | SFTP (chrooted per-server file access) | any SFTP client on the LAN |

This is the single largest source of real-world setup failure we have observed:
the mTLS saga on Behemoth was ultimately Windows Firewall program-rules not
matching a renamed binary, and the "node registered but offline" diagnosis path
exists almost entirely because of inbound reachability. It also makes two whole
classes of node impossible:

- a node behind NAT the operator does not control (a friend's house, a rented
  box, CGNAT);
- a node on a network where opening inbound ports is not allowed.

The question this doc answers: **should the Agent dial out to the Panel
instead, and what does that drag along?**

## Current topology (verified against code, 2026-08-19)

- **Panel → Agent gRPC.** `internal/panel/nodeclient/nodeclient.go` keeps a
  lazy pool of outbound mTLS connections keyed by agent address. Every feature
  — power, install, console, stats, backups, file ops, self-update — is an RPC
  on this one channel (`proto/kraken/agent/v1/agent.proto`).
- **Browser console/stats already flow through the Panel.** The browser's
  WebSocket terminates at the Panel (`internal/panel/api/handlers_stream.go`),
  which bridges to the Agent's `StreamConsole`/`StreamStats` gRPC streams.
  There is **no** direct browser→agent network path. (CLAUDE.md's architecture
  sketch still claims one; it is stale and should be corrected regardless of
  this decision.)
- **SFTP is the only non-Panel inbound consumer.** Login username **is the
  server id** (`internal/agent/sftpserver.go`), so every SFTP session is
  already self-identifying — a fact the proxy option below exploits.
- **Agents already dial the Panel once**: enrollment
  (`internal/agent/enroll/enroll.go`) POSTs to the Panel's HTTP API to obtain
  the mTLS bundle. Outbound agent→panel connectivity is an existing
  assumption, not a new one.
- **Identity gap worth knowing about:** all agent certs share one CN
  (`internal/shared/mtls/enroll.go`); the cert proves "is an enrolled agent,"
  not "is node X." Today that's fine because the *Panel* chooses which address
  to dial. Any reverse design must add the missing binding (see Security).

The good news: because console/stats were already re-homed through the Panel,
the hardest thing scope 5 was expected to drag along **no longer exists**. The
blast radius is the gRPC channel itself plus SFTP.

## Goals / non-goals

**Goals**

1. A node can be fully managed with **zero inbound ports** on the agent host.
2. Direct mode keeps working unchanged; both modes coexist in one fleet.
3. No changes to the `NodeService` proto or to any feature built on it.
4. Setup gets simpler, not just different (no new certs, no new config
   surface beyond "panel URL," which enrollment already requires).

**Non-goals**

- Game traffic. Port forwarding for players is inherent to hosting.
- Panel-behind-NAT. The Panel is the rendezvous point; it must be reachable
  by agents (it already must be, for enrollment) and by browsers.
- A general-purpose VPN. Operators who want that have Tailscale/WireGuard
  today, which already works with Kraken unmodified (documented workaround).

## Options

### Option A — reverse tunnel carrying the existing gRPC channel (recommended)

The Agent dials the Panel over TLS and keeps a multiplexed session open
(hashicorp/yamux or equivalent). The Agent runs its **unchanged** gRPC server
on the tunnel's virtual listener; the Panel's connection pool gains a custom
dialer (`grpc.WithContextDialer`) that, for tunnel-mode nodes, opens a yamux
stream instead of TCP. Everything above the dialer — every RPC, every stream,
the self-update push — is untouched.

```
direct:   Panel ──TCP──▶ agent:9090 (gRPC server)
tunnel:   Agent ──TCP──▶ panel:9443 ──┐ (one mTLS conn, yamux session)
          Panel ──yamux stream──▶ Agent's gRPC server (same server, virtual listener)
```

- **Panel side:** one new mTLS-only listener (e.g. `:9443`, configurable;
  never multiplexed with the web port) + a registry `node_id → live session`.
  `nodeclient.Pool` consults the registry before falling back to TCP.
- **Agent side:** a `KRAKEN_CONNECTION=tunnel` mode (or `--tunnel`) that dials
  `KRAKEN_PANEL_URL`-derived host:9443 with the existing cert bundle,
  reconnecting with jittered backoff. The gRPC server code does not change.
- **Health:** tunnel presence is itself a liveness signal. The reconciler's
  ~4s polls ride the tunnel; a dropped tunnel reads as offline *faster* than a
  dial timeout does today.
- **Proto impact:** none.

### Option B — invert the RPC direction (rejected)

Make the Agent the gRPC client and redesign `NodeService` as a bidirectional
command/response stream the Panel pushes into. This is the "proper" gRPC-native
shape, but it rewrites every RPC call site, re-implements request/response
correlation and per-call deadlines by hand, and turns four working streaming
RPCs into hand-rolled sub-protocols. All cost, no capability Option A lacks.

### Option C — require a mesh VPN (rejected as product, kept as docs)

Tailscale/WireGuard between panel and nodes makes "inbound 9090" a non-issue
without any Kraken changes, and it works today. But it imports a third-party
dependency into the product's happy path, conflicts with LAN-first/no-cloud
(Tailscale's control plane), and outsources exactly the part operators find
hard. Keep it as a documented workaround for the impatient; do not make it the
answer.

## SFTP under tunnel mode

Phased, because the tunnel is useful without it:

- **Phase 1 (ship with the tunnel):** tunnel-mode nodes simply have no SFTP
  unless the operator opens 2022 themselves. The in-browser file manager —
  which covers the majority of file tasks and rides the gRPC channel — works
  fully. The Files tab hides the SFTP card for tunnel nodes (honest state,
  not a broken advertisement).
- **Phase 2 (optional follow-up):** the Panel exposes a single SFTP endpoint
  (`panel-host:2022`) and proxies sessions over the tunnel. Username = server
  id already tells the proxy which node to route to; the Panel forwards the
  raw SSH byte stream over one yamux stream and the Agent's existing SFTP
  server authenticates exactly as today (the Panel never terminates SSH, so
  credentials and host keys stay agent-side end to end). One address for the
  whole fleet is arguably *better* UX than per-node addresses.

## Security analysis

- **Exposure inversion.** Today: N agents each expose one mTLS port to the
  LAN. After: the Panel exposes one additional mTLS-only listener; agents
  expose nothing. Net: strictly fewer listening sockets, concentrated on the
  host that already runs the web UI and is presumably the best-tended.
- **The tunnel listener must demand a client cert from our CA** (same
  `tls.Config` the Panel already builds for outbound) and speak nothing else —
  no HTTP, no fallback. A scanner sees a TLS socket that rejects every
  handshake without a CA-signed cert.
- **Node-identity binding (required work).** Because agent certs share a CN,
  a tunnel handshake must not let any enrolled agent claim any node id. Fix:
  after TLS, the first yamux stream carries a hello (`node_id`), and the Panel
  verifies it against the store — accepting the claim only if the node record
  exists and either has no live tunnel or matches the same cert fingerprint
  seen before (mirrors the watchdog re-adoption rules). Longer term, issue
  per-node certs (node id in a SAN) at enroll time; new enrollments get the
  binding for free while old bundles keep working until rotated.
- **Audit:** tunnel connect/disconnect/rejection are auditable events with
  source IP and fingerprint, same as enrollment.

## Operator-facing changes

- **Enrollment:** unchanged token flow; the Add Node dialog gains a
  "connection" choice — *node dials the Panel* (default since 2026-08-20,
  needs nothing inbound) vs *Panel dials the node* (needs the firewall
  rule). Tunnel-mode install drops the firewall instructions entirely.
- **Node records:** `address` becomes optional for tunnel nodes (the
  IP/DNS-only address rule keeps applying whenever an address *is* present).
- **Nodes page:** the node card shows link direction (⇄ direct / ⇉ tunnel)
  with the same tri-state health. No new states: a tunnel node without a live
  session is simply *offline*.
- **Docs:** README port-matrix and install scripts gain the mode fork;
  CLAUDE.md's stale "browser ⇄ agent WebSocket" line gets corrected.

## Failure modes

| Failure | Behavior |
| --- | --- |
| Tunnel drops (agent crash, network) | Panel registry evicts on close; node reads offline within seconds; agent reconnects with backoff; watchdog re-adoption unchanged |
| Panel restarts | All tunnels drop; agents reconnect on backoff (same as agents surviving panel restarts today); no state lost — sessions are connection-scoped |
| Two agents claim one node id | Second claim rejected unless fingerprint matches (re-adoption rule); audited |
| Operator flips a node direct → tunnel | Node record keeps address; pool prefers live tunnel, falls back to dialing — flip is non-destructive both ways |

## Effort estimate

| Piece | Size |
| --- | --- |
| Panel tunnel listener + session registry + hello/binding | ~2–3 days |
| `nodeclient` dialer selection + fallback | ~1 day |
| Agent tunnel client (dial, backoff, virtual listener) | ~1–2 days |
| Enroll/Add-Node UI + node card direction, docs | ~1–2 days |
| Tests (registry, binding rejections, reconnect, pool fallback, race) | ~2 days |
| **Phase 1 total** | **~7–10 days** |
| Phase 2 SFTP proxy | ~3–4 days, independently shippable |

Dependencies: `hashicorp/yamux` (MPL-2.0, zero transitive deps) or
`golang.org/x/net` http2-based equivalent. No proto changes, no migration —
one nullable column-equivalent (`connection_mode`) on nodes.

## Recommendation

**Go — Option A, phase 1, after the current release settles.** The two facts
that made this scary are gone: console/stats already route through the Panel,
and self-update proved the Panel⇄Agent channel can carry heavy payloads. The
remaining work is genuinely additive (a second way to *establish* the same
channel), dual-mode by construction, and it deletes the project's worst
first-run failure mode. Phase 2 (SFTP proxy) should wait for evidence that
tunnel-mode operators actually miss SFTP.

**Decisions (recorded):**

1. Go/no-go on phase 1 as scoped above. — **Go**; shipped 2026-08-19 (PR #85).
2. Default for *new* nodes: keep direct as default, or make tunnel the
   default? — **Tunnel-as-default**, decided 2026-08-20 after the tunnel
   survived the live Behemoth drill and the 0.22.x release cycle (issue #89).
   Direct stays a first-class choice in the Add Node dialog, just no longer
   the preselected one.
3. Whether per-node certificates ride along in phase 1 or land separately. —
   **Rode along**: per-node identity (`kraken://agent/<uuid>` URI SAN) is
   minted at every enrollment since #85; the yamux hello was never needed.
