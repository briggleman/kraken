---
title: Limits and logging
description: The three rate limiters and their numbers, the per-username limiter that works when the client address does not, the body caps, what the Panel logs at each level, and the Prometheus metrics that show you refusals without turning the log level down.
section: configure
order: 24
---

## Rate limits

The Panel limits the two things an unauthenticated caller can reach, and limits
login along a second axis that needs no address at all:

| what | keyed on | rate | burst |
| --- | --- | --- | --- |
| download-token redemption | client address | 30/min | 10 |
| `POST /auth/login` | client address | 20/min | 20 |
| login + change-password **failures** | submitted username | 10/min | 10 |

A refusal is a **429** in the ordinary JSON error envelope with `Retry-After`,
and a refused request does not consume future capacity.

The download limit sits on the **token branch**, not on the route: a
session-authenticated download is not what the limit is for, and an operator
must not be refused for sharing a NAT address with somebody probing tokens. The
login numbers are deliberately generous for the same reason — locking a team out
of their own Panel is a worse failure than the guessing being slowed.

**Keys are per client, and the client is resolved once.** IPv4 is keyed per
address; **IPv6 is aggregated to the /64**, because a single subscriber is
routinely delegated a whole /64 and per-address keying would hand one attacker
2^64 independent buckets. The table is swept for idle entries and, at its cap,
evicted down to a watermark; entries that cannot make a request right now are
passed over, so filling the table is not a way to buy back a spent bucket.

"Per client" means whatever [`clientIP`](/wiki/configure/reverse-proxy/)
resolves to. Behind a proxy with no `KRAKEN_TRUSTED_PROXIES` set, that is *the
proxy*, for everybody, and the per-address limiters become one shared bucket a
stranger can exhaust for the whole fleet. Of the ways to misconfigure a Panel,
it is the one I would check first.

## The per-username login limiter

The address-keyed limiter has a topology it cannot survive. On Docker Desktop
every connection to a published port arrives rewritten to the VM gateway
`192.168.65.1` — including the connections a reverse proxy on the same host
publishes — so the Panel resolves the entire internet to one address. The login
bucket is then twenty attempts a minute for everybody at once: protective of
nobody, and a self-inflicted lockout the first time a stranger finds the login
page. [Behind a reverse proxy](/wiki/configure/reverse-proxy/) has the full
story and the fix.

So login is limited on a second key that owes nothing to the network: **the
submitted username**, normalised (trimmed, lower-cased), at ten a minute with a
burst of ten. Three things about it are worth knowing:

- **It counts failures only.** A correct password spends nothing. Somebody
  guessing at your username cannot keep you out of your own Panel by exhausting
  a budget you never touch — they can only keep *themselves* out, which is the
  point.
- **It applies to every submitted username, whether or not the account
  exists.** A 429 that came back only for real accounts would answer "does this
  user exist?" for anybody patient enough to ask eleven times.
- **`POST /auth/change-password` spends the same budget**, keyed on the
  authenticated user, when the *current* password is wrong. That route verifies
  a password exactly as login does, so leaving it outside would make a stolen
  session the way around the login limiter. The consequence is worth stating
  plainly: fumble your current password ten times on the change-password screen
  and your own sign-in is refused for the rest of that minute.

A brute-force against one account is therefore slowed even when the Panel has
no idea who is calling, and every other account carries on unaffected.

### exempting an address the per-IP limit cannot help

```sh
KRAKEN_RATE_LIMIT_IP_SKIP=192.168.65.1
```

A comma-separated list of CIDRs (bare IPs allowed) whose resolved client address
is exempt from **the per-address limiters**. It exists for exactly the situation
above: an address that stands in for everybody is not a client, and limiting it
refuses the whole internet together rather than refusing anyone in particular.

It does **not** exempt anything from the per-username limiter, which is what
makes it safe to set: password guessing is still capped at ten a minute per
account. An unparseable entry is a startup error, the same as
`KRAKEN_TRUSTED_PROXIES`.

Reach for the shared-Docker-network fix first. This is for when you cannot.

### the off switch

