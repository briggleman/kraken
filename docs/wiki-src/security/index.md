---
title: Security
description: What Kraken protects and how, in the audit's own words — passwords, sessions, transport, secrets at rest and RBAC — plus the seven steps for a Panel reachable from the internet, what is deliberately not protected, and how to report something.
section: security
order: 50
---

The canonical document is
[SECURITY.md](https://github.com/briggleman/kraken/blob/main/SECURITY.md),
which is an audit history rather than a marketing page: every claim on it is
tied to a file, a test name, or a triage decision with a date. This page
summarises it and quotes it, because a security property paraphrased is a
security property weakened.

## The posture

**Passwords.** "argon2id (`internal/panel/auth/password.go`) with a
`crypto/rand` 16-byte salt, PHC-encoded, verified with
`subtle.ConstantTimeCompare`. Hashes are never stored in plaintext, never
logged, and `User.PasswordHash` is `json:"-"` so it cannot serialize to
clients. Login is timing-safe (dummy verify on unknown user) to resist user
enumeration."

**Sessions.** "32 bytes from `crypto/rand`, opaque, URL-safe; sessions expire
and are deleted on expiry. Stored as a **SHA-256 digest** at rest … so a
database dump cannot be replayed as live sessions." The session token never
goes in a URL: the console WebSocket carries it in a subprotocol instead, and
that rule "remains absolute: a session credential never goes in a URL."

**Transport, Panel to Agent.** "mutual TLS with a pinned server name
(`mtls.ClientTLS`); no `InsecureSkipVerify`. TLS 1.2 minimum." Identity is
enforced in both directions: only the Panel may drive an Agent's gRPC listener,
because `mtls.RequirePeerCN` checks the verified client leaf's subject against
the Panel's name, and "the CN is authoritative because the signer sets it …
so an enrollee cannot forge it." The reverse-tunnel listener accepts Agent
certs, which is what it is for, and authorizes them by their per-node URI-SAN
identity.

**Secrets at rest.** Every reversible secret the Panel persists is sealed with
AES-256-GCM before it reaches Postgres: "the Cloudflare API token, UniFi API
key, the Agent-enrollment **CA private key**, and each node's **SFTP backup
credentials and Steam password**". The master key is `KRAKEN_SECRETS_KEY`,
base64 of 32 bytes, or an auto-generated key persisted to the config file at
mode `0600`, **outside the database it protects**. A startup warning names the
auto-generated case.

Two things are deliberately *not* encrypted, and SECURITY.md says why. A
password hash "is the correct at-rest form, so it is not additionally
encrypted." A server's game settings, including a game's own join password, are
"low-sensitivity *game config* … not infrastructure credentials", stored in the
clear and "**accepted as low-risk**".

**SQL and paths.** "every query in `internal/panel/store/postgres` is
parameterized (`$1,$2,…`); no string-built SQL." The Agent's `safePath()`
"cleans and prefix-checks against `/data`; `..`, absolute paths, and
`/data/../x` escapes are all rejected post-`path.Clean`."

**Browser headers.** A Content-Security-Policy with `script-src 'self'` and
`style-src 'self'`, both without `'unsafe-inline'`, plus `frame-ancestors
'none'`, `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff` and
`X-Frame-Options: DENY`. `img-src` is deliberately permissive, because Game
Specs carry operator-supplied artwork URLs.

## Roles

Four built-in roles, and a permission model of `<domain>.<action>` strings where
a trailing `.*` is a domain wildcard.

| role | holds |
| --- | --- |
| **Owner** | `*`. Everything. |
| **Admin** | `server.*`, `spec.*`, `node.*`, `backup.*`, `settings.*`, `user.manage`, `audit.view` |
| **Operator** | view, create, power, console read and command, files read and write, server config, `backup.manage`, `spec.view`, `node.view` |
| **Read-only** | `server.view`, `server.console.read`, `spec.view`, `node.view` |

On top of that sits object-level ownership. A server records the user who
created it, and every server-scoped route checks it after loading the row.
Owner and Admin hold `server.any` through their wildcards; Operator and
Read-only do not, so they are scoped to servers they created. Lists are filtered
to what you may see, and a denial is **404, not 403**, "so another user's server
isn't revealed to exist."

## Hardening a Panel the internet can reach

Seven steps. None of them is optional if the Panel has a public hostname.

**1. Name your proxy.** Set `KRAKEN_TRUSTED_PROXIES` to the CIDRs of whatever is
in front. Without it the peer is the proxy on every request, "so every audit row
records the proxy and both limiters collapse into a single shared bucket that
one stranger can exhaust for everybody." An unparseable entry is a startup
error, deliberately, because "a misconfiguration that looks like a working
Panel" is worse than a refusal to start.

**2. Keep the HTTP port off the public interface.** Bind the Panel's HTTP
listener, `8080` by default, to loopback and let the proxy reach it there.
**Leave `9443` reachable from your Agents.** Raw mTLS cannot be proxied, and
binding the tunnel listener to loopback alongside the HTTP port takes the whole
fleet offline at once. It is the single most common way to break a working
install while hardening it.

**3. Authenticate the origin.** Behind Cloudflare, turn on Authenticated Origin
Pulls so your origin only answers Cloudflare, not anyone who learned its
address. [Behind a reverse proxy](/wiki/configure/reverse-proxy/) has the exact
configuration, and the failure mode that looks like a certificate problem and is
not.

**4. Leave the rate limiters on.** `KRAKEN_RATE_LIMITS` defaults to `on`:
login at 20 a minute with a burst of 20, download-token redemption at 30 a
minute with a burst of 10, keyed per client with "**IPv6 … aggregated to the
/64**". Turn them off only for an edge that already does this, and know the
Panel logs that loudly at startup when you do.

**5. Decide your log level.** `KRAKEN_LOG_LEVEL` defaults to `info`. Rejected
download-token redemptions log at `debug` on purpose, since "an unauthenticated
caller chooses when they are written", and they are counted at every level as
`kraken_download_tokens_rejected_total`. Turn `debug` on when you are
investigating, not as a standing setting.

**6. Set your origins.** `KRAKEN_ALLOWED_ORIGINS` is what cross-origin
WebSocket upgrades are checked against. Same-origin is always allowed; the
default is the localhost development origins, which is not your public hostname.

**7. Scope the setup surface.** `KRAKEN_SETUP_ALLOWED_CIDRS` gates the whole
`/setup/*` group, and "external sources receive 403 regardless of credentials."
Behind a co-located tunnel the Panel would otherwise see `127.0.0.1` for the
entire public internet, which is the second reason step 1 matters.

Two more that are not on the checklist because they are not about the Panel's
configuration: keep SFTP, `:2022` on a node and the Panel's SFTP proxy ports, on
your LAN or a VPN, and do not publish an Agent's gRPC port. "Agent gRPC has no
application-level auth; mTLS is the whole trust boundary, and it fronts the
Docker socket."

## What is deliberately not protected

**A download URL lives in browser history.** "The URL — token included — sits in
the browser's history, and on some platforms in the download manager's record,
for as long as the browser keeps it. Anyone with the victim's browser profile
can read it there. What they get is nothing: by then the token has been redeemed
(deleted) and, failing that, has expired within the minute."

**Plain HTTP on a LAN is a first-class deployment.** There is no
`upgrade-insecure-requests` in the CSP, and the Panel does not assert HSTS:
"The Panel serves plaintext HTTP on a LAN by design, so it is the wrong layer to
assert HSTS." Whatever terminates TLS should set it.

**Agent certificates share one logical identity.** Every Agent cert carries
`CN=kraken-agent`, which the Panel pins for all nodes, so they are mutually
interchangeable as *server* certs. This "is acceptable when all nodes are
equally trusted", and it is a standing open recommendation to bind per-node
identity for mixed-trust fleets. Its worst consequence is already closed: since
the cross-node fix, an Agent cert is not accepted as a *client* by a peer Agent
at all.

**Node-local secrets are not sealed on the node.** A server's runtime spec and
its rendered config files live on the node at mode `0600` and `0644`, and they
can carry admin or RCON passwords. "Both live on a host whose Docker socket the
Agent controls — an attacker with file access there has already won." Encrypting
them "would require a node-local key sitting next to the ciphertext, which buys
nothing."

**Three dependency advisories are accepted with monitoring**, all with no
upstream fix: two Docker Engine SDK advisories, reachable only from the Agent
and only through file-copy functions "not on any Kraken call path", and one in
an SMB client reached by dialling an operator-configured backup target.

## Reporting something

Use the repository's **Security** tab on
[github.com/briggleman/kraken](https://github.com/briggleman/kraken) and report
it privately if the option is offered there. If it is not, open an issue that
says you have a security report and asks for a private channel, and keep the
exploit detail out of it until you have one.

Kraken is a single-maintainer project, so expect a human timescale rather than a
corporate one. The detail that helps most is the same one SECURITY.md's own
entries lead with: which call path reaches it.
