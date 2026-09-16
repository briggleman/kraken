---
title: API reference
description: Every REST endpoint the Panel serves, generated from internal/panel/api/openapi.yaml — methods coloured by what the call can do to you, statuses by whose fault they are.
section: reference
order: 60
---

Everything the web UI does, it does through this surface. There is no private
back channel: the bundle the Panel embeds is a client of the same routes below,
which is why the reference is worth keeping honest — it is generated from
[`internal/panel/api/openapi.yaml`](https://github.com/briggleman/kraken/blob/main/internal/panel/api/openapi.yaml),
the contract the Panel ships, rather than written beside it.

Every path below is relative to `/api/v1` on the Panel's HTTP listener, which
defaults to port `8080`. The Agent's gRPC surface is a different thing entirely
and is not documented here; the browser never reaches it.

## How a caller authenticates

`POST /auth/login` returns a token, and every other route wants it as
`Authorization: Bearer <token>`. The token is opaque — 32 bytes from
`crypto/rand` — and the Panel stores only its SHA-256 digest, so the value you
hold is the only copy there is.

Sessions expire. The lifetime is `KRAKEN_SESSION_TTL`, 24 hours by default, and
`POST /auth/logout` ends one early. A revoked or expired session stops working
everywhere at once, including on a download already granted a token (below).

Three marks on the rows below are worth reading before you skim them:

- **no auth** on a route means it takes no session at all. There are two:
  login, and Agent enrollment, which authenticates with a one-time bootstrap
  token instead.
- The whole `/setup/*` group answers only callers whose source address falls
  inside `KRAKEN_SETUP_ALLOWED_CIDRS` — loopback and the private ranges by
  default. Everything else gets a 403 whatever credentials it carries.
- Permissions are checked per route on top of the session. The four built-in
  roles and what they hold are on the [security page](/wiki/security/); a denial
  on a server you may not see is a **404**, not a 403, so that another user's
  server is not revealed to exist by the refusal.

:::note
The method colours are not decorative and they are not one hue per verb. A read
is left unlit because nothing happens; every method that writes takes the light;
`DELETE` alone takes crisis magenta, because it is the only one you cannot take
back. Statuses follow the same discipline from the other side: 2xx gold, 4xx
violet — the caller got it wrong — and 5xx magenta, meaning the Panel did.
:::

## Downloading a file

The one flow on this page that does not look like the others. A file download
cannot carry an `Authorization` header and still be a browser navigation, so it
carries a token instead, and the token is deliberately not a session credential:

1. `POST /servers/{id}/files/download-token` with the path set you want. It
   needs `server.files.read`, the same permission the raw route carries.
2. The Panel mints a grant — **single-use, 60 seconds, pinned to one server, one
   route kind and one exact canonicalised path set, and bound to the user and
   the session that asked** — and hands back the token.
3. `GET /servers/{id}/files/raw?token=…` or
   `GET /servers/{id}/files/download?token=…` redeems it. The browser streams to
   disk with its own progress, with `Content-Length` announced whenever the Agent
   knew the size.

With `token` present the `Authorization` header is not consulted at all, so a
live session cannot rescue an invalid token; with it absent, both routes
authenticate exactly as they always have. Redemption is rate limited at 30 a
minute with a burst of 10, per client — the limit sits on the token branch only,
so an ordinary session-authenticated download is never refused for sharing an
address with somebody probing tokens.

The full argument for why a token in a URL is acceptable here, and the short
list of what it deliberately does not protect against, is in
[SECURITY.md](https://github.com/briggleman/kraken/blob/main/SECURITY.md) and
summarised on [Files and SFTP](/wiki/operate/files/).

## Errors

A refusal is JSON — `{"error": "…"}` — under the status code. Two shapes are
worth knowing in advance:

- A malformed id is a **404**, not a 500. Ids are Postgres `uuid` columns, and a
  value that cannot be one is indistinguishable from a row that is not there.
- A 429 carries `Retry-After`, and a refused request does not consume future
  capacity.

<!-- generated:api -->
