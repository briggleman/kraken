// Bounded mean-reverting random walks — the character of every live readout
// in the mock. A walk with hard clamping piles up against whichever bound it
// drifts into and the zone-coded meters render as a single-color block, so
// values drift toward the middle of their band instead (see DESIGN.md,
// dot-matrix history meter).

export interface WalkSpec {
  v: number; // current value (also the seed)
  lo: number;
  hi: number;
  step: number;
}

export function stepWalk(m: WalkSpec): number {
  m.v = Math.max(m.lo, Math.min(m.hi, m.v + (Math.random() * m.step - m.step / 2)));
  return m.v;
}

/** Seed a history the same way the live tick extends it, newest last. */
export function seedHistory(spec: WalkSpec, length: number): number[] {
  const m = { ...spec };
  const out: number[] = [];
  for (let i = 0; i < length; i++) out.push(stepWalk(m));
  out[out.length - 1] = spec.v; // the newest sample is the printed value
  return out;
}

export function pushSample(history: number[], v: number): void {
  history.push(v);
  history.shift();
}
