# DESIGN.md — Abyssal Design System

> Canonical, machine-readable design brief for the **Abyssal Design System** — the deep-sea,
> dark-first design language behind **Kraken**, a game-server control panel (the operator dashboard
> for starting, stopping, monitoring and debugging fleets of game servers: Valheim, CS2, Rust, ARK,
> Palworld…).
>
> **Stance:** Dark-first · Comfortable-dense · Functional motion · Operator-grade.
> **Vibe in one line:** a submarine control room — quiet, instrumented, every readout legible at a
> glance, nothing shouting until something is actually wrong.

Honor the tokens and rules below exactly. Do not invent new colors, fonts, or spacing values — reach
for the CSS variables. Do not introduce a second accent, a filled icon set, decorative gradients, or
emoji.

---

## 1. Project layout (in this repo)

This file lives at the repo root; the UI it governs lives under `web/`. Paths below are
**repo-root-relative**.

```
web/
  design-system/
    styles.css            ← global entry point. Imports every token file.
    tokens/
      fonts.css           ← Space Grotesk + JetBrains Mono (Google Fonts)
      colors.css          ← all color variables
      typography.css      ← families + weights
      spacing.css         ← 4px spacing scale, radius, elevation, layout
      effects.css         ← gradients, glows, blur, easing/duration, keyframes
    components/core/      ← React primitives (.jsx + .d.ts), consumed via the @ds alias
  src/                    ← the Kraken app (pages, shell, api client)
  public/                 ← favicons, PWA manifest, brand glyph (kraken-glyph-teal.png)
```

In sections 2–9 a bare token filename (`tokens/colors.css`, `components/core/`) is relative to
`web/design-system/` — the shorthand the design system uses for itself.

Components are imported as `@ds/components/core/<Name>` and styled with inline styles that read CSS
variables — no Tailwind. The brand mark is `web/public/kraken-glyph-teal.png`.

---

## 2. Color tokens (`tokens/colors.css`)

**Dark-first, layered from the void up.** Backgrounds step darker→raised; console & inputs are
*intentionally darker* than surfaces (recessed).

| Token | Hex | Use |
|---|---|---|
| `--bg-abyss` | `#02090E` | page void · deepest |
| `--bg-base` | `#04141B` | app background |
| `--bg-surface` | `#08191F` | cards · panels |
| `--bg-raised` | `#0C232B` | popovers · raised |
| `--bg-inset` | `#030D12` | console · inputs (darker than surface) |

**Accent — one bioluminescent teal. Never add a second accent.**
- `--accent #3DF5CF` (primary · brand · live · focus · links · data viz)
- `--accent-hover #5FFBE4` (hover brighten) · `--accent-deep #16C7A4` (gradient bottom · pressed)
- `--text-on-accent #022019` (text on teal fills)

**Coral — the single warm counterpoint.** `--coral #FF7A4D`, `--coral-soft #FF9D76`.

**Text — 4-step teal-tinted off-white ramp:**
`--text-primary #EAFFF7` → `--text-secondary #9CBEB9` → `--text-muted #6F928D` → `--text-faint #557E79`.

**Status / severity — status is NEVER color alone (hue + icon + label).** Six states:
- `--status-running #36E5A6` · `--status-starting #F4C152` · `--status-stopping #F4A24C`
- `--status-offline #5B7470` (fg `--status-offline-fg #9FB6B1`) · `--status-installing #38B6FF`
- `--status-crashed #FF5C57`

**Console log severity:** `--log-info` · `--log-warn` · `--log-error` · `--log-system` · `--log-time`
· `--log-text`.

**Borders (teal at low opacity):** `--border-subtle` (.14) · `--border-soft` (.10) · `--border-strong`
(.30) · `--border-danger` (coral .40).

**Accent washes:** `--accent-wash-08/12/16`, `--danger-wash`. **Selection:** `--selection-bg`.

---

## 3. Typography (`tokens/typography.css`)

**Two faces.** Hard rule: **anything machine-readable is mono** (numbers, IDs, ports, addresses,
config keys, file paths).

- `--font-sans` **Space Grotesk** — UI & prose (H2/H3/body/caption).
- `--font-mono` / `--font-display` **JetBrains Mono** — display headings, H1, console, and ALL
  machine-readable text.

