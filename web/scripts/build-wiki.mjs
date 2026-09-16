// Build the Kraken wiki (docs/wiki-src/*.md -> docs/wiki/**/index.html).
//
// Same shape as extract-design.mjs: one script, no framework, deterministic
// output that is committed to the repo so GitHub Pages serves it directly and
// CI can fail on staleness.
//
//   node web/scripts/build-wiki.mjs           regenerate docs/wiki/
//   node web/scripts/build-wiki.mjs --check   exit 1 if the output is stale
//
// What it does:
//
//   1. reads every docs/wiki-src/**/*.md, front-matter first
//      (title, description, section, order)
//   2. renders the body through `marked` into ONE template written in the
//      Abyssal design language (see docs/wiki/assets/wiki.css)
//   3. derives the sidebar nav, the breadcrumb, the on-page TOC, and the
//      prev/next pager from the source tree + front-matter
//   4. injects the GENERATED Panel env-var reference, parsed out of
//      internal/panel/config/config.go, wherever a page carries the
//      <!-- generated:panel-env --> marker
//   5. writes docs/wiki/sitemap.xml
//
// Determinism is load-bearing: no timestamps, no build ids, stable ordering,
// LF line endings. `make check` runs this and then `git diff --exit-code
// docs/wiki`, so anything that varies between two runs breaks CI for everyone.
//
// Edit the markdown in docs/wiki-src/, never the HTML in docs/wiki/.

import { readFileSync, writeFileSync, existsSync, mkdirSync, readdirSync, rmSync } from "node:fs";
import { resolve, dirname, join, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { marked } from "marked";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, "..", "..");
const srcDir = resolve(repoRoot, "docs", "wiki-src");
const outDir = resolve(repoRoot, "docs", "wiki");
const configGo = resolve(repoRoot, "internal", "panel", "config", "config.go");
const versionGo = resolve(repoRoot, "internal", "shared", "version", "version.go");

const SITE = "https://krakenserver.io";
const BASE = "/wiki/";
const REPO = "https://github.com/briggleman/kraken";
const EDIT_BASE = `${REPO}/edit/main/docs/wiki-src/`;

const die = (msg) => {
  console.error("build-wiki: " + msg);
  process.exit(2);
};

// ---------------------------------------------------------------- utilities

const lf = (s) => s.split("\r\n").join("\n");

const esc = (s) =>
  String(s)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");

/** Walk a directory, returning POSIX-ish relative paths, sorted. */
function walk(dir, base = dir) {
  const out = [];
  for (const entry of readdirSync(dir, { withFileTypes: true }).sort((a, b) =>
    a.name < b.name ? -1 : a.name > b.name ? 1 : 0,
  )) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...walk(full, base));
    else out.push(relative(base, full).split(sep).join("/"));
  }
  return out;
}

/** GitHub-flavoured heading slug, with the usual de-duplicating suffix. */
function makeSlugger() {
  const seen = new Map();
  return (text) => {
    const base =
      text
        .toLowerCase()
        .replace(/<[^>]*>/g, "")
        .replace(/[^\w\- ]+/g, "")
        .trim()
        .replace(/\s+/g, "-") || "section";
    const n = seen.get(base) ?? 0;
    seen.set(base, n + 1);
    return n === 0 ? base : `${base}-${n}`;
  };
}

// ------------------------------------------------------------- front matter

/** Minimal YAML front matter: `key: value` lines, quotes optional. */
function parseFrontMatter(raw, file) {
  if (!raw.startsWith("---\n")) die(`${file}: missing front matter (--- title/description/section/order ---)`);
  const end = raw.indexOf("\n---\n", 3);
  if (end === -1) die(`${file}: front matter is not closed`);
  const head = raw.slice(4, end);
  const body = raw.slice(end + 5);
  const data = {};
  for (const line of head.split("\n")) {
    if (!line.trim()) continue;
    const m = /^([a-z_]+):\s*(.*)$/.exec(line);
    if (!m) die(`${file}: cannot parse front-matter line: ${line}`);
    let v = m[2].trim();
    if ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'"))) v = v.slice(1, -1);
    data[m[1]] = v;
  }
  for (const key of ["title", "description", "section", "order"]) {
    if (!data[key]) die(`${file}: front matter is missing "${key}"`);
  }
  data.order = Number(data.order);
  if (!Number.isFinite(data.order)) die(`${file}: "order" must be a number`);
  return { data, body };
}

