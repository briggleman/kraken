import * as React from 'react';

export interface InputProps
  extends Omit<React.InputHTMLAttributes<HTMLInputElement>, 'style'> {
  /** Mono UPPERCASE field label. */
  label?: React.ReactNode;
  /** Helper / hint text below the field. */
  helper?: React.ReactNode;
  /** Error state — recolors border, value and helper. */
  error?: boolean;
  /** Show the teal focus ring. */
  focused?: boolean;
  /** Render the value in JetBrains Mono (IDs, addresses, config). */
  mono?: boolean;
  /** Wrapper style. */
  style?: React.CSSProperties;
}

/** Recessed inset text field with mono label and teal focus ring. */
export function Input(props: InputProps): JSX.Element;
