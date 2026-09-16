---
title: Releases
description: Every Kraken release and what landed in it, rendered from CHANGELOG.md — plus how a release is cut, what the version number promises while the project is pre-1.0, and the one ordering rule that matters to an operator.
section: reference
order: 62
---

Releases are cut by [release-please](https://github.com/googleapis/release-please)
from the commit subjects on `main`. Every pull request is squash-merged, so its
title becomes one commit, and that commit's Conventional-Commits type decides
both the version bump and the section it lands in below. Nothing on this page is
written by hand.

Each release publishes the binaries with their `SHA256SUMS` on the GitHub
release, and container images to `ghcr.io/briggleman/kraken-panel` and
`ghcr.io/briggleman/kraken-agent`.

## What a version number promises

Kraken is pre-1.0, so the middle number carries the weight:

- **`feat:`** — a new user-facing capability. Minor bump.
- **`fix:`** — a user-facing bug fix. Patch bump.
- **`feat!:`**, or a `BREAKING CHANGE:` footer — a breaking change. While the
  major is still 0 that is a minor bump, which is SemVer working as designed
  rather than the change being small.
- **`docs:`, `chore:`, `ci:`, `refactor:`, `test:`, `build:`, `perf:`,
  `style:`, `revert:`** — no bump, still in the notes.

Pre-1.0 means the surface can move between minors. Read the entry before you
upgrade a fleet you care about, and read
[Upgrading](/wiki/install/upgrade/) before your first one.

:::warning
**Agents before Panels.** The Panel↔Agent protocol is versioned by tolerance,
not negotiation: a new field arrives in a shape where an older Agent's zero
value means "unknown" and the Panel falls back to its previous behaviour. Get
the order wrong and nothing breaks — you simply do not get the new behaviour
until both halves are current. Announced download lengths are the worked
example, and [Upgrading](/wiki/install/upgrade/) has the rest.
:::

## Every release

Each version below is a link target of its own — `#v0-52-0` for 0.52.0 — so a
release can be cited directly.

<!-- generated:changelog -->
