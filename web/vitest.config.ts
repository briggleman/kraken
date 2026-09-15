import { defineConfig, mergeConfig } from "vitest/config";
import viteConfig from "./vite.config";

// Unit tests for the store/logic layer (`src/lib/*.svelte.ts`), not the DOM.
// The Svelte plugin from vite.config is what compiles the runes in those
// modules, so the test config is that config plus a test block — the `browser`
// resolve condition picks Svelte's client build, which is the one that has a
// working signal graph.
//
// Store-level by design: jsdom is here for the browser globals the stores reach
// for (document.hidden, localStorage, timers), not for rendering components. A
// mount harness was considered for the console pane's scroll effects (#320) and
// declined — jsdom implements no layout, so clientHeight, scrollHeight and
// scrollTop are permanently 0, and those are the very values those effects
// read. A mounted Depth.svelte would take the hidden-pane branch forever, and a
// test that stubbed the geometry back in would be a test of the stubs.
//
// So every decision that can be stated without geometry is a named helper in
// the store, covered here against real values: consoleViewKey, consoleRepin,
// restoreTop, canShowInstallLog, emptyConsoleNote, fleetPollMs, fleetHealth.
// What is left uncovered is what genuinely needs a laid-out box, and it is not
// nothing — the zero-layout guard on the scroll handler, the wasHidden latch
// that turns a tab round-trip into a "restore", and the recording of the
// operator's offset as they scroll. Those want a real browser (a Playwright
// component run) rather than a jsdom mount pretending to have one; until there
// is one, they are read in review and exercised by hand.
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: "jsdom",
      include: ["src/**/*.test.ts"],
    },
    resolve: {
      conditions: ["browser"],
    },
  }),
);
