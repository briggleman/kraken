---
name: mocks-ship
description: Close a mock-iteration round — tear down live mode, sync DESIGN.md + the sidecar from the mock via /impeccable document, regenerate house.css, and produce the implementation plan for porting the changes into the Svelte app.
---

# mocks-ship — carbonize a mock round and plan the port

Run this after a `/mocks-revise` session, when the user is happy with the mock. It turns the visual iteration into shipped contract: docs synced, generated CSS refreshed, and a concrete plan for the code changes. **DESIGN.md and the mock never drift from the app — this skill is the mechanism.**

## Steps

1. **Tear down live mode** (if armed):
   - Stop the poll background task.
   - Kill the live server (pid in `.impeccable/live/server.json`; delete that file after).
   - Strip the injected block (`<!-- impeccable-live-start -->` … `<!-- impeccable-live-end -->`) from the mock.
   - Verify: `grep -c "impeccable\|data-p-" design/mockups/spog-abyssal-ops.html` must be 0 — no markers, no leftover variant scaffolding, no unbaked param attributes.
   - Stop the :8091 mock server tab if open.

2. **Sync the documentation**: run `/impeccable document` (refresh of the existing DESIGN.md — diff the mock against DESIGN.md and `.impeccable/design.json`, fold in every new pattern, verify claims against the file, regenerate the sidecar). New named rules and components must be grounded in the mock's actual CSS, not remembered values.

3. **Regenerate the app stylesheet**: `node web/scripts/extract-design.mjs`, then `npm --prefix web run check:design` must pass. Never edit `house.css` by hand.

4. **Plan the port**: diff what changed in the mock this round (git diff of `design/mockups/` since the last ship) against the Svelte components in `web/src/`. Write an implementation plan listing each affected surface/component, the markup/behavior delta, and the port order. Present it to the user before touching app code.

5. **Commit** the round as one change: mock + DESIGN.md + sidecar + house.css together, so the contract moves atomically. (Ask before committing if the session hasn't already established commit consent.)