// ---------------------------------------------------- the version stamp

function readVersion() {
  const src = readFileSync(versionGo, "utf8");
  const m = /Version\s*=\s*"([^"]+)"/.exec(src);
  if (!m) die("could not read Version from internal/shared/version/version.go");
  return m[1];
}

// ------------------------------------------- GENERATED: Panel env reference
//
// The rules, spelled out because the failure mode is a silently missing row:
//
//   * every KRAKEN_* variable read in config.go is found, whether it is read
//     through env/envInt/envList/boolEnv/durationEnv or straight from
//     os.Getenv / os.LookupEnv;
//   * a variable's DESCRIPTION is the comment block immediately above the call
//     site if there is one, otherwise the doc comment on the Config struct
//     field it is assigned to (the composite literal in Load() carries no
//     comments of its own — the prose lives on the struct);
//   * a variable with neither is a BUILD FAILURE, not a blank cell;
//   * rows are GROUPED the way config.go groups them: the struct's
//     blank-line-separated blocks, in source order. A variable read outside
//     the struct literal joins the group of the field it feeds.

/** Split the `type Config struct { … }` body into blank-line-separated blocks. */
function parseConfigStruct(src) {
  const start = src.indexOf("type Config struct {");
  if (start === -1) die("config.go: no `type Config struct` found");
  let depth = 0;
  let i = src.indexOf("{", start);
  const open = i;
  for (; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}") {
      depth--;
      if (depth === 0) break;
    }
  }
  const bodyLines = src.slice(open + 1, i).split("\n");

  const blocks = [];
  let block = null;
  let comment = [];
  for (const rawLine of bodyLines) {
    const line = rawLine.trim();
    if (line === "") {
      comment = [];
      block = null; // a blank line ends a group
      continue;
    }
    if (line.startsWith("//")) {
      comment.push(line.replace(/^\/\/ ?/, ""));
      continue;
    }
    const m = /^([A-Z]\w*)(?:\s*,\s*([A-Z]\w*))?\s+\S/.exec(line);
    if (!m) {
      comment = [];
      continue;
    }
    if (!block) {
      block = { fields: [], lead: comment.join("\n") };
      blocks.push(block);
    }
    // A field with no comment of its own inherits the one above the field
    // before it — `CSPScriptSrc and CSPConnectSrc are …` documents both — and
    // failing that the comment that opened the block.
    const prev = block.fields[block.fields.length - 1];
    for (const name of [m[1], m[2]].filter(Boolean)) {
      block.fields.push({
        name,
        doc: comment.join("\n") || prev?.doc || block.lead,
        index: blocks.length - 1,
      });
    }
    comment = [];
  }
  return blocks;
}

/** Resolve simple `Name = "value"` string constants declared in the file. */
function parseStringConsts(src) {
  const consts = new Map();
  for (const m of src.matchAll(/^\s*([A-Z]\w*)\s*=\s*"([^"]*)"\s*$/gm)) consts.set(m[1], m[2]);
  return consts;
}

/** Render a Go default expression as something an operator can read. */
function renderDefault(expr, consts) {
  if (expr === undefined || expr === null) return "";
  let e = expr.trim();
  if (e === "") return "";
  if (consts.has(e)) return consts.get(e);
  const str = /^"([^"]*)"$/.exec(e);
  if (str) return str[1];
  const dur = /^(\d+)\s*\*\s*time\.(Hour|Minute|Second)$/.exec(e);
  if (dur) return dur[1] + { Hour: "h", Minute: "m", Second: "s" }[dur[2]];
  if (/^\d+$/.test(e)) return e;
  const joined = /^filepath\.Join\((\w+),\s*"([^"]+)"\)$/.exec(e);
  if (joined) return `<${joined[1]}>/${joined[2]}`;
  if (e === "c.Env == \"dev\"") return "true in dev, false otherwise";
  return e;
}

// Groups whose first field does not spell its own heading, plus the homes for
// variables that are not struct fields at all.
const GROUP_TITLES = {
  Env: "core",
  BootstrapAdminUser: "bootstrap admin",
  AllowedOrigins: "browser origins",
  SetupAllowedCIDRs: "first-run setup",
  CSPMode: "content security policy",
  TLSCert: "panel → agent mTLS",
  CACert: "certificate authority",
  Quickstart: "quickstart (single host)",
  StateDir: "state on disk",
  TunnelAddr: "reverse tunnel",
  SFTPProxy: "sftp proxy (tunnel nodes)",
  TrustedProxies: "behind a reverse proxy",
  RateLimits: "rate limits",
  LogLevel: "logging",
};
const LOOSE_GROUPS = { KRAKEN_SECRETS_KEY: { title: "secrets at rest", after: "state on disk" } };

