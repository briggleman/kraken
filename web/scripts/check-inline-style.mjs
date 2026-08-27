// Guard: no literal style="…" attributes in Svelte sources.
//
// The Panel serves this bundle under `style-src 'self'` with no
// 'unsafe-inline' (SECURITY.md). Style attributes — including the ones a
// framework writes via setAttribute — are blocked, and the block is SILENT:
// the declaration never applies, so an instrument renders at its CSS default
// and only a console line says why. Use Svelte's `style:` directive for a
// single property, or `use:istyle={"…"}` (web/src/lib/istyle.ts) for a
// composed declaration string; both write through the CSSOM, which CSP does
// not govern.
//
//   node scripts/check-inline-style.mjs

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, resolve, dirname, relative } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, "..", "src");

/** @type {{file: string, line: number, text: string}[]} */
const hits = [];

function walk(dir) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      walk(p);
      continue;
    }
    if (!name.endsWith(".svelte")) continue;
    const lines = readFileSync(p, "utf8").split("\n");
    lines.forEach((line, i) => {
      if (/\sstyle="/.test(line)) {
        hits.push({ file: relative(root, p), line: i + 1, text: line.trim().slice(0, 100) });
      }
    });
  }
}

walk(root);

if (hits.length) {
  console.error(
    "check-inline-style: " +
      hits.length +
      " literal style attribute(s) found — the Panel's CSP blocks these silently.\n" +
      "Use `style:` directives or use:istyle={…} instead (see web/src/lib/istyle.ts).\n",
  );
  for (const h of hits) console.error(`  ${h.file}:${h.line}  ${h.text}`);
  process.exit(1);
}
console.log("check-inline-style: no style attributes in " + relative(process.cwd(), root));
