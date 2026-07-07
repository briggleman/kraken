# Branch protection & merge policy

`main` is protected. This file documents the intended configuration so it
can be re-created after a repo migration, audited after a settings change,
or verified without opening the GitHub UI.

## Current mechanism

Today `main` is protected by a **classic branch protection rule** at
Settings → Branches. Classic rules on GitHub Free personal repos do
_not_ expose the "Restrict updates (actor list)" feature — that lives in
**Rulesets** (Settings → Rules → Rulesets) instead. Since the repo has no
other collaborators, the classic rule's "Require PR + no approvals + 4
required checks" is functionally equivalent to Rulesets-with-actor-list
for now. If we ever add a collaborator, migrate to a Ruleset with the
actor list restricted to `briggleman`.

## Intended rules on `main`

- **Direct pushes blocked.** All changes land via PR — mirrors the
  branch-workflow rule documented in [`CLAUDE.md`](../CLAUDE.md).
- **Required status checks (must pass before merge):**
  - `pr title (conventional commits)` — enforces the Conventional Commits
    format release-please reads. From [`.github/workflows/ci.yml`](workflows/ci.yml).
  - `go (build · vet · staticcheck · test)` — same workflow.
  - `web (typecheck · build)` — same workflow.
  - `CodeQL` — from GitHub's default code-scanning setup.
- **Force pushes blocked.**
- **Branch deletion blocked.**
- **Required PR reviews: 0.** Kraken is currently a solo-maintainer project.
  With reviews required, self-review is impossible and every PR gets stuck
  behind `gh pr merge --admin` — which defeats the point of enforcing the
  CI gate at all.
- **Restrict updates: only `briggleman`.** Only the maintainer can push /
  merge to `main`. External contributors may still open PRs from forks —
  they just can't merge them. This makes the "solo maintainer" posture
  explicit at the ruleset level rather than relying on manual review.
- **Allow GitHub Actions to create and approve pull requests.** This is a
  separate repo-level toggle (Settings → Actions → General → Workflow
  permissions), not part of the ruleset itself. Required by
  `release-please.yml`, which opens the recurring
  `chore(main): release X.Y.Z` PR. Without it the workflow errors with:

  > release-please failed: GitHub Actions is not permitted to create or
  > approve pull requests.

If the project takes on additional maintainers later, bump required
approvals to 1 (or higher), add them to the Restrict-updates actor list,
and remove the solo-maintainer notes above.

## Configure via the GitHub UI

**Settings → Rules → Rulesets → New branch ruleset** (or edit an existing one
targeting `main`):

- Target: `main`
- Enforcement status: Active
- Bypass list: `briggleman` (repo owner, Always) — lets the maintainer
  use `gh pr merge --admin` for emergency unblocks without disabling the
  ruleset repo-wide.
- Rules:
  - ☑ Restrict deletions
  - ☑ Restrict updates — actor list: `briggleman` only
  - ☑ Require a pull request before merging
    - Required approvals: **0**
    - ☑ Require conversation resolution before merging (optional but nice)
  - ☑ Require status checks to pass
    - Add all four checks listed above.
  - ☑ Block force pushes

Then, separately, in **Settings → Actions → General → Workflow permissions**:

- ☑ **Allow GitHub Actions to create and approve pull requests** (fixes the
  release-please error above)

## Verify via `gh` API

```sh
# List rulesets targeting branches:
gh api /repos/briggleman/kraken/rulesets --jq '.[] | select(.target=="branch") | {id, name, enforcement}'

# Inspect the pull_request + required_status_checks rules on a specific ruleset:
gh api /repos/briggleman/kraken/rulesets/<id> --jq '.rules[] | {type, parameters}'
```

The `pull_request` rule's `parameters.required_approving_review_count` should
be `0`. The `required_status_checks` rule's parameters should list the four
check contexts above.

## The merge-blocked trap

If `gh pr merge --squash` returns:

> Pull request is not mergeable: the base branch policy prohibits the merge.

while all CI checks are green, "Required approvals" has drifted above 0.
Reset it via the UI — do **not** permanently bypass with `--admin`; that
hides ruleset regressions from us.

For a one-off unblock while investigating, `gh pr merge <n> --squash --admin`
works because the repo owner has admin privileges. Use it sparingly.
