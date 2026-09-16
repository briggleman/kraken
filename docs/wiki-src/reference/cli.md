---
title: krakenctl
description: The admin CLI — two subcommands, what each writes and where, and the one thing to get right about the hosts you give an enrolling Agent.
section: reference
order: 61
---

`krakenctl` is deliberately small. It does the mutual-TLS material for the
Panel↔Agent channel and nothing else: two subcommands, plus `version`.

It is built alongside the Panel and Agent, so `make build` puts it in `bin/`,
and the installers place it on a node beside the Agent.

## `krakenctl enroll`

The normal path. Generates a key and a CSR on this host, exchanges a one-time
bootstrap token for a signed Agent certificate, and writes the bundle.

```sh
krakenctl enroll -panel URL -token TOKEN [-hosts h1,h2,…] [-port N] [-out DIR]
```

| flag | what it is |
| --- | --- |
| `-panel` | the Panel base URL, for example `http://panel:8080` |
| `-token` | the one-time bootstrap token from the Add node dialog |
| `-hosts` | IPs and DNS names the Panel can dial this Agent at. They go into the certificate SAN and prefill the node registration. |
| `-port` | this Agent's gRPC port, default `9090`, so the registration is prefilled with the right one |
| `-out` | output directory, default `./certs` |

It writes three files: `agent-key.pem` at mode `0600`, and `agent.pem` and
`ca.pem` at `0644`. A complete bundle under `<root>/certs` is adopted by the
Agent without configuring any TLS paths, which is why `-out <root>/certs` is the
usual invocation.

Re-run it with a fresh token to rotate the certificate.

:::warning
**Give `-hosts` real addresses.** A bare computer name does not resolve from a
remote Panel, and the failure arrives later as a node that enrolled fine and
then reads offline. This only matters in direct mode, where the Panel dials in.
A tunnel node dials out and needs no reachable address at all.
:::

## `krakenctl gen-certs`

The manual path, for a setup that is not using enrollment: it generates a CA, a
Panel client certificate and an Agent server certificate as PEM files.

```sh
krakenctl gen-certs [-out DIR] [-agent-hosts h1,h2,…]
```

`-out` defaults to `./certs`. `-agent-hosts` adds extra DNS names and IPs to the
Agent certificate's SAN; `localhost` and `127.0.0.1` are always included.

Enrollment is the better path for anything beyond a single host. This one exists
because the trust model should be inspectable without a running Panel, and
because a development stack needs certificates before it needs an Agent.

## `krakenctl version`

Prints the build. `-version` and `--version` do the same.

## What it does not do

There is no `krakenctl` for servers, specs, backups or users. Those are the
REST API, and the Panel's own UI is a client of it like any other. If you want
to script something, the [API reference](/wiki/reference/api/) is the surface,
and a session token from `POST /auth/login` is the whole authentication story.
