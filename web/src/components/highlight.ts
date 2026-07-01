// A tiny, dependency-free syntax highlighter for the config formats the file
// editor handles (ini / properties / conf / env / yaml / json / log). It returns
// an HTML string of <span> tokens for react-simple-code-editor's `highlight`
// prop. It must not add or drop characters — only wrap them — so the overlay
// stays aligned with the textarea.

function esc(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

const C = {
  comment: "var(--text-faint)",
  key: "var(--accent)",
  punct: "var(--text-muted)",
  string: "#e6c07b",
  number: "var(--accent-2, #38b6ff)",
  literal: "#ff8c6b", // true / false / null / none
};

function span(color: string, text: string): string {
  return `<span style="color:${color}">${esc(text)}</span>`;
}

function highlightValue(v: string): string {
  const t = v.trim();
  if ((/^".*"$/.test(t) || /^'.*'$/.test(t)) && t.length > 1) return span(C.string, v);
  if (/^-?\d+(\.\d+)?$/.test(t)) return span(C.number, v);
  if (/^(true|false|null|none|on|off|yes|no)$/i.test(t)) return span(C.literal, v);
  return esc(v);
}

function highlightLine(line: string): string {
  const trimmed = line.trimStart();
  // Comments: #, ; or // at the start of a line.
  if (trimmed.startsWith("#") || trimmed.startsWith(";") || trimmed.startsWith("//")) {
    return span(C.comment, line);
  }
  // INI section headers: [section]
  if (/^\s*\[.*\]\s*$/.test(line)) {
    return `<span style="color:${C.key};font-weight:600">${esc(line)}</span>`;
  }
  // key <sep> value, where sep is = or :
  const m = line.match(/^(\s*)([^=:]+?)(\s*[=:]\s*)([\s\S]*)$/);
  if (m) {
    const [, ws, key, sep, val] = m;
    return esc(ws) + span(C.key, key) + span(C.punct, sep) + highlightValue(val);
  }
  return esc(line);
}

export function highlightConfig(code: string): string {
  return code.split("\n").map(highlightLine).join("\n");
}
