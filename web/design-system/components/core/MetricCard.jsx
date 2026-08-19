import React from 'react';

/**
 * Dashboard stat tile — mono UPPERCASE label, big mono value with optional
 * unit suffix and accent color. Children render below for a meter, sparkline
 * or sub-line.
 *
 * Surface: a "defined instrument" — near-opaque panel with a machined top
 * highlight and a real drop shadow, so readouts never fight the ambient
 * backdrop. The abyss stays visible in the gutters between tiles.
 */
export function MetricCard({ label, value, suffix, accent, children, style, ...rest }) {
  return (
    <div
      style={{
        borderRadius: 'var(--radius-lg)',
        border: '1px solid rgba(61,245,207,.18)',
        background: 'linear-gradient(180deg, rgba(12,35,43,.94), rgba(8,25,31,.97))',
        boxShadow: 'inset 0 1px 0 rgba(234,255,247,.06), 0 16px 36px rgba(1,6,9,.5)',
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

/** Thin teal meter for use inside a MetricCard. `pct` is 0–100. An optional
 *  `color` (any CSS color, e.g. a status token) recolors the fill + glow for
 *  load-banded meters; default stays the teal accent gradient. */
export function MetricBar({ pct = 0, color, style }) {
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
          background: color || 'var(--gradient-accent-bar)',
          boxShadow: color
            ? `0 0 10px color-mix(in srgb, ${color} 70%, transparent)`
            : '0 0 10px rgba(61,245,207,.7)',
        }}
      />
    </div>
  );
}
