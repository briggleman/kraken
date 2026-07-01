import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

// The UI talks to the Panel REST API under /api. In dev, Vite proxies it to the
// Panel (default :8080). In production the Panel serves the built assets and
// /api is same-origin.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "src"),
      "@ds": path.resolve(__dirname, "design-system"),
    },
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
