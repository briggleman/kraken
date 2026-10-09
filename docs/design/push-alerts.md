# Push alerts: the Panel ⇄ relay ⇄ device contract

**Status:** implemented 2026-10-09 — scoped from issue #348 and built in #414 (this contract), #415
(the watchdog counter), #416 (the seal and the relay client), #418 (devices), #419 (the event
pipeline) and #420 (two fixes the end-to-end drill found). The transport (Panel → Kraken push relay
→ APNs, the Home Assistant model) was decided 2026-09-10 and is recorded in the iOS companion's
PRODUCT.md (`briggleman/kraken-ios`). This document is the wire contract the three parts build
against: the Panel (this repo), the relay (separate, not yet built), and the companion app. Where
the build differed from the first draft, this text was corrected to match what shipped. The
operator's view is the wiki page `operate/alerts`; the security model is in SECURITY.md, "Push
alerts".
**Scope:** what the Panel decides, encrypts and sends, and what the relay and the app must do with
it. The relay's hosting, domain, rate limits and licence are out of scope and undecided.

## Principles

- **LAN-first stays true.** The relay is optional and off by default (`KRAKEN_PUSH_RELAY_URL` unset).
  An unconfigured Panel loses alerts and nothing else.
- **The relay sees ciphertext and a device token, nothing more.** No class, no server name, no
  sentence, no user. Alert payloads are end-to-end encrypted to the device's own key.
- **An alert that arrives late is worse than one that honestly didn't.** The Panel retries briefly,
  then drops the alert and says so in the audit log. It never queues alerts for hours.
- **The Panel decides; the phone renders.** Rules, escalation and RBAC are evaluated Panel-side, so
  the phone does not have to be awake to notice a pattern. Interruption level, sound and grouping
  are the app's job, driven by the class the Panel sends.

## Alert classes and events

| class | events (on by default) | meaning |
| --- | --- | --- |
| `attend` | node offline, node partial, server stopped unexpectedly, backup failed, watchdog crash-loop | something needs a human |
| `healed` | watchdog restarted a server | the fleet fixed itself; repetition is the signal |
| `alive` | player joined | social and quiet, grouped per server |

**Escalation.** The restart that brings one server's watchdog restarts inside the last hour to
three is sent as `attend` with the event `crash_loop` instead of `healed`. The window is per server
and slides. For an hour after a `crash_loop`, further restarts of that server send nothing — the
`crash_loop` already said it, and a `healed` after it would read as a contradiction. Once that hour
has passed a restart is `healed` again, or the next `crash_loop` if the server is still looping.

Where each event comes from in the Panel:

| event | source |
| --- | --- |
| `node_offline`, `node_partial` | a node's status transition (`online`/`cordoned` → `offline`/`partial`), observed wherever the status is decided: every probe of a node (the node reconciler, a ping) and the tunnel's disconnect hook, which is where a tunnel-mode node goes offline. **Confirmed before it is sent** — see below |
| `server_crashed` | the server reconciler's transition from a live state into `crashed`, sent once the write lands; an operator stop or kill never produces one |
| `watchdog_restart`, `crash_loop` | the Agent's `ServerStatus.watchdog_restarts` counter (see below), diffed by the server reconciler on every poll |
| `backup_failed` | a backup watcher that follows every backup the Panel starts by hand or on a schedule — polling `ListBackups` every 15 seconds, for at most 30 minutes, until it reads `ready` or `failed` — and a retire's final backup that failed, which abandons the retire |
| `player_joined` | the server reconciler's roster diff from `ServerStatus.last_stats.online_players`; a count-only spec yields "a player joined · n online" with no name |

**Node falls are confirmed.** Most falls heal in seconds — an Agent restarting into an update, a
network blip, Docker Desktop restarting — and an alert for each teaches the operator to ignore the
next one. A fall schedules a confirmation 60 seconds out; when it fires the Panel reads the node's
status from the store, and sends the alert only if the node is still `offline` or `partial`. A
second fall while one confirmation is pending does not schedule another. When this Panel is pushing
the node an Agent update, or has told it to restart into one, the confirmation waits instead for
that update job's own 10-minute window, so a slow Windows service restart is not an outage. A
confirmed fall is stamped with the confirmation's time, not the fall's: the relay's two-minute
window runs from the alert's time. The first status the Panel sees for a node after it starts is a
baseline, never a fall — every Panel restart briefly sees its tunnel nodes offline.

