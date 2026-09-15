import { defineConfig, mergeConfig } from "vitest/config";
import viteConfig from "./vite.config";

// Unit tests for the store/logic layer (`src/lib/*.svelte.ts`), not the DOM.
// The Svelte plugin from vite.config is what compiles the runes in those
// modules, so the test config is that config plus a test block — the `browser`
// resolve condition picks Svelte's client build, which is the one that has a
// working signal graph.
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