Weights: `--weight-regular 400` · `--weight-medium 500` · `--weight-semibold 600` · `--weight-bold
700` · `--weight-black 800`.

Roles (set inline with literal sizes): Display 54/800/-1.5px mono · H1 34/800/-0.5px mono · H2
26/700/-0.3px grotesk · H3 19/600 grotesk · Body 15/400 line 1.6 grotesk · Caption 12/500 grotesk ·
Mono label 11px, 1.5px tracking, UPPERCASE mono · Mono body 12.5px, line 1.85 (console).

Section markers use a code-comment convention: `// YOUR FLEET`, `// CREATE SERVER` — numbered/slashed,
uppercase, teal mono.

---

## 4. Spacing, radius, elevation (`tokens/spacing.css`)

**4px base scale:** `--sp-1 4` … `--sp-16 64`.

**Radius:** `--radius-sm 8` · `--radius-md 11` · `--radius-lg 15` · `--radius-xl 20` · `--radius-pill 999`.

**Elevation:** `--elevation-e1` card · `--elevation-e2` raised · `--elevation-e3` modal ·
`--elevation-glow` / `--elevation-glow-soft` — teal glow **reserved for live & primary surfaces only**.
Cards get NO drop shadow by default — depth comes from the translucent fill over the gradient.

**Layout:** `--container-max 1240px` · `--nav-height 64px`.

---

## 5. Effects, motion & atmosphere (`tokens/effects.css`)

**Gradients:** `--gradient-accent` (teal vertical) · `--gradient-accent-bar` (horizontal) ·
`--gradient-depth` (the fixed page backdrop) · `--gradient-iris` (radial bioluminescent).

**Glows / blur:** `--glow-accent-dot`, `--glow-text`, `--blur-nav 14px`.

**Easing/duration:** `--ease-standard`, `--ease-out`; `--duration-fast 120ms` / `--duration-base
200ms` / `--duration-slow 320ms`.

**Keyframes:** `abyssalSpin` (starting), `abyssalPulseDot` (live), `abyssalBlink`, `abyssalShimmer`,
`abyssalFloat`, `abyssalIris`; plus the app's `rayDrift` for ambient god-rays.

**Motion is functional only — no bounce.** Hover brightens accent; press deepens to `--accent-deep`.
**Always respect `prefers-reduced-motion`** (handled globally in `effects.css`).

**Ambient atmosphere (full-page chrome ONLY — never behind dense data):** fixed depth gradient +
radial top-glow, drifting god-rays, a rising-particle canvas, inset fog/vignette. See
`web/src/components/AmbientBackground.tsx`.

**Surfaces & glass:** nearly every surface is a semi-transparent fill (~`rgba(7,23,29,.5)`) over the
depth gradient + a 1px teal border (12–14%), radius 13–15px. Focus/selected: teal border 30% + a 3px
teal ring. **Dashed teal borders** mark empty/placeholder/permission-disabled zones. Top nav is glass:
`backdrop-filter: blur(var(--blur-nav))` over `rgba(2,9,14,.66)` + teal bottom hairline.

---

## 6. Iconography (`Icon` component)

- **Lucide-style line icons**, 2px stroke, rounded caps/joins, 24×24 viewBox, `currentColor`.
- Icon name union lives in `Icon.d.ts`: status (running, starting, stopping, offline, installing,
  crashed), utility (check, plus, search, copy, folder, file, lock, refresh, info, octagon, chevron,
  clock, gear, x, play), platform (linux, windows, wine). `play`/`windows` are filled; the rest are stroked. `octagon` is the
  error glyph (distinct from the amber `crashed` triangle — status is hue **+** glyph, never color alone).
- **Essentially no emoji.** The lone exception is 🐙 in empty states. **Never mix in a filled or
  heavier-weight icon set.**

---

## 7. Components (`components/core/`)

React primitives, imported via `@ds/components/core/<Name>`:
`Badge, Button, Card, Icon, IconButton, Input, MetricCard, MetricBar, Select, StatusPill, Toast, Toaster, Toggle`.

- **Button** — `variant` `primary`|`secondary`|`ghost`|`danger`, `size` `sm`|`md`, `icon?` (an
  Abyssal icon-name string **or** a React node). Primary = teal gradient + glow. **Labels are bare
  imperative verbs** (`Start`, `Stop`, `Kill`, `Retry`).
