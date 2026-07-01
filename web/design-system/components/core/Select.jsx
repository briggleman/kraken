import React from 'react';
import { Icon } from './Icon.jsx';

const SIZES = {
  sm: { padding: '9px 12px', fontSize: 13, chevron: 14, optPad: '8px 11px' },
  md: { padding: '12px 14px', fontSize: 14, chevron: 16, optPad: '10px 12px' },
};

/**
 * Themed dropdown — a custom trigger + popover listbox (NOT a native
 * <select>, whose option list is OS-rendered and can't be themed). Recessed
 * inset trigger like Input, teal focus ring, raised glass list with a
 * tone-checked selection. Keyboard: ↑/↓ move, Enter/Space pick, Esc close,
 * Home/End jump, type-ahead to match. Options may carry an icon and a hint.
 */
export function Select({
  label,
  value = null,
  options = [],
  placeholder = 'Select…',
  onChange,
  disabled = false,
  error = false,
  helper,
  mono = false,
  size = 'md',
  defaultOpen = false,
  style,
  ...rest
}) {
  const s = SIZES[size] || SIZES.md;
  const [open, setOpen] = React.useState(defaultOpen);
  const [active, setActive] = React.useState(-1);
  const [flip, setFlip] = React.useState(false);
  const rootRef = React.useRef(null);
  const listRef = React.useRef(null);
  const typeRef = React.useRef({ str: '', t: 0 });

  const selected = options.find((o) => o.value === value) || null;
  const selectableIndex = (from, dir) => {
    let i = from;
    for (let n = 0; n < options.length; n++) {
      i = (i + dir + options.length) % options.length;
      if (!options[i].disabled) return i;
    }
    return -1;
  };

  // Close on outside click.
  React.useEffect(() => {
    if (!open) return undefined;
    const onDown = (e) => {
      if (rootRef.current && !rootRef.current.contains(e.target)) setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  }, [open]);

  // On open: seed active to the selected row and decide flip direction.
  React.useEffect(() => {
    if (!open) return;
    const idx = options.findIndex((o) => o.value === value);
    setActive(idx >= 0 ? idx : selectableIndex(-1, 1));
    const r = rootRef.current && rootRef.current.getBoundingClientRect();
    if (r) setFlip(window.innerHeight - r.bottom < 260 && r.top > 260);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  // Keep the active row scrolled into view inside the list only.
  React.useEffect(() => {
    if (!open || active < 0 || !listRef.current) return;
    const row = listRef.current.children[active];
    if (!row) return;
    const lr = listRef.current;
    if (row.offsetTop < lr.scrollTop) lr.scrollTop = row.offsetTop;
    else if (row.offsetTop + row.offsetHeight > lr.scrollTop + lr.clientHeight)
      lr.scrollTop = row.offsetTop + row.offsetHeight - lr.clientHeight;
  }, [active, open]);

  const choose = (opt) => {
    if (!opt || opt.disabled) return;
    onChange && onChange(opt.value, opt);
    setOpen(false);
  };

  const onKey = (e) => {
    if (disabled) return;
    if (!open) {
      if (['Enter', ' ', 'ArrowDown', 'ArrowUp'].includes(e.key)) {
        e.preventDefault();
        setOpen(true);
      }
      return;
    }
    switch (e.key) {
      case 'ArrowDown': e.preventDefault(); setActive((i) => selectableIndex(i, 1)); break;
      case 'ArrowUp': e.preventDefault(); setActive((i) => selectableIndex(i, -1)); break;
      case 'Home': e.preventDefault(); setActive(selectableIndex(-1, 1)); break;
      case 'End': e.preventDefault(); setActive(selectableIndex(0, -1)); break;
      case 'Enter': case ' ': e.preventDefault(); choose(options[active]); break;
      case 'Escape': e.preventDefault(); setOpen(false); break;
      case 'Tab': setOpen(false); break;
      default:
        if (e.key.length === 1) {
          const now = Date.now();
          const str = (now - typeRef.current.t < 600 ? typeRef.current.str : '') + e.key.toLowerCase();
          typeRef.current = { str, t: now };
          const hit = options.findIndex((o) => !o.disabled && String(o.label).toLowerCase().startsWith(str));
          if (hit >= 0) setActive(hit);
        }
    }
  };

  const borderColor = error
    ? 'rgba(255,92,87,.5)'
    : open
    ? 'rgba(61,245,207,.5)'
    : 'var(--border-subtle)';

  return (
    <div ref={rootRef} style={{ position: 'relative', ...style }}>
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

      {/* trigger */}
      <button
        type="button"
        role="combobox"
        aria-expanded={open}
        aria-haspopup="listbox"
        disabled={disabled}
        onClick={() => !disabled && setOpen((o) => !o)}
        onKeyDown={onKey}
        style={{
          width: '100%',
          display: 'flex',
          alignItems: 'center',
          gap: 10,
          padding: s.padding,
          borderRadius: 'var(--radius-md)',
          border: `1px solid ${borderColor}`,
          background: disabled ? 'rgba(7,23,29,.4)' : 'rgba(3,15,20,.7)',
          color: selected ? (error ? '#ff8a85' : 'var(--text-primary)') : 'var(--text-faint)',
          fontFamily: mono && selected ? 'var(--font-mono)' : 'var(--font-sans)',
          fontSize: s.fontSize,
          textAlign: 'left',
          cursor: disabled ? 'not-allowed' : 'pointer',
          outline: 'none',
          boxShadow: open && !error ? '0 0 0 3px rgba(61,245,207,.1)' : 'none',
          transition: 'border-color var(--duration-base) var(--ease-standard), box-shadow var(--duration-base) var(--ease-standard)',
        }}
        {...rest}
      >
        {selected && selected.icon ? (
          <Icon name={selected.icon} size={16} style={{ color: 'var(--accent)', flex: 'none' }} />
        ) : null}
        <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {selected ? selected.label : placeholder}
        </span>
        <Icon
          name="chevron"
          size={s.chevron}
          style={{
            flex: 'none',
            color: open ? 'var(--accent)' : 'var(--text-muted)',
            transform: open ? 'rotate(180deg)' : 'rotate(0deg)',
            transition: 'transform var(--duration-base) var(--ease-out), color var(--duration-base) var(--ease-standard)',
          }}
        />
      </button>

      {/* popover listbox */}
      {open ? (
        <ul
          ref={listRef}
          role="listbox"
          style={{
            listStyle: 'none',
            margin: 0,
            padding: 5,
            position: 'absolute',
            left: 0,
            right: 0,
            zIndex: 1000,
            [flip ? 'bottom' : 'top']: 'calc(100% + 6px)',
            maxHeight: 248,
            overflowY: 'auto',
            scrollbarWidth: 'thin',
            scrollbarColor: 'rgba(61,245,207,.25) transparent',
            borderRadius: 'var(--radius-lg)',
            border: '1px solid var(--border-strong)',
            background: 'rgba(12,35,43,.92)',
            backdropFilter: 'blur(14px)',
            WebkitBackdropFilter: 'blur(14px)',
            boxShadow: 'var(--elevation-e3), 0 0 26px rgba(61,245,207,.1)',
            animation: 'abyssalSelectIn 150ms var(--ease-out)',
          }}
        >
          {options.length === 0 ? (
            <li style={{ padding: s.optPad, fontSize: 13, color: 'var(--text-faint)', fontFamily: 'var(--font-sans)' }}>
              No options
            </li>
          ) : null}
          {options.map((opt, i) => {
            const isSel = opt.value === value;
            const isActive = i === active;
            return (
              <li
                key={opt.value}
                role="option"
                aria-selected={isSel}
                aria-disabled={opt.disabled || undefined}
                onMouseEnter={() => !opt.disabled && setActive(i)}
                onClick={() => choose(opt)}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 10,
                  padding: s.optPad,
                  borderRadius: 'var(--radius-sm)',
                  cursor: opt.disabled ? 'not-allowed' : 'pointer',
                  opacity: opt.disabled ? 0.45 : 1,
                  background: isActive && !opt.disabled ? 'var(--accent-wash-12)' : isSel ? 'var(--accent-wash-08)' : 'transparent',
                  color: isSel ? 'var(--accent-hover)' : 'var(--text-secondary)',
                  transition: 'background var(--duration-fast) var(--ease-standard)',
                }}
              >
                {opt.icon ? (
                  <Icon name={opt.icon} size={16} style={{ flex: 'none', color: isSel ? 'var(--accent)' : 'var(--text-muted)' }} />
                ) : null}
                <span
                  style={{
                    flex: 1,
                    minWidth: 0,
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                    fontFamily: mono ? 'var(--font-mono)' : 'var(--font-sans)',
                    fontSize: mono ? s.fontSize - 1 : s.fontSize,
                    fontWeight: isSel ? 'var(--weight-medium)' : 'var(--weight-regular)',
                  }}
                >
                  {opt.label}
                </span>
                {opt.hint != null ? (
                  <span style={{ flex: 'none', fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-faint)', letterSpacing: '.3px' }}>
                    {opt.hint}
                  </span>
                ) : null}
                {isSel ? <Icon name="check" size={15} style={{ flex: 'none', color: 'var(--accent)' }} /> : null}
              </li>
            );
          })}
        </ul>
      ) : null}

      {helper ? (
        <div style={{ fontSize: 11, color: error ? 'var(--status-crashed)' : 'var(--text-faint)', marginTop: 6 }}>
          {helper}
        </div>
      ) : null}
    </div>
  );
}
