import * as React from 'react';

export interface ToggleProps {
  checked?: boolean;
  onChange?: (next: boolean) => void;
  disabled?: boolean;
  style?: React.CSSProperties;
}

/** Teal-gradient switch; recessed track when off. */
export function Toggle(props: ToggleProps): JSX.Element;
