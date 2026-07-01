import React from 'react';

/**
 * Surface primitive — translucent fill over the depth gradient, 1px teal
 * hairline border, 15px radius. `glow` for live/primary surfaces;
 * `dashed` for empty/placeholder/permission zones.
 */
export function Card({
  children,
  glow = false,
  dashed = false,
  padding = 22,
  style,
  ...rest
}) {
  return (
    <div
      style={{
        borderRadius: 'var(--radius-lg)',
        border: dashed
          ? '1px dashed rgba(61,245,207,.2)'
          : `1px solid ${glow ? 'var(--border-strong)' : 'var(--border-subtle)'}`,
        background: dashed ? 'rgba(7,23,29,.3)' : 'rgba(7,23,29,.5)',
        boxShadow: glow ? 'var(--elevation-glow)' : 'none',
        padding,
        ...style,
      }}
      {...rest}
    >
      {children}
    </div>
  );
}
