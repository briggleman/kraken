# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

**Primary: technical homelab self-hosters** who run dedicated game servers on
hardware they own. Competent with Docker, a terminal, and their own router —
but not paid operators, and not looking for a career-grade orchestration
platform. Design for a competent stranger, not only for the author.

Two confirmed usage scenes, both first-class:

1. **Desk, second monitor.** The Panel is parked on a spare screen while the
   operator games or works. Glanceability and density matter more than
   step-by-step guidance — fleet state should read in one look.
2. **Phone, mid-session.** Something broke while playing. Power actions,
   status, and the live console must actually work on a phone on the same LAN.
   This is not a courtesy breakpoint; it is a real scene where the operator is
   under time pressure and one hand short.

Long focused **setup sessions** at a desk (enrolling a node, authoring a Game
Spec, deploying a new game) are the third context; wizards and editors serve it
but do not define the product's center.

Multi-user is supported (four built-in roles: **Owner**, **Admin**, **Operator**,
**Read-only** — `internal/panel/rbac/rbac.go`, with per-server object-level
ownership), but the assumed default install is one operator managing their own
fleet. Running servers *for* a wider community is a supported case, not the
design target.

## Product Purpose

Deploy and manage dedicated game servers across many self-owned hosts from one
control plane. One **Panel** (Go, source of truth in Postgres) drives a
lightweight **Agent** on each host, which runs each game server as a Docker
container.

Success is an operator who can go from a fresh install to a joinable game
server without reading a manual, then leave it alone — and, when something
breaks at 11pm, diagnose and fix it from whatever screen is nearest.

## Positioning

**Pelican/Pterodactyl-class capability without the operational complexity**, and
genuinely cross-OS.

The mechanism a neighboring panel cannot truthfully copy without building it:
Kraken schedules a game onto **Linux nodes, native-Windows nodes (Windows
containers), or Linux-under-Wine**, from one declarative Game Spec with
per-platform overrides — preferring a native Linux dedicated server when one
exists and falling back to Windows. Everything that touches server data (file
browser, editor, backups, restore) is **native Go filesystem work against a host
bind mount**, not the Docker archive API, so it behaves identically on both
operating systems.

Deployment is deliberately small: two binaries plus Postgres. The Panel embeds
the web UI, so there is no separate static host.

## Operating Context

- **A home LAN**, one to a few hosts. The Panel dials *into* each Agent over
  gRPC/mTLS, so inbound host firewall rules on the Agent side are part of normal
  operation — and the most common reason a freshly enrolled node reads offline.
- **Docker per host.** Panel and Agent run as host processes (they need the
  Docker socket); only Postgres and the game servers are containers. A host whose
  Docker daemon is down makes its node **partial** — reachable but unschedulable.
- **Game ports are the operator's router problem.** Per-server TCP/UDP ports get
  forwarded from a consumer router; optional **UniFi** port-forward and
  **Cloudflare** DNS integrations automate that when present.
- **Backups land somewhere the operator already owns** — a local dir, an
  SMB/NFS-mounted NAS share, or an SFTP remote — with optional replication.
- **Server data is reachable outside the UI** over per-server SFTP, chrooted to
  that server's data dir.
- **The Panel is a service, not a session.** systemd on Linux, nssm on Windows,
  or Docker Compose. It is expected to run unattended for months.
- **Downtime has a social cost.** A node going down ends a game session for
  real people, which is why fleet-wide destructive automation is treated with
  suspicion and per-node confirmation is the honest ceiling.

## Capabilities and Constraints

**Confirmed capabilities.** Multi-host fleet management · declarative Game Specs
(install script, per-platform image, startup command, ports, settings,
config-file templates) · cross-OS placement with Wine fallback · Steam auth
(anonymous by default; per-node encrypted credentials + deploy-time Steam Guard)
· BepInEx mod support for Unity titles · live console and CPU/memory/player-count
stats over WebSocket · in-browser file manager and editor · per-server SFTP ·
on-demand and scheduled `tar.gz` backups with `{{SLUG}}` destination templating
and off-node replication · cron schedules for power actions and backups ·
tri-state node health with watchdog re-adoption · crash watchdog with
auto-restart · optional Cloudflare DNS and UniFi port forwarding · argon2id auth,
RBAC, per-server ownership · AES-256-GCM encryption at rest for all
infrastructure secrets · audit log and Prometheus metrics · OpenAPI spec with
Swagger UI.

**Durable constraints future work must not break:**

- **LAN-first, no cloud dependency.** Kraken must stay fully functional with
  zero outbound internet. Cloudflare and UniFi integrations are strictly
  optional and must degrade cleanly when unconfigured.
- **Cross-OS parity.** Every feature works on both Linux and native-Windows
  nodes, or explicitly declares which it supports. No Linux-only assumptions
  about paths, signals, stats, or file operations.
- **Self-hosted simplicity.** Deployment stays two binaries plus Postgres. No
  required message broker, cache, Kubernetes, or external service.
- **Nothing is persisted in the clear.** Infrastructure secrets are sealed;
  session tokens are digests; passwords are argon2id.
- **The in-memory store is not a datastore.** An unset `KRAKEN_DATABASE_URL`
  means data is not persisted — never present that state as a working install.

