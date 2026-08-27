import { vitePreprocess } from "@sveltejs/vite-plugin-svelte";

export default {
  // TypeScript inside <script lang="ts"> blocks.
  preprocess: vitePreprocess(),
};
