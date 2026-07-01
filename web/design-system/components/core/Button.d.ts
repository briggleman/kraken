import * as React from 'react';
import type { IconName } from './Icon';

export interface ButtonProps
  extends Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, 'style'> {
  /** Visual style. Use `primary` for the single key action per view. */
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger';
  /** Control height. `sm` or `md` (default md). */
  size?: 'sm' | 'md';
  disabled?: boolean;
  /** Leading icon — an Abyssal icon name, or any React node. */
  icon?: IconName | React.ReactNode;
  children?: React.ReactNode;
  type?: 'button' | 'submit' | 'reset';
  style?: React.CSSProperties;
}

export function Button(props: ButtonProps): JSX.Element;
