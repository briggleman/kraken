// House formatting — mono values the way the design writes them.

const MONTHS = ["jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"];

export function fmtGb(mb: number, digits = 1): string {
  return (mb / 1024).toFixed(digits);
}

/**
 * Storage capacity from megabytes, in the readouts' voice: 512M, 420G, 7.6T.
 * Disks are whole terabytes where memory is gigabytes, so this picks the unit
 * rather than fixing one like fmtGb does.
 */
export function fmtCapacityMB(mb: number): string {
  if (mb < 1024) return Math.round(mb) + "M";
  const g = mb / 1024;
  if (g < 1024) return (g >= 100 ? Math.round(g).toString() : g.toFixed(1)) + "G";
  return (g / 1024).toFixed(1) + "T";
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

// Windows NTSTATUS codes an operator actually meets when a game server dies in
// a container. They are only recognisable in hex — 3221225781 is noise,
// 0xC0000135 is a name — and each one points at a different fix, so the hint
// says what to do, not just what the constant is called.
const EXIT_HINTS: Record<string, string> = {
  "0xC0000135": "a dll the game needs is missing from the container image (STATUS_DLL_NOT_FOUND)",
  "0xC0000139": "a dll is present but the wrong build — an export the game wants is missing (STATUS_ENTRYPOINT_NOT_FOUND)",
  "0xC0000005": "access violation — the game crashed on a bad memory access (STATUS_ACCESS_VIOLATION)",
  "0xC000007B": "bad image format — a 32/64-bit mismatch between the game and a dll (STATUS_INVALID_IMAGE_FORMAT)",
};

/** An exit code in hex the way Windows names it: 3221225781 → "0xC0000135".
 *  Codes are unsigned 32-bit, so a negative render would match no documentation
 *  anywhere. */
export function fmtExitHex(code: number): string {
  return "0x" + (code >>> 0).toString(16).toUpperCase().padStart(8, "0");
}

/** The crash notice's whole sentence: decimal and hex, plus what the code means
 *  when it is one of the handful worth naming. Linux codes stay short — 137 is
 *  a SIGKILL, which is a fact about the host, not the game. */
export function fmtExit(code: number): string {
  const hex = fmtExitHex(code);
  const line = `exit ${code} / ${hex}`;
  const hint = EXIT_HINTS[hex];
  if (hint) return `${line} — ${hint}`;
  if (code === 0) return `${line} — the process ended on its own without an error code`;
  if (code > 128 && code < 165) return `${line} — killed by signal ${code - 128}`;
  return line;
}

/** "HH:MM" for the events floor. */
export function fmtHm(iso: string): string {
  const d = new Date(iso);
  return String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0");
}