**Players.** Joins are diffed between polls whose player query answered; a poll that did not answer
keeps the previous roster. A server the Agent reports not running has nobody on it, so players
reconnecting after a crash or a start are joins. A server sends at most one `alive` alert a minute:
joins inside the minute are folded into one sentence and sent when it is up, and dropped if the
server stops first.

**The watchdog signal.** Crash auto-restarts happen inside the Agent (`monitor.go`) and never reach
the Panel, and a fast restart can fall between two 4-second reconcile polls. `ServerStatus` carries
`watchdog_restarts` (restarts the current monitor has performed since the last operator start or
restart) and `last_watchdog_restart_unix_ms`. The Panel diffs the counter per server: the first
value after a Panel start is the baseline, a lower value is a reset, and a rise seen while the
server reads `crashed` sends nothing (the `server_crashed` alert covers it). An Agent that predates
the fields reports zero, so it produces no `healed` alerts; a crash it gives up on still produces
`server_crashed`.

## Devices

A device belongs to a **user**, not to a session. Sessions expire (`KRAKEN_SESSION_TTL`, 24h by
default); a session-bound device would go silent every day.

Stored per device (`device_registrations`), keyed by **(`user_id`, `id`)**: device ids are not
secret, so a second account registering the same install id gets a row of its own and never takes
over the first user's. The same install signed into two accounts receives both users' alerts until
it signs out of one.

| field | notes |
| --- | --- |
| `id` | the app's own stable install id (UUID, any case, stored lowercase), chosen on the device; registration is idempotent on (user, id) |
| `user_id` | the owner; alerts are filtered by this user's permissions |
| `platform` | `ios` |
| `apns_token` | hex; rotation updates it in place; sealed at rest with the Panel's secrets key |
| `apns_environment` | `production` or `sandbox` (a development build talks to the sandbox gateway) |
| `public_key` | the device's X25519 public key, raw 32 bytes, base64; refused unless it is a usable X25519 point |
| `rules` | class toggles (`attend`, `healed`, `alive`, all on by default) and `alive_muted_servers` (server ids the user can view) |
| `name` | the device's display name, for a future devices surface |
| `created_at`, `last_seen_at`, `last_sent_at` | `last_seen_at` moves on every registration refresh |
| `token_invalid_at` | set when the relay reports the token dead (APNs 410), with the token that was sent; cleared by the next registration |

**Revocation.** `DELETE /devices/{id}` by its owner, or by a user with `user.manage` naming the
owner with `?user=`. Sign-out revokes: `POST /auth/logout` accepts an optional `device_id`, and
revokes the caller's own registration of that install. Disabling a user revokes their devices;
deleting one deletes them.

**RBAC at send time.** Every alert is filtered per device at the moment it is sent: a server alert
goes only to devices whose user can view that server (`server.view` plus per-server ownership, the
same rule the API applies, `store.MayAccessServer`), and a node alert only to users with
`node.view`. A disabled user gets nothing, whatever their devices say. Users and roles are read per
event, so a user who loses access to a server stops getting its alerts on the next event, with no
device change needed.

## API (`/api/v1`, session-authenticated)

| method and path | what |
| --- | --- |
| `POST /devices` | register or refresh: `{id, platform, apns_token, apns_environment, public_key, name}`; idempotent on (caller, `id`); answers the device with its rules; unknown fields are ignored, so a newer app works with an older Panel |
| `GET /devices` | the caller's devices (a user with `user.manage` may pass `?user=`) |
| `PATCH /devices/{id}/rules` | `{attend?, healed?, alive?, alive_muted_servers?}`, on the caller's own device only; unknown fields ignored |
| `DELETE /devices/{id}` | revoke the caller's device, or another user's with `?user=` and `user.manage` |
| `POST /devices/{id}/test` | send a test alert (class `attend`, event `test`, "test alert from" and the host the caller used) to the caller's own device and wait for the outcome — at most 20 seconds — answering `sent`, `token_dead`, `rejected`, `dropped`, or `disabled` when no relay is configured. It ignores the device's rules and a dead-token mark: it is how the owner finds out |

## The encrypted payload

