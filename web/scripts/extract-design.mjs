// Extract the house CSS from the committed living mock into the app.
//
// The mock (design/mockups/spog-abyssal-ops.html) is the design contract:
// its single <style> block IS the house stylesheet, and the app must not
// drift from it. This script copies that block into
// web/src/styles/house.css verbatim, plus a small mount shim, so the same
// selectors the mock exercises style the ported Svelte markup 1:1.
//
//   node scripts/extract-design.mjs           regenerate house.css
//   node scripts/extract-design.mjs --check   exit 1 if house.css is stale
//                                             (CI drift gate; see Makefile)
//
// Edit the mock (or DESIGN.md), never house.css.

import { readFileSync, writeFileSync, existsSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const mockPath = resolve(here, "..", "..", "design", "mockups", "spog-abyssal-ops.html");
const outPath = resolve(here, "..", "src", "styles", "house.css");

const mock = readFileSync(mockPath, "utf8");

const open = mock.indexOf("<style>");
const close = mock.indexOf("</style>");
if (open === -1 || close === -1 || close < open) {
  console.error("extract-design: no <style> block found in " + mockPath);
  process.exit(2);
}
if (mock.indexOf("<style>", open + 1) !== -1) {
  console.error("extract-design: expected exactly one <style> block in the mock");
  process.exit(2);
}

let css = mock.slice(open + "<style>".length, close).replace(/^\n/, "");

// The one transform we apply: the mock references sibling assets relatively
// (it must render standalone from design/mockups/); the app serves the same
// files from web/public at the site root.
css = css.replaceAll("url(kraken-glyph-teal.png)", "url(/kraken-glyph-teal.png)");

// No hotlinks, ever — the app must be self-contained.
if (/url\(\s*['"]?https?:/.test(css)) {
  console.error("extract-design: the mock's CSS references an external URL; localize it first");
  process.exit(2);
}

const header = `/* GENERATED FILE — do not edit.
 *
 * Extracted verbatim from design/mockups/spog-abyssal-ops.html (the living
 * mock, the design contract) by web/scripts/extract-design.mjs. To change
 * the design: edit the mock (see /mocks-revise), update DESIGN.md (see
 * /mocks-ship), then regenerate:
 *
 *   node web/scripts/extract-design.mjs
 */

/* The app mounts into #app inside <body>; the mock styles <body> directly.
 * display:contents makes the mount wrapper invisible to the grid so every
 * body-level rule applies to the ported markup unchanged. */
#app { display: contents; }

`;

const next = header + css;

if (process.argv.includes("--check")) {
  const current = existsSync(outPath) ? readFileSync(outPath, "utf8") : "";
  if (current !== next) {
    console.error(
      "extract-design: web/src/styles/house.css is stale — the mock's <style> block " +
        "changed. Run: node web/scripts/extract-design.mjs",
    );
    process.exit(1);
  }
  console.log("extract-design: house.css matches the mock");
  process.exit(0);
}

writeFileSync(outPath, next, "utf8");
console.log(
  "extract-design: wrote " + outPath + " (" + next.split("\n").length + " lines)",
);
