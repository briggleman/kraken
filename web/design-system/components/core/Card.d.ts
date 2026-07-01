import * as React from 'react';

export interface CardProps extends React.HTMLAttributes<HTMLDivElement> {
  /** Teal glow + stronger border, for live / primary surfaces. */
  glow?: boolean;
  /** Dashed teal border for empty / placeholder / permission zones. */
  dashed?: boolean;
  /** Inner padding in px. Default 22. */
  padding?: number | string;
  children?: React.ReactNode;
}

/** Base translucent surface over the depth gradient. */
export function Card(props: CardProps): JSX.Element;