function extractPanelEnv() {
  const src = lf(readFileSync(configGo, "utf8"));
  const lines = src.split("\n");
  const blocks = parseConfigStruct(src);
  const consts = parseStringConsts(src);

  const fieldToGroup = new Map();
  const fieldDoc = new Map();
  blocks.forEach((b, gi) => {
    for (const f of b.fields) {
      fieldToGroup.set(f.name, gi);
      fieldDoc.set(f.name, f.doc);
    }
  });

  // Every env read in the file, in source order.
  const reads = [];
  const callRe =
    /(env|envInt|envList|boolEnv|durationEnv)\(\s*"(KRAKEN_[A-Z0-9_]+)"(?:\s*,\s*([^()]*?(?:\([^()]*\))?[^()]*?))?\s*\)|os\.(?:Getenv|LookupEnv)\(\s*"(KRAKEN_[A-Z0-9_]+)"\s*\)/g;
  lines.forEach((line, idx) => {
    for (const m of line.matchAll(callRe)) {
      const name = m[2] ?? m[4];
      const helper = m[1] ?? null; // null == a bare os.Getenv / os.LookupEnv
      const seen = reads.find((r) => r.name === name);
      if (seen) {
        // A bare LookupEnv is usually the "was it set?" probe beside the real
        // read; the helper call is the one that carries the default.
        if (seen.helper || !helper) continue;
        reads.splice(reads.indexOf(seen), 1);
      }
      reads.push({ name, helper, def: m[3], line: idx, text: line });
    }
  });
  if (reads.length === 0) die("config.go: found no KRAKEN_* environment reads — the extractor's patterns are stale");

  /** The comment block immediately above a line, if any. */
  const commentAbove = (idx) => {
    const out = [];
    for (let i = idx - 1; i >= 0; i--) {
      const t = lines[i].trim();
      if (t.startsWith("//")) out.unshift(t.replace(/^\/\/ ?/, ""));
      else break;
    }
    return out.join("\n");
  };

  /** The doc comment on the function a read sits inside, for the reads that
   *  are not part of the Config literal at all (the secrets key). */
  const enclosingFuncDoc = (idx) => {
    for (let i = idx; i >= 0; i--) {
      if (!/^func /.test(lines[i])) continue;
      const out = [];
      for (let j = i - 1; j >= 0; j--) {
        const t = lines[j].trim();
        if (t.startsWith("//")) out.unshift(t.replace(/^\/\/ ?/, ""));
        else break;
      }
      return out.join("\n");
    }
    return "";
  };

  /** Which Config field does this read feed? */
  const fieldFor = (read) => {
    const direct = /^\s*([A-Z]\w*):\s/.exec(read.text) || /^\s*c\.([A-Z]\w*)\s*=/.exec(read.text);
    if (direct && fieldToGroup.has(direct[1])) return direct[1];
    // `local := env(…)` assigned into the literal further down.
    const local = /^\s*(?:var\s+)?(\w+)(?:,\s*\w+)?\s*(?::=|=)/.exec(read.text);
    if (local) {
      const v = local[1];
      const re = new RegExp(`^\\s*([A-Z]\\w*):\\s*${v}\\s*,|^\\s*c\\.([A-Z]\\w*)\\s*=\\s*${v}\\s*$`);
      for (const line of lines) {
        const m = re.exec(line);
        const name = m && (m[1] || m[2]);
        if (name && fieldToGroup.has(name)) return name;
      }
    }
    return null;
  };

  const groups = blocks.map((b, gi) => ({
    title: GROUP_TITLES[b.fields[0]?.name] ?? (b.fields[0]?.name ?? `group ${gi}`).toLowerCase(),
    rows: [],
  }));
  const loose = [];
  const undocumented = [];

  for (const read of reads) {
    const field = fieldFor(read);
    const above = commentAbove(read.line);
    const doc = above || (field ? fieldDoc.get(field) : "") || (field ? "" : enclosingFuncDoc(read.line));
    if (!doc.trim()) {
      undocumented.push(read.name);
      continue;
    }
    const row = { name: read.name, def: renderDefault(read.def, consts), doc };
    if (field && fieldToGroup.has(field)) groups[fieldToGroup.get(field)].rows.push(row);
    else loose.push(row);
  }

  if (undocumented.length) {
    die(
      "these KRAKEN_* variables in internal/panel/config/config.go have no comment above the read and no doc " +
        "comment on the Config field they feed, so the wiki cannot describe them:\n  " +
        undocumented.join("\n  ") +
        "\nDocument them in config.go (the wiki is generated from it) and re-run.",
    );
  }

  const ordered = groups.filter((g) => g.rows.length > 0);
  for (const row of loose) {
    const home = LOOSE_GROUPS[row.name];
    const title = home?.title ?? "other";
    let g = ordered.find((x) => x.title === title);
    if (!g) {
      g = { title, rows: [] };
      const anchor = home?.after ? ordered.findIndex((x) => x.title === home.after) : -1;
      if (anchor === -1) ordered.push(g);
      else ordered.splice(anchor + 1, 0, g);
    }
    g.rows.push(row);
  }
  return ordered;
}

