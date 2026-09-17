---
title: Releases
description: How a release is cut, what the version number promises while the project is pre-1.0, where the release notes live, and the one ordering rule that matters to an operator.
section: reference
order: 62
---

Releases are cut by [release-please](https://github.com/googleapis/release-please)
from the commit subjects on `main`. Every pull request is squash-merged, so its
title becomes one commit, and that commit's Conventional-Commits type decides
both the version bump and the section it lands in under the release notes.

Each release publishes the binaries with their `SHA256SUMS` on the [GitHub
release](https://github.com/briggleman/kraken/releases), and container images
to `ghcr.io/briggleman/kraken-panel` and `ghcr.io/briggleman/kraken-agent`.
The notes for every version are on that releases page and in the repository's
[CHANGELOG.md](https://github.com/briggleman/kraken/blob/main/CHANGELOG.md);
this wiki does not repeat them.

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

:::note
**Panel first, then push the Agents from it.** The Panel carries the Agent
builds of its own release and is the only thing that can install them, so the
Nodes page offers an update per node once the Panel is current. The protocol is
versioned by tolerance, not negotiation: a new field arrives in a shape where an
older Agent's zero value means "unknown" and the new Panel falls back to its
previous behaviour, so a half-upgraded fleet keeps working and each node gains
the new behaviour when you push it. Announced download lengths are the worked
example, and [Upgrading](/wiki/install/upgrade/) has the rest.
:::
