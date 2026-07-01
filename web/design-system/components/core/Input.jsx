import React from 'react';

/**
 * Form text field. Mono UPPERCASE label, recessed inset background.
 * `focused` shows the teal ring; `error` recolors border, value and helper.
 */
export function Input({
  label,
  value,
  placeholder,
  helper,
  error = false,
  focused = false,
  mono = false,
  style,
  ...rest
}) {
  const borderColor = error
    ? 'rgba(255,92,87,.5)'
    : focused
    ? 'rgba(61,245,207,.5)'
    : 'var(--border-subtle)';
  return (
    <div style={{ ...style }}>
      {label ? (
        <label
          style={{
            display: 'block',
            fontFamily: 'var(--font-mono)',
            fontSize: 12,
            letterSpacing: '.5px',
            color: 'var(--text-muted)',
            marginBottom: 7,
          }}
        >
          {label}
        </label>
      ) : null}
      <input
        value={value}
        placeholder={placeholder}
        style={{
          width: '100%',
          padding: '12px 14px',
          borderRadius: 'var(--radius-md)',
          border: `1px solid ${borderColor}`,
          background: 'rgba(3,15,20,.7)',
          color: error ? '#ff8a85' : 'var(--text-primary)',
          fontFamily: mono ? 'var(--font-mono)' : 'var(--font-sans)',
          fontSize: 14,
          outline: 'none',
          boxShadow: focused && !error ? '0 0 0 2px rgba(61,245,207,.18), 0 0 18px rgba(61,245,207,.4)' : 'none',
          transition: 'box-shadow var(--duration-base) var(--ease-standard), border-color var(--duration-base) var(--ease-standard)',
        }}
        {...rest}
      />
      {helper ? (
        <div
          style={{
            fontSize: 11,
            color: error ? 'var(--status-crashed)' : 'var(--text-faint)',
            marginTop: 6,
          }}
        >
          {helper}
        </div>
      ) : null}
    </div>
  );
}
