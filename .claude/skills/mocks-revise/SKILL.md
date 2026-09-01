---
name: mocks-revise
description: Start the living-mock server and impeccable live mode so the user can visually iterate on the design contract (design/mockups/spog-abyssal-ops.html) in the browser.
---

# mocks-revise — open the living mock for visual iteration

The living mock at `design/mockups/spog-abyssal-ops.html` is the design contract: DESIGN.md, `.impeccable/design.json`, and the generated `web/src/styles/house.css` all derive from it. This skill arms **impeccable live mode** on it so the user can select elements in the browser, request variants, steer, and accept/discard.

Requires the `impeccable` skill (user-level, scripts in `~/.claude/skills/impeccable/scripts/`). If it is missing, stop and tell the user.

## Steps

1. **Serve the mock over HTTP** — live mode does not work from `file://`. Use the Browser pane: `preview_start` with the `mockups` launch config (npx http-server on **:8091**, serving `design/mockups/`).
2. **Arm live mode**: from the repo root run `node ~/.claude/skills/impeccable/scripts/live.mjs --target design/mockups/spog-abyssal-ops.html` (adjust the path to the impeccable skill's reported base directory). It starts the live server, injects the script tag into the mock, and prints server port + token.
3. **Open the page** in the Browser pane at `http://localhost:8091/spog-abyssal-ops.html` and confirm the live badge connects.
4. **Enter the poll loop**: run `node .../live-poll.mjs` as a harness-tracked background task (never `&` inside Bash). On timeout, restart it immediately. Service `generate` / `steer` / `accept` / `discard` events per the impeccable live reference.

## Traps (learned the hard way)

- **Accept merges markup only.** `live-accept` splices the chosen variant's markup verbatim and deletes the scoped `<style>`. A CSS-only variant lands NOWHERE, and a markup variant carries preview-suffixed ids (`nsGamePort1`) and variant-only hook classes into source. After EVERY accept: grep the mock for a signature token of the accepted design, restore real ids, strip hook classes, hand-translate `:scope` rules into house selectors, bake the accepted `paramValues` branch. Trust the file, not the ack.
- One edit per generation: splice CSS + all 3 variants into the scaffold's wrapperBlock in a single write, or the browser strands at 0/N.
- **Measurements taken inside live mode are not evidence.** With variant wrappers, `@scope`
  rules, and injected styles racing variant swaps, `getComputedStyle` and
  `getBoundingClientRect` can read stale or detached elements — a bare
  `width: 25% !important` "not applying" and a child measuring 0×0 are what a poisoned
  harness looks like, not what broken CSS looks like. This faked an "impossible"
  containing-block bug once (#177): two implementations were pulled as unverifiable, and a
  clean-room test later measured the identical CSS working perfectly. Before concluding any
  CSS *can't* work, reproduce it with the mock served plainly (`npx http-server
  design/mockups -p 8091 -c-1`, **no live mode**), a hand-written element, and a fresh
  `document.querySelector` per read — never a cached node reference across DOM changes.
- Real credentials never enter the mock — sample placeholders only.

## When the round is done

Tell the user to run `/mocks-ship` — it tears live mode down, re-syncs DESIGN.md and the sidecar, regenerates `house.css`, and plans the port of the changes into the Svelte app.
