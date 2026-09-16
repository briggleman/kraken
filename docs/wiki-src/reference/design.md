---
title: Design documents
description: The documents behind the decisions — the product brief, the design language, the reverse-connections note, and the two files the wiki treats as canonical rather than restating.
section: reference
order: 63
---

Kraken keeps its reasoning in the repository rather than in a wiki page, and the
wiki quotes those files instead of paraphrasing them. This page is the index,
with a paragraph on each so you can tell which one you want.

## The decisions

**[PRODUCT.md](https://github.com/briggleman/kraken/blob/main/PRODUCT.md)** is
the product brief: who this is for and what it refuses to become. The user is a
technical homelab self-hoster who is competent with Docker, a terminal and their
own router, and is not a paid operator. Two usage scenes are first-class and
shape everything: the Panel parked on a second monitor while the operator plays,
where density beats guidance, and a phone mid-session when something broke,
where power actions and the live console have to genuinely work one-handed. Read
it before proposing a feature; it is mostly a document about scope.

**[DESIGN.md](https://github.com/briggleman/kraken/blob/main/DESIGN.md)** is the
design language, and it is the single source of truth for it. Colour, type,
layout, depth and a long list of Named Rules that are the actual contract:
sodium gold is the only light source and always means alive; violet means
something is prevented; magenta means you cannot take it back; no pure greys
anywhere. Two of its rules are visible on this wiki, on the [API
reference](/wiki/reference/api/): the Verb-Risk Rule colours an HTTP method by
what the call can do to you, and the Whose-Fault Rule colours a status by whose
fault it is. `web/src/styles/house.css` is **generated** from the living mock at
`design/mockups/spog-abyssal-ops.html`, so the mock is what you edit.

**[docs/design/reverse-connections.md](https://github.com/briggleman/kraken/blob/main/docs/design/reverse-connections.md)**
is the note behind tunnel mode: the problem was that every node had to accept
two inbound TCP connections, which is fine on a LAN you own and impossible
behind somebody else's NAT. Phase 1 turned the transport around, so the Agent
dials the Panel and serves over one outbound mTLS connection with per-node cert
identity. Phase 2 added the Panel-side SFTP proxy, one port per tunnel node,
forwarding the raw SSH byte stream without ever terminating SSH. Game traffic is
explicitly out of scope: players always connect straight to node ports, tunnel
or not.

## The canonical files

Two files the wiki quotes rather than replaces. Where a page here and one of
these disagree, these win.

**[SECURITY.md](https://github.com/briggleman/kraken/blob/main/SECURITY.md)** is
an audit history, not a posture statement. Each pass is dated, each finding is
tied to a file and a regression test, each accepted risk carries its reasoning
and its re-evaluation trigger. The [security page](/wiki/security/) summarises
it and quotes it verbatim for every property, because a security claim
paraphrased is a security claim weakened.

**[internal/panel/catalog/bundled/SPECS.md](https://github.com/briggleman/kraken/blob/main/internal/panel/catalog/bundled/SPECS.md)**
is the Game Spec authoring convention: where image assets come from, how to pick
between a game appid and a dedicated-server appid, the idempotency obligation
that came with update-on-start, the dual-platform rules, the Windows SteamCMD
guard, and the two optional blocks worth the most thought, `backup:` and
`query:`. [Writing a spec](/wiki/specs/writing/) is the orientation; that file
is the reference.

## Generated, not written

Three pages in this wiki are produced from the source rather than maintained
beside it, so they cannot drift:

- [Panel configuration](/wiki/configure/panel/), from the `KRAKEN_*` reads and
  their comments in `internal/panel/config/config.go`. A variable with no
  comment fails the build rather than appearing as a blank cell.
- [API reference](/wiki/reference/api/), from
  `internal/panel/api/openapi.yaml`.
- [Releases](/wiki/reference/releases/), from `CHANGELOG.md`, which
  release-please writes from the commits on `main`.

The generator is
[`web/scripts/build-wiki.mjs`](https://github.com/briggleman/kraken/blob/main/web/scripts/build-wiki.mjs),
its output is committed under `docs/wiki/`, and CI fails when the two disagree.
