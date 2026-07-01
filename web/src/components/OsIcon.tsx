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
 */
export function OsIcon({ os, size = 15, style }: { os: string; size?: number; style?: React.CSSProperties }) {
  const entry = DEFS[os];
  if (!entry) return null;
  const [width, height, , , path] = entry.def.icon;
  const d = Array.isArray(path) ? path.join("") : path;
  return (
    <svg
      width={size}
      height={size}
      viewBox={`0 0 ${width} ${height}`}
      fill="currentColor"
      role="img"
      aria-label={entry.label}
      style={style}
    >
      <title>{entry.label}</title>
      <path d={d} />
    </svg>
  );
}
