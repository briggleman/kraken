import * as React from 'react';

export interface MetricCardProps extends React.HTMLAttributes<HTMLDivElement> {
  /** Mono UPPERCASE label. */
  label: React.ReactNode;
  /** Big mono value. */
  value: React.ReactNode;
  /** Muted unit suffix (e.g. `%`, `/4`). */
  suffix?: React.ReactNode;
  /** Override the value color (e.g. var(--status-crashed) for alarms). */
  accent?: string;
  /** Renders below the value — a meter, sparkline or sub-line. */
  children?: React.ReactNode;
}

export interface MetricBarProps {
  /** Fill percentage, 0–100. */
  pct?: number;
  style?: React.CSSProperties;
}

/** Dashboard stat tile. */
export function MetricCard(props: MetricCardProps): JSX.Element;
/** Thin teal meter, intended as a MetricCard child. */
export function MetricBar(props: MetricBarProps): JSX.Element;
