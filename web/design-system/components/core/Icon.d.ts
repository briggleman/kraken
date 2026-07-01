import * as React from 'react';

export type IconName =
  | 'running' | 'starting' | 'stopping' | 'offline' | 'installing' | 'crashed'
  | 'check' | 'plus' | 'search' | 'copy' | 'folder' | 'file' | 'lock' | 'database'
  | 'refresh' | 'info' | 'octagon' | 'chevron' | 'clock' | 'gear' | 'x' | 'play' | 'linux' | 'windows' | 'wine'
  | 'eye' | 'eye-off' | 'kraken';

export interface IconProps extends React.SVGProps<SVGSVGElement> {
  /** Glyph name from the Abyssal icon set. */
  name: IconName;
  /** Pixel size (width & height). Default 18. */
  size?: number;
  /** Stroke width for line icons. Default 2 (range 1.5–2.2). */
  strokeWidth?: number;
}

/**
 * Lucide-style line icon. Renders at `currentColor` so it inherits the
 * surrounding status tint. Every server state has a dedicated glyph.
 */
export function Icon(props: IconProps): JSX.Element | null;