/** The generated reference, as markdown the ordinary renderer then handles. */
function panelEnvMarkdown() {
  const groups = extractPanelEnv();
  const cell = (s) =>
    marked
      .parseInline(
        s
          .split("\n")
          .map((l) => l.trim())
          .join(" ")
          .replace(/\s+/g, " "),
      )
      .replaceAll("|", "&#124;");
  // Two columns, not three: the name and its default are one fact about one
  // variable, and stacking them keeps the whole reference inside the prose
  // measure instead of behind a sideways drag.
  const out = [];
  for (const g of groups) {
    out.push(`### ${g.title}`, "");
    out.push('<div class="tbl"><table class="envs">');
    out.push("<thead><tr><th>variable</th><th>what it does</th></tr></thead>");
    out.push("<tbody>");
    for (const r of g.rows) {
      const def =
        r.def === ""
          ? '<span class="def">default <span class="nil">unset</span></span>'
          : `<span class="def">default <b>${esc(r.def)}</b></span>`;
      out.push(`<tr><td><code>${esc(r.name)}</code>${def}</td><td>${cell(r.doc)}</td></tr>`);
    }
    out.push("</tbody>", "</table></div>", "");
  }
  return out.join("\n");
}

// ------------------------------------------------------------- markdown

/**
 * Container directives, resolved before marked sees the text:
 *
 *   :::note / :::warning / :::security   the three callouts
 *   :::shot                              a screenshot placeholder — an HTML
 *                                        comment for whoever captures it, plus
 *                                        a dashed italic line on the page, the
 *                                        same dashed mark the house already
 *                                        spends on synthetic data
 */
function renderDirectives(md, render) {
  const kinds = { note: "note", warning: "warning", security: "security", shot: "shot" };
  const lines = md.split("\n");
  const out = [];
  for (let i = 0; i < lines.length; i++) {
    const open = /^:::(note|warning|security|shot)\s*$/.exec(lines[i]);
    if (!open) {
      out.push(lines[i]);
      continue;
    }
    const kind = kinds[open[1]];
    const inner = [];
    i++;
    while (i < lines.length && lines[i].trim() !== ":::") inner.push(lines[i]), i++;
    const text = inner.join("\n").trim();
    if (kind === "shot") {
      out.push(`<!-- SCREENSHOT: ${text.replace(/\s+/g, " ")} -->`);
      out.push(`<p class="shot"><em>screenshot: ${esc(text.replace(/\s+/g, " "))}</em></p>`);
    } else {
      out.push(`<aside class="callout is-${kind}">`);
      out.push(`<span class="callout-k">${kind}</span>`);
      out.push(`<div class="callout-b">${render(text)}</div>`);
      out.push("</aside>");
    }
    out.push("");
  }
  return out.join("\n");
}

const COPY_SVG =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" ' +
  'stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="11" height="11" rx="2"/>' +
  '<path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1"/></svg>';

const LINK_SVG =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" ' +
  'stroke-linejoin="round" aria-hidden="true"><path d="M10 13a5 5 0 0 0 7 0l3-3a5 5 0 0 0-7-7l-1 1"/>' +
  '<path d="M14 11a5 5 0 0 0-7 0l-3 3a5 5 0 0 0 7 7l1-1"/></svg>';

