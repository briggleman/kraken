// Abyssal syntax highlight for the spec code editor (SpecEdit.svelte's code view).
//
// Returns HTML with the .spec-code token classes DESIGN.md documents — keys
// violet, strings teal, numbers gold, literals magenta, {{templates}} lumen,
// comments faint. It renders behind an editable <textarea> (a transparent-text
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

/** Highlight spec source in the given serialisation. */
export function highlight(text: string, fmt: "json" | "yaml"): string {
  return fmt === "yaml" ? highlightYAML(text) : highlightJSON(text);
}

function highlightJSON(text: string): string {
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
      const m = /^-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/.exec(text.slice(i));
      if (m) {
        out += `<span class="n">${m[0]}</span>`;
        i += m[0].length;
        continue;
      }
    }
    const lit = /^(true|false|null)\b/.exec(text.slice(i));
    if (lit) {
      out += `<span class="k">${lit[0]}</span>`;
      i += lit[0].length;
      continue;
    }
    out += esc(c);
    i++;
  }
  return out;
}

function highlightYAML(text: string): string {
  return text.split("\n").map(highlightYAMLLine).join("\n");
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

  // leading indent and any "- " list markers are structure (base colour)
  const lead = (/^(\s*(?:-\s+)*)/.exec(code) as RegExpExecArray)[0];
  const rest = code.slice(lead.length);
  let html = esc(lead);

  const kv = /^([^:\s#][^:]*?):(\s*)(.*)$/.exec(rest);
  if (kv) {
    html += `<b>${esc(kv[1])}</b>:${esc(kv[2])}` + valueHTML(kv[3]);
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
  if (v === "[]" || v === "{}") return esc(v); // empty flow collection = punctuation
  return `<i>${withTemplates(esc(v))}</i>`;
}
