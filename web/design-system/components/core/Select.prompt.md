`Select` — the Abyssal dropdown. A **custom** trigger + popover listbox, not a native `<select>` (the OS-rendered option list can't be themed and breaks the deep-sea look). Recessed inset trigger like `Input`, teal focus ring, raised glass list with a tone-checked selection.

```jsx
const NODES = [
  { value: 'node-01', label: 'node-01', icon: 'linux', hint: 'eu-west · 38%' },
  { value: 'node-02', label: 'node-02', icon: 'linux', hint: 'us-east · 61%' },
  { value: 'node-04', label: 'node-04', icon: 'linux', hint: 'ap-south · 12%' },
  { value: 'node-09', label: 'node-09 (draining)', icon: 'offline', disabled: true },
];

const [node, setNode] = React.useState('node-01');
<Select label="NODE PLACEMENT" mono value={node} options={NODES} onChange={setNode} />
```

**Options** carry `value` + `label`, plus optional `icon` (leading glyph, tints teal when selected), `hint` (right-aligned mono detail — load %, app id, region), and `disabled`.

Props: `label`, `value`, `options`, `placeholder` (default "Select…"), `onChange(value, option)`, `disabled`, `error` + `helper` (validation), `mono` (IDs/nodes/ports), `size` `sm`|`md` (matches Button/Input), `defaultOpen` (specimens only).

Keyboard: ↑/↓ move · Enter/Space pick · Esc close · Home/End jump · type-ahead to match a label. Closes on outside-click and auto-flips upward when near the viewport bottom. Use `mono` whenever the values are machine-readable; keep labels short.
