---
title: Panel configuration
description: Every KRAKEN_* environment variable the Panel reads, with its default and what it does — generated from internal/panel/config/config.go so it cannot drift from the code.
section: configure
order: 20
---

The Panel is configured entirely from the environment, read once at startup.
Defaults make it runnable out of the box for local development, which is not the
same as runnable in production — see [installing the
Panel](/wiki/install/panel/) for the handful that matter on a real deploy.

Where the variables go depends on how you run it: `deploy/.env` (compose),
`/etc/kraken/panel.env` (systemd), or the process environment.

:::note
**The tables below are generated** from the `env(…)` calls and their comments in
[`internal/panel/config/config.go`](https://github.com/briggleman/kraken/blob/main/internal/panel/config/config.go),
grouped the way that file groups them. Editing this page by hand is pointless —
the next `make wiki` overwrites it. A `KRAKEN_*` variable with no comment on the
Go side fails the build rather than appearing here with a blank description.
:::

<!-- generated:panel-env -->

## Reading the defaults

- An **unset** default means empty, and the description says what empty
  selects. That is not always "nothing": an empty `KRAKEN_DATABASE_URL` selects
  the in-memory store, and an empty `KRAKEN_TRUSTED_PROXIES` means no forwarding
  header is ever believed.
- A **list** is comma-separated, trimmed, with empty entries dropped.
- A **duration** takes either a Go duration (`24h`) or a plain number of
  seconds.
- **Booleans** take anything Go's `strconv.ParseBool` accepts — `true`, `1`,
  `false`, `0`. An unparseable value falls back to the default rather than
  failing.

Two variables refuse to be quietly wrong, and the difference is which way each
list fails. An unparseable entry in `KRAKEN_TRUSTED_PROXIES` is a **startup
error**: a typo that silently emptied that list would leave the Panel running
and wrong. `KRAKEN_SETUP_ALLOWED_CIDRS` keeps skip-and-warn, because that list
fails closed — a dropped entry denies access rather than granting it.

## The ones worth knowing by heart

- **`KRAKEN_SECRETS_KEY`** — base64 of 32 bytes. Seals every at-rest secret.
  Lose it and every stored secret is unrecoverable. See [installing the
  Panel](/wiki/install/panel/).
- **`KRAKEN_TRUSTED_PROXIES`** — empty unless a proxy really fronts the Panel,
  and set correctly when one does. It decides what the audit log records and
  whether the rate limiters work at all: [behind a reverse
  proxy](/wiki/configure/reverse-proxy/).
- **`KRAKEN_TUNNEL_ADDR`** — the `:9443` listener every tunnel-mode node dials.
  Binding it to loopback takes the fleet offline: [ports and
  firewall](/wiki/configure/network/).
- **`KRAKEN_RATE_LIMITS`** and **`KRAKEN_LOG_LEVEL`** — [limits and
  logging](/wiki/configure/limits-and-logging/).

## What is not here

The Agent has its own configuration, with a file, flags and environment
variables of its own: [Agent configuration](/wiki/configure/agent/).

A few settings live in the database and are edited in the UI rather than the
environment — the datastore DSN entered in the first-run wizard, the session
TTL, the WebSocket origins. Where an environment variable exists for one of
those, **the environment wins and the UI shows the field as locked**, so a host
driven by compose or systemd cannot be quietly changed out from under its unit
file.
