# Design — external game-spec catalog

**Status:** proposal · **Owner:** briggleman · **Last updated:** 2026-07-08

## Motivation

The bundled game-spec catalog lives at `internal/panel/catalog/bundled/*.yaml`
and is `go:embed`ed into the Panel binary. Every spec correction — a Palworld
version bump, a Valheim BepInEx path change, a new startup argument — requires
a Kraken release. That's expensive for what should be a data-only change and
makes the catalog effectively read-only for anyone who isn't cutting Panel
releases.

Move the catalog to a **separate, signed, versioned repo** so:

- Spec fixes ship in minutes, not release cycles
- Operators can pull updates on their own schedule
- The catalog can eventually accept community contributions without granting
  commit access to `briggleman/kraken`

## Goals

- Panel fetches the catalog from a public HTTPS source (default:
  `raw.githubusercontent.com/briggleman/kraken-catalog/main/…`).
- Fetched content is cryptographically verified against a trust root
  embedded in the Panel binary — a compromise of the catalog repo cannot
  inject a malicious startup command.
- Operators see a per-spec "update available" badge and can apply updates
  with one click, or roll back to the previous version.
- The existing `//go:embed`ed bundled catalog remains the fallback for
  fresh installs, air-gapped hosts, and any transient upstream outage.

## Non-goals