**Terminology.** *Panel* (control plane) · *Agent* (per-host daemon) · *Node*
(a host the Panel manages) · *Game Spec* (the declarative game definition; the
"egg" equivalent) · *Server* (one deployed game instance) · *Fleet* (all servers
across all nodes) · *partial* (an Agent that answers but whose Docker daemon is
unreachable). Node health is exactly three states: **online**, **partial**,
**offline**.

**Address rule.** Anywhere a node address is entered or prefilled (enrollment
hosts, agent addresses), it is an **IP or DNS name — never a Windows computer
name**. The Agent reports its real listening port for prefills.

**Explicitly undecided — do not invent an answer:**

- **Telemetry stance.** Nothing is collected today and no collection exists in
  the code, but a "never, under any circumstances" commitment has not been made.
  Adding any collection is a product decision requiring an explicit yes.
- **Openness to outside contributions.** Whether external PRs are accepted at
  all, and under what bar, is deliberately unresolved (see BACKLOG.md). GPL-3.0
  is the license context.
- **Accessibility bar** — see below.
- **Agent self-update policy** (fleet-wide vs. per-node-with-confirmation) and
  **runtime spec catalog from an external repo** are designed but unshipped.

## Brand Commitments

- **Name:** Kraken. The Agent's default node identity (`abyss-node-01`) and the
  deep-sea vocabulary are part of the product's character, not decoration.
- **Voice:** operator-to-operator — precise, calm, technical, with a thin thread
  of deep-sea mythology. Buttons are bare imperative verbs. Errors state the
  problem and the fix in one line. Machine-readable values (ports, IDs,
  addresses, permission names, file paths) are always presented as such.
- **Visual authority already exists and is binding:** the Abyssal design system,
  documented at [`DESIGN.md`](DESIGN.md) with tokens in
  `web/design-system/`. It is the single source of truth for the visual
  language; this file does not restate or reopen it.
- **Brand mark:** `web/public/kraken-glyph-teal.png`.
- **Identity constraint:** wherever Linux appears in the UI, it is represented by
  the penguin (Tux) — never a terminal or CLI glyph.
- **License:** GNU GPL-3.0. Published as `briggleman/kraken`; container images
  at `ghcr.io/briggleman/kraken-panel` and `-agent`.

## Evidence on Hand

**Real, verifiable:**

- Nine bundled Game Specs in `internal/panel/catalog/bundled/` — Palworld,
  Valheim, V Rising, Enshrouded, Factorio, Abiotic Factor, DragonWilds,
  Windrose, plus a `windemo` Windows demo — authored against the SPECS.md
  convention in the same directory.
- Live-validated deployments recorded in BACKLOG.md with dates and specifics:
  modded Valheim booting through BepInEx/Doorstop, Abiotic Factor reaching a
  joinable session under Wine 10, a 3.6 GB Palworld backup replicated
  byte-identically to a real SFTP remote, Windows-native end-to-end on a Windows
  Docker daemon, and watchdog re-adoption verified by SIGKILL.
- Security posture and audit history in [`SECURITY.md`](SECURITY.md), including
  fixed findings (object-level authz/IDOR, WebSocket origin, token-in-URL,
  command injection) with the tests that cover them.
- A published OpenAPI spec (`internal/panel/api/openapi.yaml`), served with
  Swagger UI at `/docs`.
- A clean static-analysis stack: `go vet`, `staticcheck`, `deadcode`, `gosec`,
  `govulncheck`.

**Absent — must never be fabricated:** no customers, testimonials, case studies,
press, user counts, star counts, download figures, uptime statistics, benchmarks,
or performance comparisons against other panels. No pricing, plans, licensing
tiers, hosted offering, SLA, or support commitment. No screenshots or demo
recordings currently exist as committed assets. No roadmap dates beyond what
BACKLOG.md states.

## Product Principles

1. **Honest state over reassuring state.** A node whose Docker daemon is down
   reads *partial* with the daemon's own error, not *online* with fake capacity.
   Never show a green light the system hasn't earned; the tri-state health work
   and the rejection of the fake-runtime fallback are this principle in code.
2. **An indicator operators learn to ignore is worse than no indicator.** Every
   warning must earn its attention with a trigger that only fires when something
   actually needs a human. Amber-most-of-the-time is a defect.
3. **Simple to run is the feature.** Complexity the operator can feel — an extra
   service, a second config surface, a required cloud account — must buy
   something large. When in doubt, fewer moving parts.
4. **Both operating systems, or say so.** Windows nodes are a first-class target,
   not a port. A feature that quietly assumes Linux is unfinished.
5. **Destructive actions are reversible or confirmed.** Staging is separate from
   the irreversible step; the fleet is never operated on wholesale without the
   operator saying so per node.

## Accessibility & Inclusion

**Explicitly undecided.** No conformance target has been set and no user with a
specific access need is known. Future work should neither assume a WCAG level is
required nor assume none applies — raise it as a decision when a surface would
materially depend on the answer. What the existing implementation already does
(full keyboard navigation in the `Select` component, status conveyed as hue **+**
icon **+** label rather than color alone, a global `prefers-reduced-motion`
honor) should be preserved regardless of where that decision lands.
