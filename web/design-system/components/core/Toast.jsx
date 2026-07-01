import React from 'react';
import { Icon } from './Icon.jsx';

/* ------------------------------------------------------------------ *
 *  Tone map — hue + glyph together (never color alone).
 *  `loading` spins and persists until updated or dismissed.
 * ------------------------------------------------------------------ */
const TONES = {
  info:    { color: '#3DF5CF', icon: 'info' },
  success: { color: '#36E5A6', icon: 'running' },
  warning: { color: '#F4C152', icon: 'crashed' },
  error:   { color: '#FF5C57', icon: 'octagon' },
  loading: { color: '#3DF5CF', icon: 'starting', spin: true, persist: true },
};

const DEFAULT_DURATION = 4500;

function rgba(hex, a) {
  const n = parseInt(hex.slice(1), 16);
  return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${a})`;
}

/* ------------------------------------------------------------------ *
 *  Singleton store — module-level so `Toaster.success(...)` works from
 *  anywhere, with a single <Toaster/> mounted at the app root.
 * ------------------------------------------------------------------ */
let items = [];
let seq = 0;
const listeners = new Set();
const emit = () => listeners.forEach((l) => l(items));

function push(opts) {
  const id = opts.id != null ? opts.id : ++seq;
  const tone = opts.tone || 'info';
  const persist = TONES[tone] && TONES[tone].persist;
  const duration = opts.duration != null ? opts.duration : persist ? 0 : DEFAULT_DURATION;
  items = [...items, { ...opts, id, tone, duration, _v: 0 }];
  emit();
  return id;
}

function updateItem(id, opts) {
  let found = false;
  items = items.map((t) => {
    if (t.id !== id) return t;
    found = true;
    const next = { ...t, ...opts, leaving: false, _v: (t._v || 0) + 1 };
    if (opts.tone && opts.duration == null) {
      const persist = TONES[opts.tone] && TONES[opts.tone].persist;
      next.duration = persist ? 0 : DEFAULT_DURATION;
    }
    return next;
  });
  if (found) emit();
  else push({ ...opts, id });
}

function remove(id) {
  if (id == null) {
    items = items.map((t) => ({ ...t, leaving: true }));
    emit();
    setTimeout(() => { items = []; emit(); }, 240);
    return;
  }
  items = items.map((t) => (t.id === id ? { ...t, leaving: true } : t));
  emit();
  setTimeout(() => { items = items.filter((t) => t.id !== id); emit(); }, 240);
}

/* ------------------------------------------------------------------ *
 *  Toast — a single notification card. Owns its own auto-dismiss
 *  timer (rAF-driven so hover can pause it) and the progress meter.
 * ------------------------------------------------------------------ */
export function Toast({ toast, onDismiss, position = 'bottom-right' }) {
  const t = TONES[toast.tone] || TONES.info;
  const duration = toast.duration || 0;
  const dismissible = toast.dismissible !== false;

  const [mounted, setMounted] = React.useState(false);
  const [pct, setPct] = React.useState(100);

  const rafRef = React.useRef(0);
  const startRef = React.useRef(0);
  const leftRef = React.useRef(duration);

  // Enter animation — fire once.
  React.useEffect(() => {
    const r = requestAnimationFrame(() => setMounted(true));
    return () => cancelAnimationFrame(r);
  }, []);

  // Auto-dismiss timer, restarted whenever the toast is updated (_v).
  React.useEffect(() => {
    if (!duration) { setPct(100); return undefined; }
    leftRef.current = duration;
    setPct(100);
    const tick = (now) => {
      if (!startRef.current) startRef.current = now;
      const left = leftRef.current - (now - startRef.current);
      setPct(Math.max(0, (left / duration) * 100));
      if (left <= 0) { onDismiss(toast.id); return; }
      rafRef.current = requestAnimationFrame(tick);
    };
    startRef.current = 0;
    rafRef.current = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(rafRef.current);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [toast._v, duration]);

  const pause = () => {
    if (!duration) return;
    cancelAnimationFrame(rafRef.current);
    if (startRef.current) leftRef.current -= performance.now() - startRef.current;
  };
  const resume = () => {
    if (!duration || toast.leaving) return;
    startRef.current = 0;
    rafRef.current = requestAnimationFrame(function tick(now) {
      if (!startRef.current) startRef.current = now;
      const left = leftRef.current - (now - startRef.current);
      setPct(Math.max(0, (left / duration) * 100));
      if (left <= 0) { onDismiss(toast.id); return; }
      rafRef.current = requestAnimationFrame(tick);
    });
  };

  const dx = position.includes('right') ? 24 : position.includes('left') ? -24 : 0;
  const dy = dx === 0 ? (position.includes('top') ? -16 : 16) : 0;
  const hidden = !mounted || toast.leaving;

  return (
    <div
      onMouseEnter={pause}
      onMouseLeave={resume}
      style={{
        pointerEvents: 'auto',
        position: 'relative',
        overflow: 'hidden',
        boxSizing: 'border-box',
        display: 'flex',
        gap: 12,
        padding: '13px 14px 14px',
        borderRadius: 'var(--radius-lg)',
        border: `1px solid ${rgba(t.color, toast.leaving ? 0.18 : 0.28)}`,
        background: 'rgba(8,25,31,.86)',
        backdropFilter: 'blur(14px)',
        WebkitBackdropFilter: 'blur(14px)',
        boxShadow: `var(--elevation-e3), 0 0 22px ${rgba(t.color, 0.13)}`,
        color: 'var(--text-primary)',
        maxHeight: hidden ? 0 : 320,
        marginBottom: hidden ? 0 : 0,
        paddingTop: hidden ? 0 : 13,
        paddingBottom: hidden ? 0 : 14,
        opacity: hidden ? 0 : 1,
        transform: hidden ? `translate(${dx}px, ${dy}px) scale(.97)` : 'translate(0,0) scale(1)',
        transition:
          'opacity 220ms var(--ease-out), transform 240ms var(--ease-out), max-height 240ms var(--ease-standard), padding 240ms var(--ease-standard)',
      }}
    >
      {/* tone icon chip */}
      <div
        style={{
          flex: 'none',
          width: 28,
          height: 28,
          borderRadius: 'var(--radius-sm)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          background: rgba(t.color, 0.12),
          border: `1px solid ${rgba(t.color, 0.3)}`,
          color: t.color,
        }}
      >
        <Icon
          name={t.icon}
          size={17}
          strokeWidth={2.1}
          style={t.spin ? { animation: 'abyssalSpin 1.4s linear infinite' } : undefined}
        />
      </div>

      {/* body */}
      <div style={{ flex: 1, minWidth: 0 }}>
        <div
          style={{
            fontFamily: 'var(--font-sans)',
            fontSize: 14,
            fontWeight: 'var(--weight-semibold)',
            lineHeight: 1.3,
            color: 'var(--text-primary)',
          }}
        >
          {toast.title}
        </div>
        {toast.message != null && toast.message !== '' ? (
          <div
            style={{
              marginTop: 3,
              fontFamily: toast.mono ? 'var(--font-mono)' : 'var(--font-sans)',
              fontSize: toast.mono ? 12 : 13,
              lineHeight: 1.5,
              color: 'var(--text-secondary)',
              letterSpacing: toast.mono ? '.2px' : 0,
              wordBreak: 'break-word',
            }}
          >
            {toast.message}
          </div>
        ) : null}
        {toast.action ? (
          <button
            onClick={() => {
              toast.action.onClick();
              onDismiss(toast.id);
            }}
            style={{
              marginTop: 10,
              padding: '5px 11px',
              borderRadius: 'var(--radius-sm)',
              border: `1px solid ${rgba(t.color, 0.35)}`,
              background: rgba(t.color, 0.1),
              color: t.color,
              fontFamily: 'var(--font-sans)',
              fontSize: 12.5,
              fontWeight: 'var(--weight-semibold)',
              cursor: 'pointer',
              transition: 'background var(--duration-base) var(--ease-standard)',
            }}
          >
            {toast.action.label}
          </button>
        ) : null}
      </div>

      {/* dismiss */}
      {dismissible ? (
        <button
          aria-label="Dismiss"
          onClick={() => onDismiss(toast.id)}
          style={{
            flex: 'none',
            width: 24,
            height: 24,
            margin: '-2px -2px 0 0',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            borderRadius: 'var(--radius-sm)',
            border: '1px solid transparent',
            background: 'transparent',
            color: 'var(--text-faint)',
            cursor: 'pointer',
            transition: 'color var(--duration-fast) var(--ease-standard), background var(--duration-fast) var(--ease-standard)',
          }}
          onMouseEnter={(e) => {
            e.currentTarget.style.color = 'var(--text-secondary)';
            e.currentTarget.style.background = 'rgba(61,245,207,.08)';
          }}
          onMouseLeave={(e) => {
            e.currentTarget.style.color = 'var(--text-faint)';
            e.currentTarget.style.background = 'transparent';
          }}
        >
          <Icon name="x" size={14} />
        </button>
      ) : null}

      {/* auto-dismiss progress meter */}
      {duration ? (
        <div
          style={{
            position: 'absolute',
            left: 0,
            bottom: 0,
            height: 2,
            width: `${pct}%`,
            background: t.color,
            opacity: 0.85,
            borderTopRightRadius: 2,
          }}
        />
      ) : null}
    </div>
  );
}

const POSITIONS = {
  'top-left': { top: 20, left: 20 },
  'top-center': { top: 20, left: '50%', transform: 'translateX(-50%)' },
  'top-right': { top: 20, right: 20 },
  'bottom-left': { bottom: 20, left: 20 },
  'bottom-center': { bottom: 20, left: '50%', transform: 'translateX(-50%)' },
  'bottom-right': { bottom: 20, right: 20 },
};

/* ------------------------------------------------------------------ *
 *  Toaster — mount ONCE at the app root. Trigger from anywhere via the
 *  attached methods: Toaster.success('Saved'), Toaster.error(...), etc.
 * ------------------------------------------------------------------ */
export function Toaster({ position = 'bottom-right', max = 4, gap = 12 } = {}) {
  const [list, setList] = React.useState(items);
  React.useEffect(() => {
    listeners.add(setList);
    setList(items);
    return () => listeners.delete(setList);
  }, []);

  const isBottom = position.includes('bottom');
  const visible = list.slice(-max);

  return (
    <div
      style={{
        position: 'fixed',
        zIndex: 9999,
        display: 'flex',
        flexDirection: isBottom ? 'column-reverse' : 'column',
        gap,
        width: 'min(390px, calc(100vw - 32px))',
        pointerEvents: 'none',
        ...POSITIONS[position],
      }}
    >
      {visible.map((toast) => (
        <Toast key={toast.id} toast={toast} position={position} onDismiss={remove} />
      ))}
    </div>
  );
}

// Imperative API — rides on the capitalized `Toaster` export, so it is
// reachable as window.<Namespace>.Toaster.success(...) in plain HTML too.
Toaster.show = (opts) => push(opts);
Toaster.info = (title, opts) => push({ ...opts, title, tone: 'info' });
Toaster.success = (title, opts) => push({ ...opts, title, tone: 'success' });
Toaster.warning = (title, opts) => push({ ...opts, title, tone: 'warning' });
Toaster.error = (title, opts) => push({ ...opts, title, tone: 'error' });
Toaster.loading = (title, opts) => push({ ...opts, title, tone: 'loading' });
Toaster.update = (id, opts) => updateItem(id, opts);
Toaster.dismiss = (id) => remove(id);
