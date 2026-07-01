import { faCloudflare } from "@fortawesome/free-brands-svg-icons";
import type { IconDefinition } from "@fortawesome/free-brands-svg-icons";

interface Props {
  size?: number;
  style?: React.CSSProperties;
}

// faGlyph draws a FontAwesome icon-definition's path into an inline SVG using
// currentColor (the brands package ships only path data, no React renderer), so
// the glyph inherits our theme color instead of any baked-in brand color.
function faGlyph(def: IconDefinition, label: string, { size = 16, style }: Props) {
  const [width, height, , , path] = def.icon;
  const d = Array.isArray(path) ? path.join("") : path;
  return (
    <svg width={size} height={size} viewBox={`0 0 ${width} ${height}`} fill="currentColor" role="img" aria-label={label} style={style}>
      <path d={d} />
    </svg>
  );
}

/** Cloudflare brand glyph, themed via currentColor. */
export function CloudflareIcon(props: Props) {
  return faGlyph(faCloudflare, "Cloudflare", props);
}

/**
 * UniFi (Ubiquiti) brand glyph. Not in FontAwesome, so the path is inlined from
 * the official mark and recolored with currentColor to match the design system.
 */
export function UnifiIcon({ size = 16, style }: Props) {
  return (
    <svg width={size} height={size} viewBox="0 0 512 512" fill="currentColor" role="img" aria-label="UniFi" style={style}>
      <path d="M494.2 0h-31.8v31.8h31.8zM383.1 222.4v-63.6h63.5v63.5h63.5c1.1 58.9-3.4 110.2-33.3 161.6-86.6 152.4-300.5 172.9-414 39.2C36.3 392.4 17.2 355 8.3 315c-4.5-21.7-6.5-49.2-6.5-72.5V4h127l.2 242c.6 31.3 6.3 63.5 25 88 53.9 73 167.9 66.3 212.1-13.1 15.9-26.6 17.3-68.7 17-98.5m15.8-174.8h47.6v47.6H510v63.5h-63.5V95.3h-47.6z" />
    </svg>
  );
}
