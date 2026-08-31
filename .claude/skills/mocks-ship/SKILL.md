---
name: mocks-ship
description: Close a mock-iteration round — tear down live mode, sync DESIGN.md + the sidecar from the mock via /impeccable document, regenerate house.css, then branch and port the changes into the Svelte app.
---

# mocks-ship — carbonize a mock round and ship it into the app

Run this after a `/mocks-revise` session, when the user is happy with the mock. It turns the
visual iteration into shipped contract: docs synced, generated CSS refreshed, **and the app
actually built to match**. **DESIGN.md and the mock never drift from the app — this skill is the
mechanism.**

**The round is not done when the plan is written.** Kraken is a live project, so a mock that has
moved ahead of `web/src/` is drift by definition — the contract says one thing and the running
panel does another. Ship the port in the same round, on a branch, as one PR. Only stop at the
plan if the user explicitly says to, or if the port needs a decision they have to make first
(in which case ask the question, don't just hand over a plan and stop).

## Steps

0. **Branch first**, before any edit — including the DESIGN.md and `house.css` writes. The mock's
   own changes are usually already uncommitted on `main` from the `/mocks-revise` round; move them
   with `git switch -c feat/<short-name>` (uncommitted work follows the switch). Never push to
   `main` — see CLAUDE.md's branching workflow.

1. **Tear down live mode** (if armed):
   - Stop the poll background task.
   - Kill the live server (pid in `.impeccable/live/server.json`; delete that file after).
   - Strip the injected block (`<!-- impeccable-live-start -->` … `<!-- impeccable-live-end -->`) from the mock.
   - Verify: `grep -c "impeccable\|data-p-" design/mockups/spog-abyssal-ops.html` must be 0 — no markers, no leftover variant scaffolding, no unbaked param attributes.
   - Stop the :8091 mock server tab if open.

2. **Sync the documentation**: run `/impeccable document` (refresh of the existing DESIGN.md — diff the mock against DESIGN.md and `.impeccable/design.json`, fold in every new pattern, verify claims against the file, regenerate the sidecar). New named rules and components must be grounded in the mock's actual CSS, not remembered values.

3. **Regenerate the app stylesheet**: `node web/scripts/extract-design.mjs`, then `npm --prefix web run check:design` must pass. Never edit `house.css` by hand.

4. **Plan the port**: diff what changed in the mock this round (git diff of `design/mockups/`
   since the last ship) against the Svelte components in `web/src/`. List each affected
   surface/component, the markup/behavior delta, and the port order. Check the backend before
   assuming work: Kraken's API is usually ahead of its UI, so the route, the client method and the
   types often already exist.

   Two things this diff must always look for, because they are where mock and app genuinely
   disagree rather than merely differ:
   - **Duplicated facts.** The app's markup has often drifted from the mock's — a value the mock
     puts on a new line may already be rendered somewhere else in the same cell. Porting naively
     shows it twice.
   - **States the mock doesn't have.** A mock is one frozen frame; the app needs in-flight,
     failure, and empty states. Follow the house idiom (`disabled={busy}` with the label swapping,
     as in `NodeCfg.svelte`) rather than inventing one, and say plainly that the state was
     inherited rather than designed. If it deserves real design, that is the next mock round.

5. **Do the port.** Implement it, then verify: `npm --prefix web run build` (or `make check` when
   Go changed too) must pass, and confirm the change in the browser against the real app — the
   `run-kraken` skill launches the stack. A port nobody rendered is not verified.

6. **Commit and open the PR**: mock + DESIGN.md + sidecar + house.css + the Svelte changes as one
   change, so the contract and the app move together. Conventional Commits title (`feat(web): …`
   for a new capability, `fix(web): …` for a correction); no `Co-Authored-By` trailer. Then
   `gh pr create --fill` and hand the user the link.