/** One renderer per page: the slugger and the heading list are per document. */
function makeRenderer(headings) {
  const slug = makeSlugger();
  const renderer = new marked.Renderer();

  renderer.heading = function ({ tokens, depth }) {
    const text = this.parser.parseInline(tokens);
    const id = slug(text);
    if (depth === 2 || depth === 3) headings.push({ id, depth, text });
    const anchor =
      depth === 1
        ? ""
        : ` <a class="hlink" href="#${id}" aria-label="link to this section">${LINK_SVG}</a>`;
    return `<h${depth} id="${id}">${text}${anchor}</h${depth}>\n`;
  };

  // The house Command Block: it wraps rather than scrolling sideways, and it
  // carries a copy affordance because everything in it is meant to be run.
  renderer.code = function ({ text, lang }) {
    const label = (lang || "").split(/\s+/)[0] || "";
    return (
      `<div class="cmd" data-lang="${esc(label)}">` +
      `<button class="cmd-copy" type="button" data-copy>${COPY_SVG}<span>copy</span></button>` +
      `<pre><code>${esc(text)}</code></pre>` +
      `</div>\n`
    );
  };

  // Tables scroll inside their own box on a phone rather than widening the page.
  const baseTable = renderer.table.bind(renderer);
  renderer.table = function (token) {
    return `<div class="tbl">${baseTable(token)}</div>\n`;
  };

  return renderer;
}

// -------------------------------------------------------------- the pages

function loadPages() {
  if (!existsSync(srcDir)) die("docs/wiki-src does not exist");
  const files = walk(srcDir).filter((f) => f.endsWith(".md"));
  if (!files.length) die("docs/wiki-src holds no markdown");

  const pages = files.map((file) => {
    const raw = lf(readFileSync(join(srcDir, file), "utf8"));
    const { data, body } = parseFrontMatter(raw, file);
    const noExt = file.replace(/\.md$/, "");
    const slugPath = noExt === "index" ? "" : noExt.replace(/\/index$/, "");
    const url = slugPath === "" ? BASE : `${BASE}${slugPath}/`;
    return { file, url, body, ...data };
  });

  const seen = new Set();
  for (const p of pages) {
    if (seen.has(p.url)) die(`two sources produce ${p.url}`);
    seen.add(p.url);
  }
  return pages;
}

/** Sidebar model: sections in order, each holding a nested page list. */
function buildNav(pages) {
  const sections = new Map();
  for (const p of pages) {
    if (!sections.has(p.section)) sections.set(p.section, []);
    sections.get(p.section).push(p);
  }
  const model = [...sections.entries()]
    .map(([title, items]) => ({
      title,
      order: Math.min(...items.map((i) => i.order)),
      items: items.slice().sort((a, b) => a.order - b.order || (a.url < b.url ? -1 : 1)),
    }))
    .sort((a, b) => a.order - b.order || (a.title < b.title ? -1 : 1));

  // One level of nesting: a page under another page's URL is its child.
  for (const sec of model) {
    const tree = [];
    for (const page of sec.items) {
      const parent = sec.items
        .filter((c) => c !== page && page.url.startsWith(c.url))
        .sort((a, b) => b.url.length - a.url.length)[0];
      if (parent) (parent.children ??= []).push(page);
      else tree.push(page);
    }
    sec.tree = tree;
  }
  const flat = [];
  for (const sec of model) {
    for (const page of sec.tree) {
      flat.push(page);
      for (const child of page.children ?? []) flat.push(child);
    }
  }
  return { model, flat };
}

function navHtml(nav, current) {
  const link = (p) =>
    `<a class="nav-link${p.url === current.url ? " is-here" : ""}" href="${p.url}"` +
    `${p.url === current.url ? ' aria-current="page"' : ""}>${esc(p.title)}</a>`;
  const out = [];
  for (const sec of nav.model) {
    const holdsCurrent = sec.items.some((p) => p.url === current.url);
    out.push(`<details class="nav-sec" open${holdsCurrent ? ' data-here="1"' : ""}>`);
    out.push(`<summary><span class="nav-sec-t">${esc(sec.title)}</span></summary>`);
    for (const page of sec.tree) {
      out.push(link(page));
      if (page.children?.length) {
        out.push('<div class="nav-kids">');
        for (const child of page.children) out.push(link(child));
        out.push("</div>");
      }
    }
    out.push("</details>");
  }
  return out.join("\n");
}

