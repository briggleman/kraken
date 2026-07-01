import * as React from 'react';

export interface BadgeProps extends React.HTMLAttributes<HTMLSpanElement> {
  /** Color tone. Default `accent`. */
  tone?: 'accent' | 'coral' | 'info' | 'warn' | 'neutral';
  children?: React.ReactNode;
}

/** Compact mono tag for platforms, slugs and attribute chips. */
export function Badge(props: BadgeProps): JSX.Element;
