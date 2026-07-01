import React from 'react';
import type { IconName } from './Icon';

export interface SelectOption {
  /** Stable value passed to `onChange` and matched against `value`. */
  value: string;
  /** Visible row label. */
  label: React.ReactNode;
  /** Optional right-aligned mono hint (e.g. load %, app id, region). */
  hint?: React.ReactNode;
  /** Optional leading glyph, tinted teal when selected. */
  icon?: IconName;
  /** Greyed out, not pickable. */
  disabled?: boolean;
}

export interface SelectProps {
  /** Mono UPPERCASE field label (matches Input). */
  label?: string;
  /** Currently-selected value, or null. */
  value?: string | null;
  options: SelectOption[];
  /** Shown when nothing is selected. Default "Select…". */
  placeholder?: string;
  onChange?: (value: string, option: SelectOption) => void;
  disabled?: boolean;
  /** Recolor the trigger border red (validation). */
  error?: boolean;
  /** Helper / error line under the field. */
  helper?: string;
  /** Render the value + options in JetBrains Mono — for IDs, nodes, ports. */
  mono?: boolean;
  /** Control scale. Default `md`. */
  size?: 'sm' | 'md';
  /** Start opened — for specimens/screenshots only. */
  defaultOpen?: boolean;
  style?: React.CSSProperties;
}

/**
 * Themed dropdown — a custom trigger + popover listbox (never a native
 * <select>, whose option list is OS-rendered and breaks the dark theme).
 * Full keyboard support, type-ahead, optional option icons + hints,
 * auto-flips up near the viewport bottom.
 */
export function Select(props: SelectProps): JSX.Element;
