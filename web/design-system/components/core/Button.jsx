import React from 'react';
import { Icon } from './Icon.jsx';

const SIZES = {
  sm: { padding: '8px 14px', fontSize: 13, iconSize: 13, gap: 6 },
  md: { padding: '11px 20px', fontSize: 14, iconSize: 14, gap: 7 },
};

function variantStyle(variant, disabled) {
  if (disabled) {
    return {
      border: '1px solid var(--border-soft)',
      background: 'rgba(7,23,29,.4)',
      color: '#456b66',
      boxShadow: 'none',
      cursor: 'not-allowed',
    };
  }
  switch (variant) {
    case 'primary':
      return {
        border: '1px solid transparent',
        background: 'var(--gradient-accent)',
        color: 'var(--text-on-accent)',
        fontWeight: 'var(--weight-bold)',
        boxShadow: 'var(--elevation-glow-soft)',
      };
    case 'secondary':
      return {
        border: '1px solid var(--border-strong)',
        background: 'rgba(7,26,33,.5)',
        color: '#dff7f1',
        fontWeight: 'var(--weight-semibold)',
      };
    case 'ghost':
      return {
        border: '1px solid transparent',
        background: 'transparent',
        color: 'var(--text-secondary)',
        fontWeight: 'var(--weight-semibold)',
      };
    case 'danger':
      return {
        border: '1px solid var(--border-danger)',
        background: 'rgba(40,12,11,.5)',
        color: '#ff8a85',
        fontWeight: 'var(--weight-semibold)',
      };
    default:
      return {};
  }
}

/**
 * Abyssal button. Primary is the bioluminescent gradient with glow;
 * secondary/ghost/danger for the rest. Bare-verb labels (Start, Stop, Kill).
 * `icon` accepts an Abyssal icon name (string) or any React node.
 */
export function Button({
  children,
  variant = 'primary',
  size = 'md',
  icon,
  disabled = false,
  type = 'button',
  style,
  ...rest
}) {
  const s = SIZES[size] || SIZES.md;
  const renderedIcon =
    icon == null ? null : typeof icon === 'string' ? <Icon name={icon} size={s.iconSize} /> : icon;
  return (
    <button
      type={type}
      disabled={disabled}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        gap: s.gap,
        padding: s.padding,
        fontFamily: 'var(--font-sans)',
        fontSize: s.fontSize,
        lineHeight: 1,
        borderRadius: 'var(--radius-md)',
        cursor: disabled ? 'not-allowed' : 'pointer',
        transition:
          'background var(--duration-base) var(--ease-standard), box-shadow var(--duration-base) var(--ease-standard), filter var(--duration-fast) var(--ease-standard)',
        whiteSpace: 'nowrap',
        ...variantStyle(variant, disabled),
        ...style,
      }}
      {...rest}
    >
      {renderedIcon}
      {children}
    </button>
  );
}
