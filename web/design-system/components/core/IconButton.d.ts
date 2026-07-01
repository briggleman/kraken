import * as React from 'react';
import type { IconName } from './Icon';

export interface IconButtonProps
  extends Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, 'style'> {
  /** Glyph name (required). */
  icon: IconName;
  /** 30px (sm) or 36px (md). Default md. */
  size?: 'sm' | 'md';
  /** Default `secondary`. */
  variant?: 'secondary' | 'ghost' | 'accent';
  disabled?: boolean;
  style?: React.CSSProperties;
}

/** Square icon-only button for toolbar / row actions. */
export function IconButton(props: IconButtonProps): JSX.Element;
