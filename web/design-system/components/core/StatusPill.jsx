import React from 'react';
import { Icon } from './Icon.jsx';

const STATES = {
  running:    { color: '#36E5A6', label: 'Running',    icon: 'running' },
  starting:   { color: '#F4C152', label: 'Starting',   icon: 'starting', spin: true },
  stopping:   { color: '#F4A24C', label: 'Stopping',   icon: 'stopping' },
  offline:    { color: '#9FB6B1', label: 'Offline',    icon: 'offline', ring: '#5B7470' },
  installing: { color: '#38B6FF', label: 'Installing', icon: 'installing' },
  crashed:    { color: '#FF5C57', label: 'Crashed',    icon: 'crashed' },
};

function rgba(hex, a) {
  const n = parseInt(hex.slice(1), 16);
  return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${a})`;
}

/**
 * Server-state pill. Hue + icon + label together — never color alone.
 */
export function StatusPill({ status = 'running', label, style, ...rest }) {
  const s = STATES[status] || STATES.running;
  const ring = s.ring || s.color;
  return (
    <span
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 8,
        padding: '6px 13px',
        borderRadius: 'var(--radius-pill)',
        background: rgba(s.ring || s.color, status === 'offline' ? 0.14 : 0.12),
        border: `1px solid ${rgba(ring, status === 'offline' ? 0.5 : 0.4)}`,
        color: s.color,
        fontFamily: 'var(--font-mono)',
        fontSize: 12,
        fontWeight: 'var(--weight-medium)',
        whiteSpace: 'nowrap',
        ...style,
      }}
      {...rest}
    >
      <Icon
        name={s.icon}
        size={14}
        strokeWidth={2.2}
        style={s.spin ? { animation: 'abyssalSpin 1.4s linear infinite' } : undefined}
      />
      {label || s.label}
    </span>
  );
}
