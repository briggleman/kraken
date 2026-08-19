/** Discrete memory-pressure bands, shared by the header's live cluster and the
 *  fleet-memory tile so the two readouts always agree: ≤50% healthy (green),
 *  ≤75% elevated (amber), above that critical (red). */
export function memColor(pct: number): string {
  if (pct <= 50) return "var(--status-running)";
  if (pct <= 75) return "var(--status-starting)";
  return "var(--status-crashed)";
}
