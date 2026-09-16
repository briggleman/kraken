---
title: Limits and logging
description: The two rate limiters and their numbers, the body caps, what the Panel logs at each level, and the Prometheus metrics that show you refusals without turning the log level down.
section: configure
order: 24
---

## Rate limits

The Panel limits the two things an unauthenticated caller can reach:

| what | rate | burst |
| --- | --- | --- |
| download-token redemption | 30/min | 10 |
| `POST /auth/login` | 20/min | 20 |

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
proxy*, for everybody, and both limiters become one shared bucket a stranger can
exhaust for the whole fleet. Of the ways to misconfigure a Panel, it is the one
I would check first.

### the off switch

```sh
KRAKEN_RATE_LIMITS=off
```

Disables both limiters, logged loudly at startup, for an edge that already does
this. Anything other than `off` leaves them on, which is the default.

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

The last two are why the quiet `Debug` lines are also counted: they give an
operator the signal without turning the log level down. A 429'd login never
reaches the handler that audits a failed attempt, so the limiter writes that row
itself, once per client per window rather than once per request.

:::note
A steady trickle on `kraken_download_tokens_rejected_total` is somebody probing,
and it is what the rejection path is for: a download token is single-use, sixty
seconds long, scoped to one server and one exact path set, and bound to the user
and session that minted it. A spike on `kraken_rate_limited_total` for the login
limiter, with one client address behind it, is worth reading the audit log over.
:::
