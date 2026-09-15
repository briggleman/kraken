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
// scrollTop are permanently 0, and those are the very values the effects read.
// A mounted Depth.svelte would take the hidden-pane branch forever, and a test
// that stubbed the geometry back in would be a test of the stubs. So each
// decision is a named helper in the store instead (consoleViewKey,
// consoleRepin, canShowInstallLog, emptyConsoleNote, fleetPollMs, fleetHealth),
// covered here against real values, and the component keeps only the wiring.
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
