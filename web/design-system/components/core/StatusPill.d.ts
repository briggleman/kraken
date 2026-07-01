import * as React from 'react';

export type ServerStatus =
  | 'running' | 'starting' | 'stopping' | 'offline' | 'installing' | 'crashed';

export interface StatusPillProps extends React.HTMLAttributes<HTMLSpanElement> {
  /** Server state. Default `running`. */
  status?: ServerStatus;
  /** Optional label override (defaults to the state's word). */
  label?: string;
}

/** Server-state pill: hue + icon + label together, never color alone. */
export function StatusPill(props: StatusPillProps): JSX.Element;
