import React from 'react';
import { Icon } from './Icon.jsx';

const SIZES = { sm: 30, md: 36 };

/**
 * Square icon-only button — toolbar actions, copy, close, row controls.
 */
export function IconButton({
  icon,
  size = 'md',
  variant = 'secondary',
  disabled = false,
  style,
  ...rest
}) {
  const px = SIZES[size] || SIZES.md;
  const variants = {
    secondary: {
      border: '1px solid var(--border-subtle)',
      background: 'rgba(7,26,33,.5)',
      color: '#dff7f1',
    },
    ghost: { border: '1px solid transparent', background: 'transparent', color: 'var(--text-secondary)' },
    accent: {
      border: '1px solid var(--border-strong)',
      background: 'var(--accent-wash-12)',
      color: 'var(--accent-hover)',
    },
  };
  return (
    <button
      disabled={disabled}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        width: px,
        height: px,
        flex: 'none',
        borderRadius: 'var(--radius-sm)',
        cursor: disabled ? 'not-allowed' : 'pointer',
        opacity: disabled ? 0.45 : 1,
        transition: 'background var(--duration-base) var(--ease-standard)',
        ...(variants[variant] || variants.secondary),
        ...style,
      }}
      {...rest}
    >
      <Icon name={icon} size={size === 'sm' ? 14 : 16} />
    </button>
  );
}