```sh
KRAKEN_RATE_LIMITS=all        # the default; "on" is accepted as its old spelling
KRAKEN_RATE_LIMITS=login      # the login limiters only
KRAKEN_RATE_LIMITS=downloads  # the download-token limiter only
KRAKEN_RATE_LIMITS=off        # neither
```

Anything else is a **startup error**: every value here is a deliberate posture,
and a typo must not be read as one of them. Anything other than `all` is logged
loudly at startup.

The switch is split because the two surfaces fail differently. The advice for a
Panel behind a NAT that erases the client used to be `off`, which threw away the
token-redemption limiter as well — a limiter that has no such problem, since it
guards a credential nobody shares.

## Body caps

Not configurable, listed here because the answers arrive as HTTP errors:

- **Every JSON request body is capped at 4 MiB.** Sized off the largest
  legitimate body, which is the in-browser editor saving a 1 MiB file,
  JSON-escaped. Without a cap, any authenticated caller could pin arbitrary
  Panel memory with one request, because `json.Decoder` reads until the body
  ends.
- **Uploads are capped at 64 MiB** plus a megabyte of multipart framing, and
  answer **413** past it. `ParseMultipartForm`'s argument is only the in-memory
  threshold — everything past it spills to temp files, unbounded — so without
  this one authenticated request could fill the Panel's disk. Anything larger
  belongs on SFTP.
- **A download token's path set** is capped at 256 paths of 4096 bytes, since a
  mint holds that set in memory for 60 seconds.

## Log level

```sh
KRAKEN_LOG_LEVEL=debug   # debug | info (default) | warn | error
```

An unrecognised value falls back to `info`: a typo must not silence the Panel.

Several diagnostics are `Debug` on purpose, because they are writable by an
unauthenticated caller. A rejected download token is the example. Turn the level
down when you are diagnosing something; leaving it there hands a stranger a
write into the log an operator actually reads.

Logs are JSON. Read them wherever the Panel runs:

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.full.yml logs -f panel
journalctl -u kraken-panel -f
```

The Agent keeps its own log: `journalctl -u kraken-agent` on Linux,
`C:\kraken\state\agent.log` on Windows (JSON, rotated at 10 MiB).

## Audit retention

The audit log is the one thing the Panel keeps in Postgres rather than on
stdout, so it has a window of its own:

```sh
KRAKEN_AUDIT_RETENTION_DAYS=90   # 0 keeps every entry forever
```

A daily job deletes entries past the window in batches. The value must be a
whole number of days, `0` or more; anything else stops startup. The mechanics,
and what the console shows for each setting, are on [the audit
log](/wiki/operate/audit/).

## Metrics

The Panel exports Prometheus metrics at **`GET /metrics`**, unauthenticated
alongside `/healthz` and `/readyz`. It belongs on a network your scrapers can
reach and strangers cannot, which is where the rest of the Panel belongs too.

| metric | what |
| --- | --- |
| `kraken_build_info{version}` | the running Panel's version |
| `kraken_servers{state}` | servers by lifecycle state |
| `kraken_nodes_total`, `kraken_nodes_online` | fleet size and health |
| `kraken_schedules_total` | cron schedules |
| `kraken_http_requests_total` | request counter |
| `kraken_audit_events_total` | audit rows written |
| `kraken_download_tokens_rejected_total` | refused download-token redemptions |
| `kraken_rate_limited_total` | refusals, per limiter |

`kraken_rate_limited_total` carries a `limiter` label: `download`, `login` (the
per-address one) and `login_user` (the per-username one). A spike on
`login_user` with nothing on `login` is somebody working through one account
from many addresses — or from behind a NAT, where `login` cannot see them apart
in the first place.

The last two are why the quiet `Debug` lines are also counted: they give an
operator the signal without turning the log level down. A 429'd login never
reaches the handler that audits a failed attempt, so the limiter writes that row
itself, once per key per window rather than once per request.

:::note
A steady trickle on `kraken_download_tokens_rejected_total` is somebody probing,
and it is what the rejection path is for: a download token is single-use, sixty
seconds long, scoped to one server and one exact path set, and bound to the user
and session that minted it. A spike on `kraken_rate_limited_total` for the login
limiter, with one client address behind it, is worth reading the audit log over.
:::
