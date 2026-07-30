import { faDocker, faLinux, faWindows } from "@fortawesome/free-brands-svg-icons";
import type { IconDefinition } from "@fortawesome/free-brands-svg-icons";

const DEFS: Record<string, { def: IconDefinition; label: string }> = {
  linux: { def: faLinux, label: "Linux" },
  windows: { def: faWindows, label: "Windows" },
  docker: { def: faDocker, label: "Docker" },
};

/**
 * Renders the FontAwesome brand glyph for a server's host OS. The brands
 * package ships only icon definitions (no React renderer is installed), so we
 * draw the path data into an inline SVG ourselves, inheriting currentColor.
 *
 * `label` overrides the accessible name and tooltip — used where the glyph means
 * something narrower than the bare OS (the coral Windows mark for Wine).
 */
export function OsIcon({ os, size = 15, label, style }: { os: string; size?: number; label?: string; style?: React.CSSProperties }) {
  const entry = DEFS[os];
  if (!entry) return null;
  const name = label ?? entry.label;
  const [width, height, , , path] = entry.def.icon;
  const d = Array.isArray(path) ? path.join("") : path;
  return (
    <svg
      width={size}
      height={size}
      viewBox={`0 0 ${width} ${height}`}
      fill="currentColor"
      role="img"
      aria-label={name}
      style={style}
    >
      <title>{name}</title>
      <path d={d} />
    </svg>
  );
}