function crumbsHtml(page, byUrl) {
  if (page.url === BASE) return "";
  const parts = page.url.slice(BASE.length).replace(/\/$/, "").split("/");
  const crumbs = [`<a href="${BASE}">wiki</a>`];
  let acc = BASE;
  parts.forEach((part, i) => {
    acc += part + "/";
    const label = esc(byUrl.get(acc)?.title ?? part.replace(/-/g, " "));
    const last = i === parts.length - 1;
    crumbs.push(last ? `<span aria-current="page">${label}</span>` : byUrl.has(acc) ? `<a href="${acc}">${label}</a>` : `<span>${label}</span>`);
  });
  return `<nav class="crumbs" aria-label="breadcrumb">${crumbs.join('<i aria-hidden="true">/</i>')}</nav>`;
}

function tocHtml(headings) {
  if (headings.length < 2) return "";
  return headings
    .map((h) => `<a class="toc-link lvl-${h.depth}" href="#${h.id}">${h.text}</a>`)
    .join("\n");
}

const GLYPH =
  '<span class="kr-glyph" role="img" aria-label="Kraken glyph"></span><span class="kr-word">KRAKEN</span>';

const ARROW =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" ' +
  'stroke-linejoin="round" aria-hidden="true"><path d="M5 12h14M13 6l6 6-6 6"/></svg>';

