# Push alerts: the Panel ⇄ relay ⇄ device contract

**Status:** proposed 2026-10-09 — scoped from issue #348. The transport (Panel → Kraken push relay →
APNs, the Home Assistant model) was decided 2026-09-10 and is recorded in the iOS companion's
PRODUCT.md (`briggleman/kraken-ios`). This document is the wire contract the three parts build
against: the Panel (this repo), the relay (separate, not yet built), and the companion app.
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

**Escalation.** The third watchdog restart of one server within one hour is sent as `attend` with
the event `crash_loop` instead of a third `healed`. The window is per server and slides.

Where each event comes from in the Panel:

| event | source |
| --- | --- |
| `node_offline`, `node_partial` | the node reconciler's status transition (`online`/`cordoned` → `offline`/`partial`) |
| `server_crashed` | the server reconciler's transition into `crashed`; an operator stop or kill never produces one |
| `watchdog_restart`, `crash_loop` | the Agent's `ServerStatus.watchdog_restarts` counter (new; see below), diffed by the server reconciler |
| `backup_failed` | a backup watcher that follows every backup the Panel starts (scheduled, manual, pre-restore) until it reads `ready` or `failed` |
| `player_joined` | the server reconciler's roster diff from `ServerStatus.last_stats.online_players`; a count-only spec yields "a player joined · n online" with no name |

**The watchdog signal.** Crash auto-restarts happen inside the Agent (`monitor.go`) and never reach
the Panel, and a fast restart can fall between two 4-second reconcile polls. `ServerStatus` gains
`watchdog_restarts` (restarts the current monitor has performed since the last operator start or
restart) and `last_watchdog_restart_unix_ms`. The Panel diffs the counter per server. An Agent that
predates the fields reports zero, so it produces no `healed` alerts; a crash it gives up on still
produces `server_crashed`.

## Devices

A device belongs to a **user**, not to a session. Sessions expire (`KRAKEN_SESSION_TTL`, 24h by
default); a session-bound device would go silent every day.

Stored per device (`device_registrations`):

| field | notes |
| --- | --- |
| `id` | the app's own stable install id (UUID), chosen on the device; registration is idempotent on it |
| `user_id` | the owner; alerts are filtered by this user's permissions |
| `platform` | `ios` |
| `apns_token` | hex; rotation updates it in place |
| `apns_environment` | `production` or `sandbox` (a development build talks to the sandbox gateway) |
| `public_key` | the device's X25519 public key, raw 32 bytes, base64 |
| `rules` | class toggles (`attend`, `healed`, `alive`, all on by default) and `alive_muted_servers` (server ids) |
| `name` | the device's display name, for a future devices surface |
| `created_at`, `last_seen_at`, `last_sent_at` | `last_seen_at` moves on every registration refresh |
| `token_invalid_at` | set when the relay reports the token dead (APNs 410); cleared by the next registration |

**Revocation.** `DELETE /devices/{id}` by its owner (or an admin). Sign-out revokes: `POST
/auth/logout` accepts an optional `device_id`. Disabling or deleting a user revokes their devices.

**RBAC at send time.** Every alert is filtered per device at the moment it is sent: a server alert
goes only to devices whose user can view that server (role plus per-server ownership, the same check
the API makes), and a node alert only to users with `node.view`. A user who loses access to a server
stops getting its alerts on the next event, with no device change needed.

## API (`/api/v1`, session-authenticated)

| method and path | what |
| --- | --- |
| `POST /devices` | register or refresh: `{id, platform, apns_token, apns_environment, public_key, name}`; idempotent on `id`; answers the device with its rules |
| `GET /devices` | the caller's devices (an admin with `user.manage` may pass `?user=`) |
| `PATCH /devices/{id}/rules` | `{attend?, healed?, alive?, alive_muted_servers?}` |
| `DELETE /devices/{id}` | revoke |
| `POST /devices/{id}/test` | send a test alert (class `attend`, event `test`) through the configured relay; answers whether the relay accepted it |

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
  "body": "stopped unexpectedly — exit 0xC0000135, a DLL the game needs is missing",
  "thread": "server:4866d26c-…",
  "ts_ms": 1791555300000
}
```

`title` is the object's name, `body` is one sentence in the house voice (the same voice as the
console's system lines), `thread` groups notifications (per server for `alive` and `healed`, per
node for node events), and `server_id` / `node_id` let the app open the right screen. Unknown
fields are ignored by the app; `v` changes only for an incompatible change.

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
30s), and dropped once the alert is **two minutes old**.

**Relay authentication** is the relay's decision (out of scope). The Panel sends its install id on
every request and will send `Authorization: Bearer <KRAKEN_PUSH_RELAY_TOKEN>` when that variable is
set, so the relay can adopt a registration scheme without a Panel change.

## Audit

One entry per alert per device, actor `system`, on the same log as everything else:
`push.sent`, `push.failed` (a non-retryable refusal), `push.dropped` (out of time), each naming the
event, the object and the device — never the token or the payload.

## Testing the contract

`krakenctl push-relay-stub` runs a local relay on the LAN that accepts the request above, opens the
envelope with a device key it is given, and prints each alert — the end-to-end check for the Panel,
and a stand-in for the companion's developer until the real relay exists. A Go test seals with the
Panel's code and opens with a reference implementation, and the same fixed test vector (key pair,
plaintext, envelope) is published in `internal/panel/push/testdata/` for the app's own tests.

## Out of scope

The relay service; the app's registration UI, notification rendering and settings (they land in
`kraken-ios` against this contract); a friendlier audit feed.