- Signed release attestations via Sigstore / Rekor. See [Trust model](#trust-model)
  for why we chose a simpler primitive.
- Multi-source / mirror support. Single canonical origin for v1.
- Automatic apply. Updates require operator action — no unattended rewrites
  of running-server configuration.

## Architecture

```
briggleman/kraken-catalog                      briggleman/kraken
├── manifest.json     ← signed index          ├── internal/panel/catalog/
├── manifest.sig      ← Ed25519 signature     │   ├── bundled/            (offline fallback)
└── specs/                                    │   ├── external/           (fetcher, new)
    ├── palworld.yaml                         │   │   ├── fetch.go
    ├── valheim.yaml                          │   │   ├── verify.go
    ├── vrising.yaml                          │   │   ├── store.go
    └── …                                     │   │   └── pubkey.pem      ← trust root
                                              │   └── catalog.go          (existing)
                                              └── internal/panel/store/
                                                  └── migrate/sql/
                                                      └── NNNN_catalog_versions.sql
```

### Manifest schema (`manifest.json`)

```json
{
  "version": 1,
  "generated_at": "2026-07-08T18:00:00Z",
  "specs": [
    {
      "slug":         "palworld",
      "path":         "specs/palworld.yaml",
      "spec_version": "1.4.2",
      "sha256":       "8f2a…"
    }
  ]
}
```

`manifest.sig` is a base64-encoded Ed25519 signature over the raw bytes of
`manifest.json`. Signing key is held by the maintainer; verification key is
committed to the Kraken repo at `internal/panel/catalog/external/pubkey.pem`
and `go:embed`ed into the Panel binary.

### Fetch flow

1. Background goroutine + on-demand endpoint (`POST /api/v1/catalog/refresh`):
   1. GET `${CATALOG_URL}/manifest.json` and `${CATALOG_URL}/manifest.sig`
   2. Verify Ed25519 signature → reject on failure, keep serving cached data
   3. For each spec entry not already at that (slug, sha256):
      - GET `${CATALOG_URL}/${path}`
      - Verify sha256 matches manifest entry → reject blob on mismatch
      - Upsert into `catalog_versions` (see data model)
   4. Log a summary: `catalog refresh: 12 up-to-date, 2 new versions`
2. UI reads the diff between `catalog_versions` (fetched) and `specs`
   (imported) → renders per-spec "update available" badges.
3. Operator clicks Update → Panel copies the new blob from
   `catalog_versions` into `specs`, keeping the previous `specs` row
   under a rollback pointer.

### Data model

New table `catalog_versions`:

| column | type | notes |
| --- | --- | --- |
| `slug` | text | primary component |
| `sha256` | text | primary component; multiple rows per slug over time |
| `data` | jsonb | parsed spec |
| `manifest_version` | int | which manifest generation surfaced this |
| `fetched_at` | timestamptz | monotonic within a slug |

Composite PK `(slug, sha256)`. Old rows are pruned when > N versions per
slug exist (N=10). The `specs` table gains a `catalog_sha256` column that
points at the `catalog_versions` row it was imported from (nullable — bundled
imports stay `null`).

## Trust model

**Ed25519 signature over `manifest.json`, verified against a public key
embedded in the Panel binary.**

Chosen over the alternatives because:

- **Zero runtime dependency.** No Sigstore infra, no transparency log
  fetch, no OIDC dance. Works on an air-gapped host that has periodic
  connectivity.
- **Simple threat model.** The Panel trusts anyone who holds the private
  key. That's the maintainer (via a GitHub Actions secret). No trust
  transfer between projects or identities.
- **Rotation is a Kraken release.** New pubkey → new Panel binary. This
  is the same trust root as Kraken itself, so it doesn't expand the
  attack surface — a compromised Panel release could ship anything
  anyway.
- **Fits repo velocity.** Signing happens in CI on merge to
  `kraken-catalog:main`. Manual signing works too for one-off updates.

### Rejected: Sigstore / cosign
Great for containers and general artifacts, but adds real deps and requires
network reach to the Sigstore instance during verification. Overkill for a
maintainer-signed data feed.

### Rejected: sha256 pins in bundled
Every catalog update would still require a Kraken release. Defeats the whole
point of moving to an external repo.

### Threats considered

| Threat | Mitigation |
| --- | --- |
| Compromised `kraken-catalog` repo → malicious startup command | Signature verification against key never present in the catalog repo |
| Compromised signing key | Rotate via Kraken release; short-term impact bounded by the fact that operators must click "Update" |
| MITM on the fetch | HTTPS to GitHub, plus signature verification (belt + suspenders) |
| Downgrade attack (serve an older signed manifest) | `manifest.generated_at` compared to last-known; refuse if `< last_seen - 24h` |
| Signed but subtly-wrong spec | Not fully mitigatable at this layer. UI shows a diff on update; operator reviews. |

## Deploy considerations

### New env vars

| Var | Default | Purpose |
| --- | --- | --- |
| `KRAKEN_CATALOG_URL` | `https://raw.githubusercontent.com/briggleman/kraken-catalog/main` | Base URL for `manifest.json` + `specs/*.yaml` |
| `KRAKEN_CATALOG_ENABLED` | `true` | Set `false` to disable all outbound catalog fetches. Bundled fallback only. |
| `KRAKEN_CATALOG_INTERVAL` | `12h` | Background refresh cadence |

**Default is on.** Rationale: a home-server project benefits far more from
fresh specs than from strict air-gap, the fetch is signature-verified, and
operators who want air-gap set one env var. The behavior is documented in
README's Firewall/Deploy sections.

### First-run behavior

Fresh Postgres, network available:
1. Bootstrap seeds the `specs` table from `bundled/*.yaml` (existing behavior)
2. First catalog refresh populates `catalog_versions`
3. If any bundled spec is behind the catalog version, UI immediately shows
   an "Update available" badge — operator applies at their leisure.

Fresh Postgres, air-gapped:
1. Bootstrap seeds from `bundled/*.yaml` (existing behavior)
2. Catalog refresh returns a network error → logged, no state change
3. Operator sees the bundled catalog, no update badges. Zero regression.

## Migration path

The bundled catalog stays as-is. The external catalog is additive:
`catalog_versions` starts empty and fills on first refresh. Existing
installs pick up the new plumbing on the release that ships PR B (below);
no data migration required beyond the additive schema migration.

## Rollout phases

Three self-contained PRs, mergeable independently:

### PR A — Catalog fetcher (client + storage)

- `internal/panel/catalog/external` package: fetch, verify, cache
- Embedded Ed25519 pubkey at `pubkey.pem` (placeholder until PR C generates the real key)
- New `catalog_versions` table + goose migration
- Background fetcher goroutine wired at Panel startup
- Unit tests via `httptest.Server` fake catalog signed with a throwaway key
- No API endpoints yet, no UI changes — fetcher fills the cache invisibly

**Verification:** logs show refresh cycles, DB rows appear.

### PR B — API + UI surface

- `GET /api/v1/catalog/updates` — returns per-spec `{slug, current_sha, latest_sha, has_update}`
- `POST /api/v1/catalog/refresh` — triggers a fetch cycle synchronously
- `POST /api/v1/specs/{id}/update-from-catalog` — copies new blob into `specs`, records `previous_sha` for rollback
- `POST /api/v1/specs/{id}/rollback` — swaps back to `previous_sha`
- Web UI: "N updates available" badge on the Specs page, per-spec "Update" button with a diff modal

### PR C — Bootstrap the catalog repo

- Generate Ed25519 keypair; commit public key to `internal/panel/catalog/external/pubkey.pem`; add private key as a GitHub Actions secret in `kraken-catalog`
- Create `briggleman/kraken-catalog`, port the 9 bundled specs there, write a signing workflow that runs on push to `main`
- Add a `catalog:` scope to `release-please` for versioning the catalog repo independently
- Documentation in `README.md` linking to the catalog repo and the contribution flow

## Open questions

- **Community contributions:** once PR C lands, do we accept spec PRs from the community? If yes, we need a `CODEOWNERS` and a minimal review checklist for the catalog repo. Deferred out of scope.
- **Multi-arch spec entries:** the current spec schema encodes platform variants (`linux-native` / `windows-native`) inline. That stays. If we ever want per-arch binary references, we'd extend the schema — no impact on this design.
- **Update UX for running servers:** if operator updates the Palworld spec while a Palworld server is running, does the running server pick up the new config on next start? Currently yes (spec is re-rendered on power start). We should surface this in the update modal so operators aren't surprised.

## Backlog impact

Closes the BACKLOG.md item "Pull game specs from an external GitHub repo."
Superseded by the catalog repo once PR C lands.
