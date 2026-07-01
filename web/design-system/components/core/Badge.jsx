import React from 'react';

const TONES = {
  accent:  { bg: 'rgba(61,245,207,.10)', border: 'rgba(61,245,207,.2)',  color: '#7df0d8' },
  coral:   { bg: 'rgba(255,122,77,.10)', border: 'rgba(255,122,77,.3)',  color: '#ff9d76' },
  info:    { bg: 'rgba(56,182,255,.10)', border: 'rgba(56,182,255,.3)',  color: '#9fd6ff' },
  warn:    { bg: 'rgba(244,193,82,.10)', border: 'rgba(244,193,82,.32)', color: '#f4c152' },
  neutral: { bg: 'rgba(7,23,29,.5)',     border: 'rgba(61,245,207,.16)', color: '#bfe9e2' },
};

/**
 * Compact mono tag — platform marks, game slugs, attribute chips.
 */
export function Badge({ children, tone = 'accent', style, ...rest }) {
  const t = TONES[tone] || TONES.accent;
  return (
    <span
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 7,
        padding: '4px 9px',
        borderRadius: 'var(--radius-sm)',
        background: t.bg,
        border: `1px solid ${t.border}`,
        color: t.color,
        fontFamily: 'var(--font-mono)',
        fontSize: 10.5,
        letterSpacing: '.5px',
        lineHeight: 1.4,
        whiteSpace: 'nowrap',
        ...style,
      }}
      {...rest}
    >
      {children}
    </span>
  );
}
