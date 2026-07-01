import React from 'react';

export type ToastTone = 'info' | 'success' | 'warning' | 'error' | 'loading';

export type ToastPosition =
  | 'top-left' | 'top-center' | 'top-right'
  | 'bottom-left' | 'bottom-center' | 'bottom-right';

export interface ToastAction {
  /** Bare-verb label, e.g. "Retry", "View log". */
  label: string;
  onClick: () => void;
}

export interface ToastOptions {
  /** One-line headline (sentence case). */
  title: string;
  /** Optional detail. Set `mono` for IDs / addresses / config. */
  message?: React.ReactNode;
  /** Hue + glyph. Default `info`. `loading` spins and persists. */
  tone?: ToastTone;
  /** Auto-dismiss after N ms. `0` persists until dismissed/updated.
   *  Default 4500 (0 for `loading`). */
  duration?: number;
  /** Render `message` in JetBrains Mono — for machine-readable text. */
  mono?: boolean;
  /** Optional single action button. */
  action?: ToastAction;
  /** Show the close button. Default true. */
  dismissible?: boolean;
}

export interface ToastData extends ToastOptions {
  id: number;
  tone: ToastTone;
  duration: number;
  leaving?: boolean;
  _v?: number;
}

export interface ToastProps {
  toast: ToastData;
  onDismiss: (id: number) => void;
  position?: ToastPosition;
}

/** A single notification card. Normally rendered by `Toaster`; exported
 *  for static specimens. Owns its hover-pausable auto-dismiss timer. */
export function Toast(props: ToastProps): JSX.Element;

export interface ToasterProps {
  /** Anchor. Default `bottom-right`. */
  position?: ToastPosition;
  /** Max simultaneously visible. Default 4. */
  max?: number;
  /** Gap between stacked toasts in px. Default 12. */
  gap?: number;
}

/**
 * Toast viewport — mount ONCE at the app root. Fire from anywhere via the
 * attached methods (no context/provider needed):
 *
 *   Toaster.success('Server started', { message: 'leviathan-01 is live' });
 *   const id = Toaster.loading('Starting leviathan-01…');
 *   Toaster.update(id, { tone: 'success', title: 'leviathan-01 is live' });
 */
export function Toaster(props?: ToasterProps): JSX.Element;
export namespace Toaster {
  /** Push a fully-specified toast. Returns its id. */
  function show(opts: ToastOptions): number;
  function info(title: string, opts?: Partial<ToastOptions>): number;
  function success(title: string, opts?: Partial<ToastOptions>): number;
  function warning(title: string, opts?: Partial<ToastOptions>): number;
  function error(title: string, opts?: Partial<ToastOptions>): number;
  /** Persistent spinner toast — pair with `update` to resolve it. */
  function loading(title: string, opts?: Partial<ToastOptions>): number;
  /** Mutate an existing toast in place (e.g. loading → success). */
  function update(id: number, opts: Partial<ToastOptions>): void;
  /** Dismiss one toast, or all when `id` is omitted. */
  function dismiss(id?: number): void;
}