- **IconButton** — `icon` (required), `size` `sm`(30px)|`md`(36px), `variant`
  `secondary`(default)|`ghost`|`accent`.
- **Badge** — `tone` `accent`|`coral`|`info`|`neutral`. Compact mono tag for platforms/slugs/attrs.
- **StatusPill** — `status` (seven states), optional `label`. Hue + icon + label; `starting` spins. Use
  anywhere server state is shown. `partial` is the node-only degraded state (amber + warning
  triangle): reachable but not doing its job, distinguished from `stopping` by glyph, not hue.
- **Card** — `glow?` (teal glow + stronger border for live/primary), `dashed?`
  (empty/placeholder/permission), `padding?` (default 22). The base translucent surface.
- **Input** — mono UPPERCASE `label`, `value`, `placeholder`, `helper`, `error`, `focused`, `mono`.
  Recessed inset bg, teal focus ring.
- **Select** — themed dropdown (custom trigger + popover listbox, **never** a native `<select>`).
  `label`, `value`, `options` (`{value, label, icon?, hint?, disabled?}`), `onChange(value, option)`,
  `placeholder`, `error`+`helper`, `mono` (IDs/nodes/ports), `size` `sm`|`md`. Full keyboard nav +
  type-ahead, auto-flips up near the viewport bottom. Matches `Input`'s recessed trigger.
- **Toggle** — `checked`, `onChange(next)`, `disabled`. Teal gradient + glow on; recessed track off.
- **MetricCard** — `label` (mono UPPERCASE), `value`, `suffix?`, `accent?`. **MetricBar** — `pct`
  0–100, thin teal meter, intended as a MetricCard child.
- **Toaster** — transient operator notifications. Mount **one** `<Toaster position="bottom-right" />`
  at the app root, then fire from anywhere via the singleton API (no provider/context):
  `Toaster.success(title, { message })`, `.error`, `.info`, `.warning`, `.loading`. Five tones (hue +
  glyph): info teal · success green · warning amber-triangle · error red-octagon · loading teal-spinner.
  Auto-dismiss 4500ms with a hover-pausable meter (`duration: 0` persists). For async work, hold the
  `loading` id and `Toaster.update(id, { tone: 'success', title })`. `message` + `mono: true` for
  IDs/addresses. **Toast** is the single card, exported for specimens.
- **Icon** — `name`, `size` (default 18), `strokeWidth` (default 2). Renders at `currentColor`.

The Kraken app screens (Fleet dashboard, server detail + live console, create-server wizard, specs,
nodes, admin) are the reference assembly for layout and density.

---

## 8. Voice & content

Operator-to-operator: **precise, calm, technical**, with a thin thread of deep-sea mythology.

- Buttons are bare verbs. System messages describe the system in third person. "You/your" only for the
  operator's own fleet (_"Your fleet"_).
- Sentence case for prose/headings; **UPPERCASE mono micro-labels** tracked 1.5–3px for section/field
  labels and metrics (`RUNNING SERVERS`, `FLEET MEMORY`); terse status words (`Running`, `Crashed`).
- **Mono is mandatory** for machine-readable text: `27015/UDP`, `app 1604030`, `server.power`,
  `203.0.113.7:27015`.
- **Errors** state problem + fix in one line: _"Must be 64 or fewer."_
- **Permissions** named in mono, referenced plainly (_"requires the `server.power` permission"_).
- **Deep-sea flavor, sparingly:** server names are sea creatures (`leviathan-01`, `nautilus-07`);
  empty states lean in (_"The deep is quiet."_). Lone 🐙 in empty states.

---

## 9. Working rules

1. **Link `web/design-system/styles.css`** for all tokens + fonts. Never duplicate token values inline —
   use the CSS variables (`var(--accent)`, `var(--sp-4)`, …).
2. **Use the components** in `components/core/` for any control they cover.
3. **Don't break the rules that make this system itself:** one teal accent only; status = hue + icon +
   label; mono for all machine-readable text; teal glow only for live/primary; functional motion only,
   no bounce, honor `prefers-reduced-motion`; Lucide line icons at `currentColor`; no emoji beyond 🐙.
4. **Ambient atmosphere is full-page chrome only** — never behind dense data tables/consoles.
5. When adding a new component: ship `Name.jsx` + `Name.d.ts` in `components/core/` and read tokens via
   CSS variables.
