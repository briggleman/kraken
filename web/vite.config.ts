import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

// The UI talks to the Panel REST API under /api. In dev, Vite proxies it to
// the Panel (default :8080). In production the Panel binary embeds this
// build (see internal/panel/webui) and serves it same-origin, so /api works
// without a proxy.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "src"),
      "@ds": path.resolve(__dirname, "design-system"),
    },
  },
  // Build straight into the Go embed target so `go build ./cmd/panel` picks
  // up the freshly compiled assets on the next build. The path is repo-root
  // relative (via __dirname/..).
  //
  // emptyOutDir is intentionally false: `scripts/clean-dist.mjs` runs as the
  // npm `prebuild` step and clears the directory while keeping the two
  // committed markers (.gitignore, index.stub.html) that make //go:embed
  // compile on a fresh checkout with no prior web build.
  build: {
    outDir: path.resolve(__dirname, "../internal/panel/webui/dist"),
    emptyOutDir: false,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: process.env.VITE_API_URL || "http://localhost:8080",
        changeOrigin: true,
        ws: true,
      },
    },
  },
});