function renderPage({ page, nav, byUrl, version }) {
  const headings = [];
  const renderer = makeRenderer(headings);
  const inline = (md) => marked.parse(md, { renderer, async: false });

  let body = page.body;
  if (body.includes("<!-- generated:panel-env -->")) {
    body = body.replace("<!-- generated:panel-env -->", panelEnvMarkdown());
  }
  body = renderDirectives(body, (t) => marked.parse(t, { renderer: new marked.Renderer(), async: false }));
  const article = inline(body);

  const idx = nav.flat.findIndex((p) => p.url === page.url);
  const prev = idx > 0 ? nav.flat[idx - 1] : null;
  const next = idx >= 0 && idx < nav.flat.length - 1 ? nav.flat[idx + 1] : null;
  const toc = tocHtml(headings);
  const canonical = SITE + page.url;
  const title = page.url === BASE ? `Kraken wiki — ${page.title}` : `${page.title} — Kraken wiki`;

  return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8" />
<meta name="viewport" content="width=device-width, initial-scale=1.0" />
<title>${esc(title)}</title>
<meta name="description" content="${esc(page.description)}" />
<link rel="canonical" href="${canonical}" />

<meta property="og:type" content="article" />
<meta property="og:site_name" content="Kraken" />
<meta property="og:url" content="${canonical}" />
<meta property="og:title" content="${esc(title)}" />
<meta property="og:description" content="${esc(page.description)}" />
<meta property="og:image" content="${SITE}/assets/kraken-social-card.png" />
<meta name="twitter:card" content="summary_large_image" />
<meta name="twitter:title" content="${esc(title)}" />
<meta name="twitter:description" content="${esc(page.description)}" />
<meta name="twitter:image" content="${SITE}/assets/kraken-social-card.png" />
<meta name="theme-color" content="#02090E" />

<link rel="icon" type="image/png" sizes="32x32" href="/assets/favicon-32.png" />
<link rel="apple-touch-icon" href="/assets/favicon-180.png" />
<link rel="preload" href="/assets/fonts/archivo-latin.woff2" as="font" type="font/woff2" crossorigin />
<link rel="preload" href="/assets/fonts/spline-sans-mono-latin.woff2" as="font" type="font/woff2" crossorigin />
<link rel="stylesheet" href="/wiki/assets/wiki.css" />
</head>
<body>

<div class="bg-depth"></div>
<div class="bg-fog-glow"></div>
<div class="bg-vignette"></div>

<a class="skip" href="#doc">skip to content</a>

<header class="top">
  <div class="top-in">
    <a class="kr-lock" href="/" aria-label="Kraken home">${GLYPH}</a>
    <span class="top-tag">wiki</span>
    <button class="nav-toggle" id="navToggle" aria-label="Toggle the wiki contents" aria-expanded="false" aria-controls="side">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true"><path d="M4 7h16M4 12h16M4 17h16"/></svg>
    </button>
    <nav class="top-nav">
      <a class="link" href="/">landing</a>
      <a class="link" href="${REPO}">github</a>
      <span class="stamp mono">v${esc(version)}</span>
    </nav>
  </div>
</header>

<div class="shell">
  <aside class="side" id="side">
    <nav class="nav" aria-label="wiki contents">
${navHtml(nav, page)}
    </nav>
  </aside>

  <main class="doc" id="doc">
    ${crumbsHtml(page, byUrl)}
    <h1>${esc(page.title)}</h1>
    <p class="lede">${esc(page.description)}</p>
${toc ? `    <details class="toc-in"><summary>on this page</summary><div class="toc">\n${toc}\n</div></details>` : ""}
    <article class="prose">
${article.trim()}
    </article>

    <nav class="pager" aria-label="more pages">
      ${
        prev
          ? `<a class="pg prev" href="${prev.url}"><span class="pg-k">previous</span><span class="pg-t">${esc(prev.title)}</span></a>`
          : '<span class="pg is-empty"></span>'
      }
      ${
        next
          ? `<a class="pg next" href="${next.url}"><span class="pg-k">next</span><span class="pg-t">${esc(next.title)}${ARROW}</span></a>`
          : '<span class="pg is-empty"></span>'
      }
    </nav>

    <footer class="doc-foot">
      <a class="edit" href="${EDIT_BASE}${page.file}">edit this page on github</a>
      <span class="mono">source: docs/wiki-src/${esc(page.file)}</span>
    </footer>
  </main>

  <aside class="rail" aria-label="on this page">
${toc ? `    <span class="rail-k">on this page</span>\n    <div class="toc">\n${toc}\n    </div>` : ""}
  </aside>
</div>

<footer class="site-foot">
  <div class="foot-in">
    <a class="kr-lock sm" href="/" aria-label="Kraken home">${GLYPH}</a>
    <nav class="foot-links">
      <a href="${BASE}">wiki</a>
      <a href="${REPO}">repository</a>
      <a href="${REPO}/blob/main/SECURITY.md">security</a>
      <a href="${REPO}/blob/main/LICENSE">gpl-3.0</a>
    </nav>
    <div class="signoff mono">
      <span>the deep is quiet</span><span class="sep">·</span>
      <span>documentation for v${esc(version)}</span><span class="sep">·</span>
      <span>generated from docs/wiki-src</span>
    </div>
  </div>
</footer>

<script src="/wiki/assets/wiki.js" defer></script>
</body>
</html>
`;
}

function sitemapXml(pages) {
  const urls = pages
    .map((p) => SITE + p.url)
    .sort()
    .map((u) => `  <url><loc>${u}</loc></url>`)
    .join("\n");
  return `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
${urls}
</urlset>
`;
}

// ------------------------------------------------------------------- main

marked.use({ gfm: true, breaks: false });

const version = readVersion();
const pages = loadPages();
const nav = buildNav(pages);
const byUrl = new Map(pages.map((p) => [p.url, p]));

/** path (relative to docs/wiki) -> contents */
const artifacts = new Map();
for (const page of pages) {
  const rel = page.url === BASE ? "index.html" : page.url.slice(BASE.length) + "index.html";
  artifacts.set(rel, lf(renderPage({ page, nav, byUrl, version })));
}
artifacts.set("sitemap.xml", sitemapXml(pages));

const check = process.argv.includes("--check");
let stale = [];

for (const [rel, next] of artifacts) {
  const dest = join(outDir, rel);
  const current = existsSync(dest) ? lf(readFileSync(dest, "utf8")) : null;
  if (current === next) continue;
  stale.push(rel);
  if (!check) {
    mkdirSync(dirname(dest), { recursive: true });
    writeFileSync(dest, next, "utf8");
  }
}

// Sources that went away must take their HTML with them.
if (existsSync(outDir)) {
  for (const rel of walk(outDir)) {
    if (!rel.endsWith("/index.html") && rel !== "index.html") continue;
    if (artifacts.has(rel)) continue;
    stale.push(`${rel} (orphaned)`);
    if (!check) rmSync(join(outDir, rel));
  }
}

if (check) {
  if (stale.length) {
    console.error(
      "build-wiki: docs/wiki is stale —\n  " +
        stale.join("\n  ") +
        "\nRun: node web/scripts/build-wiki.mjs (or `make wiki`) and commit the result.",
    );
    process.exit(1);
  }
  console.log(`build-wiki: docs/wiki matches docs/wiki-src (${pages.length} pages)`);
  process.exit(0);
}

console.log(`build-wiki: wrote ${artifacts.size} files for ${pages.length} pages (v${version})`);
