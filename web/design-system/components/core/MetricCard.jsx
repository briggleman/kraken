import React from 'react';

/**
 * Dashboard stat tile — mono UPPERCASE label, big mono value with optional
 * unit suffix and accent color. Children render below for a meter, sparkline
 * or sub-line.
 */
export function MetricCard({ label, value, suffix, accent, children, style, ...rest }) {
  return (
    <div
      style={{
        borderRadius: 'var(--radius-lg)',
        border: '1px solid rgba(61,245,207,.13)',
        background: 'rgba(7,23,29,.55)',
        padding: 20,
        ...style,
      }}
      {...rest}
    >
      <div
        style={{
          fontFamily: 'var(--font-mono)',
          fontSize: 11,
          letterSpacing: '1.5px',
          color: 'var(--text-muted)',
          marginBottom: 12,
        }}
      >
        {label}
      </div>
      <div
        style={{
          fontFamily: 'var(--font-mono)',
          fontSize: 32,
          fontWeight: 'var(--weight-black)',
          lineHeight: 1,
          color: accent || 'var(--text-primary)',
        }}
      >
        {value}
        {suffix ? (
          <span style={{ fontSize: 17, color: 'var(--text-muted)', fontWeight: 'var(--weight-black)' }}>
            {suffix}
          </span>
        ) : null}
      </div>
      {children}
    </div>
  );
}

/** Thin teal meter for use inside a MetricCard. `pct` is 0–100. */
export function MetricBar({ pct = 0, style }) {
  return (
    <div
      style={{
        height: 6,
        borderRadius: 4,
        background: 'rgba(61,245,207,.12)',
        marginTop: 18,
        overflow: 'hidden',
        ...style,
      }}
    >
      <div
        style={{
          width: `${Math.max(0, Math.min(100, pct))}%`,
          height: '100%',
          background: 'var(--gradient-accent-bar)',
          boxShadow: '0 0 10px rgba(61,245,207,.7)',
        }}
      />
    </div>
  );
}
