// House formatting — mono values the way the design writes them.

const MONTHS = ["jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"];

export function fmtGb(mb: number, digits = 1): string {
  return (mb / 1024).toFixed(digits);
}

/** File sizes the way the mock's files list writes them: 0.3K, 4.2K, 218K, 1.1M, 4.8G. */
export function fmtSize(bytes: number): string {
  if (bytes < 1024) return (bytes / 1024).toFixed(1) + "K";
  const k = bytes / 1024;
  if (k < 1000) return (k >= 100 ? Math.round(k).toString() : k.toFixed(1)) + "K";
  const m = k / 1024;
  if (m < 1000) return (m >= 100 ? Math.round(m).toString() : m.toFixed(1)) + "M";
  return (m / 1024).toFixed(1) + "G";
}

/** "today 23:30" / "aug 22 03:11" — the mock's timestamp voice. */
export function fmtWhen(ms: number): string {
  if (!ms) return "—";
  const d = new Date(ms);
  const now = new Date();
  const hm =
    String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0");
  if (d.toDateString() === now.toDateString()) return "today " + hm;
  return MONTHS[d.getMonth()] + " " + String(d.getDate()).padStart(2, "0") + " " + hm;
}

/** "3d 14h" / "11h 02m" / "6m" — uptime the way the cards write it. */
export function fmtUptime(seconds: number): string {
  if (!seconds || seconds < 0) return "—";
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d}d ${String(h).padStart(2, "0")}h`;
  if (h > 0) return `${h}h ${String(m).padStart(2, "0")}m`;
  return `${m}m`;
}

export function fmtClock(tsMs: number): string {
  return new Date(tsMs).toLocaleTimeString("en-US", { hour12: false });
}

/** "HH:MM" for the events floor. */
export function fmtHm(iso: string): string {
  const d = new Date(iso);
  return String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0");
}
