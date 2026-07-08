// Prune the Vite output directory before a build.
//
// Vite's built-in `emptyOutDir` wipes EVERY file in `outDir` — including the
// two committed markers (.gitignore, index.stub.html) that this repo relies
// on so //go:embed all:dist works on a fresh checkout without a prior web
// build. We instead run this script as `prebuild`, keeping the markers but
// removing every generated artifact so successive builds stay reproducible.

import { readdirSync, rmSync, existsSync, mkdirSync } from "node:fs";
import { join, resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const dist = resolve(here, "..", "..", "internal", "panel", "webui", "dist");
const KEEP = new Set([".gitignore", "index.stub.html"]);

if (!existsSync(dist)) {
  mkdirSync(dist, { recursive: true });
  process.exit(0);
}
for (const name of readdirSync(dist)) {
  if (KEEP.has(name)) continue;
  rmSync(join(dist, name), { recursive: true, force: true });
}
