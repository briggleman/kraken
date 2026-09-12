// Abyssal syntax highlight for the spec code editor (SpecEdit.svelte's code view).
//
// Returns HTML with the .spec-code token classes DESIGN.md documents — keys
// violet, strings teal, numbers gold, literals magenta, {{templates}} lumen,
// structural punctuation (.p) a step of teal under the strings, comments faint.
// Only whitespace is left untagged. It renders behind an editable <textarea> (a transparent-text
// overlay), so it must MATCH the textarea's plain wrapping: no per-line hanging
// indent, tokens emitted inline with the source's own newlines.
//
// The input is the operator's editable spec text, injected via {@html}. Escaping
// therefore happens BEFORE any token wrapping, and every wrapper is a fixed,
// safe literal — a value can never introduce a tag. (CSP forbids inline event
// handlers and scripts regardless, but the escape is the real guard here.)

const esc = (s: string): string =>
  s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

// Wrap {{SLOT}} template markers inside already-escaped text. Braces can't be a
// tag, so running this after esc() is safe.
const withTemplates = (escaped: string): string =>
  escaped.replace(/\{\{[^{}]+\}\}/g, (m) => `<span class="t">${m}</span>`);

// Guards (#279). A spec can legitimately carry a multi-KB value on ONE line —
// the base64 of a PowerShell -EncodedCommand in an install_script — and the
// document is re-highlighted as you type. Tokenising is only milliseconds even
// then, so these thresholds are not about the CPU: they bound how many inline
// boxes the overlay hands the layout engine for a run of text that has no break
// opportunities and reads as one opaque blob anyway. Past them the line falls
// back to escaped plain text — one text node, which is what it looked like.
//
// 8 KB is comfortably above the longest line in any bundled spec (palworld's
// config template, ~4.5 KB), so nothing real loses its colour.
const MAX_LINE = 8_000;
const MAX_DOC = 200_000;

/**
 * Highlight spec source a LINE AT A TIME, in the given serialisation.
 *
 * Returned per line rather than as one blob so the editor can mount one
 * `{@html}` per line: typing then rewrites the line you are on instead of
 * replacing — and re-laying-out — the whole overlay on every keystroke (#279).
 *
 * Both serialisations tokenise per line. For YAML that was always true; for
 * JSON it is free, because a JSON string cannot hold a raw newline — so
 * splitting first costs nothing in correctness and buys the per-line guard.
 * (It also stops one unterminated quote mid-edit from painting the rest of the
 * document as a string.)
 */
export function highlightLines(text: string, fmt: "json" | "yaml"): string[] {
  const lines = text.split("\n");
  if (text.length > MAX_DOC) return lines.map(esc);
  const one = fmt === "yaml" ? highlightYAMLLine : highlightJSONLine;
  return lines.map((l) => (l.length > MAX_LINE ? esc(l) : one(l)));
}

/** Highlight a whole document. Line-joined `highlightLines`. */
export function highlight(text: string, fmt: "json" | "yaml"): string {
  return highlightLines(text, fmt).join("\n");
}

// Sticky (/y) so they can be matched AT an offset without slicing the rest of
// the line off first — the per-character `text.slice(i)` this used to do made
// tokenising quadratic in the line length (#279).
const NUM_RE = /-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/y;
const LIT_RE = /(?:true|false|null)\b/y;

function highlightJSONLine(text: string): string {
  let out = "";
  let i = 0;
  const n = text.length;
  while (i < n) {
    const c = text[i];
    if (c === '"') {
      // scan to the closing quote, honouring backslash escapes
      let j = i + 1;
      while (j < n) {
        if (text[j] === "\\") {
          j += 2;
          continue;
        }
        if (text[j] === '"') {
          j++;
          break;
        }
        j++;
      }
      const raw = text.slice(i, j);
      // a string is a key when the next non-space character is a colon
      let k = j;
      while (k < n && (text[k] === " " || text[k] === "\t")) k++;
      const inner = withTemplates(esc(raw));
      out += text[k] === ":" ? `<b>${inner}</b>` : `<i>${inner}</i>`;
      i = j;
      continue;
    }
    if (c === "-" || (c >= "0" && c <= "9")) {
      NUM_RE.lastIndex = i;
      const m = NUM_RE.exec(text);
      if (m) {
        out += `<span class="n">${m[0]}</span>`;
        i += m[0].length;
        continue;
      }
    }
    // gate on the initial letter so the literal regex only runs where one of
    // true/false/null could actually begin
    if (c === "t" || c === "f" || c === "n") {
      LIT_RE.lastIndex = i;
      const lit = LIT_RE.exec(text);
      if (lit) {
        out += `<span class="k">${lit[0]}</span>`;
        i += lit[0].length;
        continue;
      }
    }
    // structural punctuation is the green scaffold; whitespace is the only base text
    out += "{}[]:,".includes(c) ? `<span class="p">${c}</span>` : esc(c);
    i++;
  }
  return out;
}

function highlightYAMLLine(line: string): string {
  // peel off a comment: '#' at line start or after whitespace (best-effort; a '#'
  // inside a quoted value is rare in serialised specs and not worth a full parser)
  let code = line;
  let comment = "";
  const cm = /(^|\s)#.*$/.exec(line);
  if (cm) {
    const cut = cm.index + (cm[1] ? cm[1].length : 0);
    code = line.slice(0, cut);
    comment = line.slice(cut);
  }

  // leading indent stays base; the "- " list markers in it are structure (.p)
  const lead = (/^(\s*(?:-\s+)*)/.exec(code) as RegExpExecArray)[0];
  const rest = code.slice(lead.length);
  let html = esc(lead).replace(/-/g, '<span class="p">-</span>');

  const kv = /^([^:\s#][^:]*?):(\s*)(.*)$/.exec(rest);
  if (kv) {
    // the comment peel leaves the value's trailing space behind — keep it as
    // whitespace so `true # note` still classifies as a literal, not a string
    const val = kv[3].trimEnd();
    const tail = kv[3].slice(val.length);
    html += `<b>${esc(kv[1])}</b><span class="p">:</span>${esc(kv[2])}` + valueHTML(val) + esc(tail);
  } else if (rest.length) {
    html += valueHTML(rest); // a bare scalar (e.g. a list item)
  }
  if (comment) html += `<em>${esc(comment)}</em>`;
  return html;
}

function valueHTML(v: string): string {
  if (v === "") return "";
  if (/^-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?$/.test(v)) return `<span class="n">${esc(v)}</span>`;
  if (/^(true|false|null|~)$/.test(v)) return `<span class="k">${esc(v)}</span>`;
  if (v === "[]" || v === "{}") return `<span class="p">${esc(v)}</span>`; // empty flow collection = punctuation
  return `<i>${withTemplates(esc(v))}</i>`;
}