**Scheme:** HPKE (RFC 9180), base mode, ciphersuite **DHKEM(X25519, HKDF-SHA256) / HKDF-SHA256 /
ChaCha20-Poly1305** — Go's `crypto/hpke` on the Panel (stdlib since Go 1.26, no new dependency), and
CryptoKit's `HPKE.Ciphersuite.Curve25519_SHA256_ChachaPoly` on the device (iOS 17+, which is the
companion's deployment target). `info` is the ASCII string `kraken-push-v1`; there is no AAD.

**Envelope:** `base64(enc ‖ ciphertext)`, where `enc` is the 32-byte encapsulated key. One seal per
device per alert.

**Plaintext** (UTF-8 JSON):

```json
{
  "v": 1,
  "class": "attend",
  "event": "server_crashed",
  "server_id": "4866d26c-…",
  "node_id": "bd70f48c-…",
  "title": "dragonwilds-01",
  "body": "dragonwilds-01 stopped unexpectedly — exit 0xC0000135, a DLL the game needs is missing",
  "thread": "server:4866d26c-…",
  "ts_ms": 1791555300000
}
```

`title` is the object's name, `body` is one sentence in the house voice (the same voice as the
console's system lines), `thread` groups notifications (every event of a server under
`server:<id>`, a node's under `node:<id>`, the test alert under `test` with the title "Kraken"),
and `server_id` / `node_id` let the app open the right screen. Unknown fields are ignored by the
app; `v` changes only for an incompatible change.

## The relay request

```
POST {KRAKEN_PUSH_RELAY_URL}/v1/push
Content-Type: application/json
User-Agent: kraken-panel/<version>
X-Kraken-Panel-Id: <the Panel's random install id>

{"token": "<apns token>", "environment": "production", "payload": "<envelope>"}
```

The relay sends APNs a **generic alert with `mutable-content: 1`** carrying the envelope in a custom
key (`k`); the app's Notification Service Extension decrypts it and sets the title, body,
interruption level (`attend` → time-sensitive, `healed` → active, `alive` → passive) and thread.
If decryption fails the extension shows a generic "Kraken alert" rather than dropping silently.

The User-Agent is not decoration: Cloudflare in front of a Panel or relay refuses default library
agents with `403 error code: 1010`.

**Responses:** `2xx` sent. `410` the token is dead: the Panel sets `token_invalid_at` and stops
sending to that device until it registers again. `400` the request is wrong: dropped, not retried.
`429`, `5xx`, timeouts and connection errors: retried with backoff (1s, 3s, 9s, 27s, then every
30s), and dropped once the alert is **two minutes old** — in practice after the seventh attempt,
about 100 seconds in, since an eighth would land past the window.

**Relay authentication** is the relay's decision (out of scope). The Panel sends its install id on
every request and will send `Authorization: Bearer <KRAKEN_PUSH_RELAY_TOKEN>` when that variable is
set, so the relay can adopt a registration scheme without a Panel change. The Panel warns at
startup when a token is set and the URL is plain `http://` to a host that is not loopback or a
private address.

## Audit

One entry per alert per device, actor `system`, on the same log as everything else, target
`device/<id>` and status the relay's HTTP status: `push.sent`, `push.failed` (a non-retryable
refusal, a dead token included), `push.dropped` (out of time). The action names the event, the
object and the device's user, plus the relay's reason on a failure — for example
`push.dropped — server_crashed (server dragonwilds-01) to admin: the alert would be two minutes old
before another attempt, after 7 attempt(s) (the relay answered 503)`. Never the token, the key or
the payload.

## Testing the contract

`krakenctl push-relay-stub` runs a local relay on the LAN that accepts the request above, opens the
envelope with a device key it is given, and prints each alert — the end-to-end check for the Panel,
and a stand-in for the companion's developer until the real relay exists. A Go test seals with the
Panel's code and opens with a reference implementation, and the same fixed test vector (key pair,
plaintext, envelope) is published in `internal/panel/push/testdata/` for the app's own tests.

On the fake-live stack every event can be produced by hand through a server's console: `crash`,
`watchdog`, `join <name>` / `leave <name>` (for a spec with a `log` player query), `backupfail
<reason>` / `backupfail off`, and `runtime down` / `runtime up` (node-wide). Stopping the fake
Agent produces `node_offline`.

## Out of scope

The relay service; the app's registration UI, notification rendering and settings (they land in
`kraken-ios` against this contract); a friendlier audit feed.
