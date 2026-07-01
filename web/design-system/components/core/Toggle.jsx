import React from 'react';

/**
 * Toggle switch. On = teal gradient + glow with the knob to the right;
 * off = recessed track with the knob to the left. The knob is abyss-dark.
 */
export function Toggle({ checked = false, onChange, disabled = false, style, ...rest }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      disabled={disabled}
      onClick={disabled ? undefined : () => onChange && onChange(!checked)}
      style={{
        position: 'relative',
        width: 42,
        height: 24,
        flex: 'none',
        padding: 0,
        border: checked ? '1px solid transparent' : '1px solid var(--border-subtle)',
        borderRadius: 'var(--radius-pill)',
        background: checked ? 'var(--gradient-accent-bar)' : 'rgba(3,15,20,.8)',
        boxShadow: checked ? 'var(--elevation-glow-soft)' : 'none',
        cursor: disabled ? 'not-allowed' : 'pointer',
        opacity: disabled ? 0.45 : 1,
        transition: 'background var(--duration-base) var(--ease-standard)',
        ...style,
      }}
      {...rest}
    >
      <span
        style={{
          position: 'absolute',
          top: 2,
          left: checked ? 20 : 2,
          width: 20,
          height: 20,
          borderRadius: '50%',
          background: checked ? 'var(--text-on-accent)' : '#7fa6a1',
          transition: 'left var(--duration-base) var(--ease-out)',
        }}
      />
    </button>
  );
}
