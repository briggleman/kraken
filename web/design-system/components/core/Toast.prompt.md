`Toast` / `Toaster` — transient operator notifications. Mount **one** `<Toaster />` at the app root, then fire from anywhere — no provider/context to wire.

```jsx
import { Toaster } from 'AbyssalDesignSystem';

// once, at the root:
<Toaster position="bottom-right" />

// anywhere:
Toaster.success('Server started', { message: 'leviathan-01 is live' });
Toaster.error('Start failed', { message: 'node-02 unreachable', action: { label: 'Retry', onClick: retry } });
Toaster.info('Config saved', { mono: true, message: 'server.power · 2 keys changed' });
```

**Tones** (hue + glyph together, never color alone): `info` (teal · circle-i), `success` (green · circle-check), `warning` (amber · triangle), `error` (red · octagon), `loading` (teal · spinner — persists).

**Async pattern** — resolve a loading toast in place:

```jsx
const id = Toaster.loading('Starting leviathan-01…');
try {
  await start();
  Toaster.update(id, { tone: 'success', title: 'leviathan-01 is live' });
} catch (e) {
  Toaster.update(id, { tone: 'error', title: 'Start failed', message: String(e) });
}
```

Auto-dismiss after 4500ms (a thin tone-colored meter ticks it down; hovering pauses it). `duration: 0` persists. `loading` defaults to persistent. `Toaster.dismiss(id)` removes one; `Toaster.dismiss()` clears all.

Keep titles short and sentence-case; use `mono: true` for any machine-readable detail (IDs, ports, addresses, config keys). `position`: `top/bottom` × `left/center/right` (default `bottom-right`); `max` caps the visible stack (default 4).
