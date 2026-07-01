import React from 'react';

const PATHS = {
  // status
  running:    { el: '<circle cx="12" cy="12" r="9"/><path d="M8.5 12.5l2.5 2.5 4.5-5"/>' },
  starting:   { el: '<path d="M12 3a9 9 0 1 0 9 9"/>' },
  stopping:   { el: '<rect x="6" y="6" width="12" height="12" rx="2"/>' },
  offline:    { el: '<path d="M12 4v8"/><path d="M7.5 7a7 7 0 1 0 9 0"/>' },
  installing: { el: '<path d="M12 4v10"/><path d="M8 11l4 4 4-4"/><path d="M5 19h14"/>' },
  crashed:    { el: '<path d="M12 4l9 15H3z"/><path d="M12 10v4"/><path d="M12 17v.5"/>' },
  // utility
  check:   { el: '<path d="M5 12l5 5 9-11"/>' },
  plus:    { el: '<path d="M3 12h18"/><path d="M12 3v18"/>' },
  search:  { el: '<circle cx="11" cy="11" r="7"/><path d="M21 21l-4-4"/>' },
  copy:    { el: '<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/>' },
  folder:  { el: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>' },
  file:    { el: '<path d="M6 3h8l4 4v14H6z"/><path d="M14 3v4h4"/>' },
  lock:    { el: '<rect x="5" y="11" width="14" height="9" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/>' },
  database:{ el: '<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v6c0 1.66 3.58 3 8 3s8-1.34 8-3V5"/><path d="M4 11v6c0 1.66 3.58 3 8 3s8-1.34 8-3v-6"/>' },
  refresh: { el: '<path d="M3 12a9 9 0 1 0 3-6.7"/><path d="M3 4v4h4"/>' },
  info:    { el: '<circle cx="12" cy="12" r="9"/><path d="M12 8v5"/><path d="M12 16v.5"/>' },
  octagon: { el: '<path d="M8.5 3h7L21 8.5v7L15.5 21h-7L3 15.5v-7z"/><path d="M12 8v4.5"/><path d="M12 16v.5"/>' },
  chevron: { el: '<path d="M6 9l6 6 6-6"/>' },
  clock:   { el: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3.5 2"/>' },
  gear:    { el: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/>' },
  x:       { el: '<path d="M18 6 6 18"/><path d="M6 6l12 12"/>' },
  eye:     { el: '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7z"/><circle cx="12" cy="12" r="3"/>' },
  'eye-off': { el: '<path d="M9.9 4.24A9.12 9.12 0 0 1 12 5c6.5 0 10 7 10 7a18.5 18.5 0 0 1-2.16 3.19"/><path d="M6.6 6.6A18.45 18.45 0 0 0 2 12s3.5 7 10 7a9.1 9.1 0 0 0 5.4-1.6"/><path d="M14.12 14.12A3 3 0 1 1 9.88 9.88"/><path d="M2 2l20 20"/>' },
  play:    { el: '<path d="M7 5l12 7-12 7z"/>', fill: true },
  // platform
  linux:   { el: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M7 9l3 3-3 3"/><path d="M13 15h4"/>' },
  windows: { el: '<rect x="3" y="4" width="8" height="8" rx="1"/><rect x="13" y="4" width="8" height="8" rx="1"/><rect x="3" y="13" width="8" height="8" rx="1"/><rect x="13" y="13" width="8" height="8" rx="1"/>', fill: true },
  wine:    { el: '<path d="M8 4h8a4 6 0 0 1-4 8 4 6 0 0 1-4-8z"/><path d="M12 12v6"/><path d="M9 20h6"/>' },
  // brand — kraken mark rendered as a single outline contour (no fill)
  kraken:  { el: '<path d="M12 2c-2.6 0-4.3 2-4.3 4.7 0 1.7.9 3 1.9 3.9"/><path d="M12 2c2.6 0 4.3 2 4.3 4.7 0 1.7-.9 3-1.9 3.9"/><path d="M9.6 10.6c-1.2.9-2.8 1.3-4.4 1.3-1.7 0-3 1-3 2.6 0 1.2 1 2 2 2 .9 0 1.5-.6 1.5-1.4 0-.6-.4-1-.9-1"/><path d="M14.4 10.6c1.2.9 2.8 1.3 4.4 1.3 1.7 0 3 1 3 2.6 0 1.2-1 2-2 2-.9 0-1.5-.6-1.5-1.4 0-.6.4-1 .9-1"/><path d="M10.5 11.5c-.8 1.4-1.2 3.2-1.2 5 0 1.6-.8 2.8-2 2.8-1 0-1.7-.8-1.7-1.7 0-.7.5-1.2 1.1-1.2"/><path d="M13.5 11.5c.8 1.4 1.2 3.2 1.2 5 0 1.6.8 2.8 2 2.8 1 0 1.7-.8 1.7-1.7 0-.7-.5-1.2-1.1-1.2"/><path d="M12 12c-.7 1.6-1 3.6-1 5.6 0 2-.5 3.4-1.6 4.2"/><path d="M12 12c.7 1.6 1 3.6 1 5.6 0 2 .5 3.4 1.6 4.2"/>' },
};

/**
 * Abyssal icon — Lucide-style line glyph, currentColor, 24x24 viewBox.
 * Stroke icons inherit color; `play` / `windows` are filled.
 */
export function Icon({ name, size = 18, strokeWidth = 2, style, ...rest }) {
  const def = PATHS[name];
  if (!def) return null;
  const common = {
    width: size,
    height: size,
    viewBox: '0 0 24 24',
    style,
    dangerouslySetInnerHTML: { __html: def.el },
    ...rest,
  };
  if (def.fill) {
    return <svg {...common} fill="currentColor" stroke="none" />;
  }
  return (
    <svg
      {...common}
      fill="none"
      stroke="currentColor"
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
    />
  );
}
