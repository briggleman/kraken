// ServerStream — the Svelte port of useServerStream: the Panel's per-server
// WebSocket carrying console lines, live stats, and a command channel. All
// reconnect discipline is verbatim from the hook: backoff ladder, handshake
// watchdog, stable-uptime reset, replay-vs-live close semantics.

import { getToken } from "@/api/client";

export interface StreamConsoleLine {
  /** Stable identity for the console's keyed `{#each}`. The buffer is a ring —
   *  array indices shift on every eviction, so keying by index makes one new
   *  line rewrite all MAX_LINES rows (#279). */
  seq: number;
  ts: number;
  stream: string;
  /** What the console renders: clamped to MAX_LINE_CHARS, or a marker when the
   *  line was a PowerShell CLIXML record. */
  text: string;
  /** Characters `text` leaves out; 0 when the line is rendered whole. */
  hidden: number;
  /** The untouched line, kept only when `text` is not the whole of it, so the
   *  console can still hand the operator the real thing on copy. */
  full?: string;
}

export interface LiveStats {
  ts: number;
  cpu_percent: number;
  mem_used_mb: number;
  mem_limit_mb: number;
  net_rx_bytes: number;
  net_tx_bytes: number;
  uptime_seconds: number;
  disk_used_mb: number;
  players: number;
  max_players: number;
  players_known: boolean;
}

interface StreamFrame extends Partial<LiveStats> {
  type: "console" | "stats" | "error";
  stream?: string;
  text?: string;
  message?: string;
}

const MAX_LINES = 500;
const MAX_SAMPLES = 40;
// A single console line renders only this far (#279). A Windows container's
// installer can emit multi-KB records with no newline in them; laying one out
// costs far more than the buffer's whole nominal 500 lines, and dozens arrive
// in a row. The remainder is kept on the line for copy, never for layout.
const MAX_LINE_CHARS = 2_000;
// PowerShell writes its progress bars and error records as CLIXML — a
// `#< CLIXML` header followed by an <Objs …> blob, all on one line. They carry
// nothing an operator can act on, so they collapse to a marker rather than
// eating 2 KB of the pane each. Detection looks at the head of the line only.
const CLIXML_HEAD_CHARS = 32;
const CLIXML_RE = /^\s*(?:#<\s*CLIXML|<Objs\b)/;
// Reconnect backoff for a live stream, in ms. The last value repeats.
const BACKOFF_MS = [1_000, 2_000, 4_000, 8_000, 15_000];
// A handshake that neither opens nor errors within this window is abandoned
// and retried on the backoff ladder (a socket wedged in CONNECTING, #104).
const CONNECT_TIMEOUT_MS = 10_000;
// A connection must stay open at least this long to count as "healthy" and
// reset the backoff; an accept-then-immediately-close keeps climbing instead.
const STABLE_MS = 3_000;

/** live — reconnect on unexpected close, commands accepted. replay — the
 *  stream tails a stopped container's logs and an end is expected. off — no
 *  socket at all. */
export type StreamMode = "off" | "live" | "replay";

export type StreamStatus = "idle" | "connecting" | "open" | "retrying" | "ended";

/** Reduce one raw console line to what the pane should render, keeping the
 *  original alongside it when the two differ. O(1) in the line's length apart
 *  from the one slice that produces the clamped prefix. */
function renderable(raw: string): { text: string; hidden: number; full?: string } {
  const head = raw.length > CLIXML_HEAD_CHARS ? raw.slice(0, CLIXML_HEAD_CHARS) : raw;
  if (CLIXML_RE.test(head)) {
    return {
      text: `[powershell clixml record — ${raw.length.toLocaleString()} chars]`,
      hidden: raw.length,
      full: raw,
    };
  }
  if (raw.length <= MAX_LINE_CHARS) return { text: raw, hidden: 0 };
  return { text: raw.slice(0, MAX_LINE_CHARS), hidden: raw.length - MAX_LINE_CHARS, full: raw };
}

export class ServerStream {
  lines = $state<StreamConsoleLine[]>([]);
  stats = $state<LiveStats | null>(null);
  cpuHistory = $state<number[]>([]);
  memHistory = $state<number[]>([]);
  status = $state<StreamStatus>("idle");

  #id = "";
  #mode: StreamMode = "off";
  #ws: WebSocket | null = null;
  #retryTimer: ReturnType<typeof setTimeout> | undefined;
  #connectTimer: ReturnType<typeof setTimeout> | undefined;
  #attempt = 0;
  #openedAt = 0;
  #seq = 0;

  /** Append one console line to the ring buffer. Clamping happens here rather
   *  than at render time so the cap covers replayed scrollback too, and so a
   *  line costs one slice once instead of a layout pass per repaint. */
  #say(ts: number, stream: string, raw: string) {
    const { text, hidden, full } = renderable(raw);
    this.lines.push({ seq: this.#seq++, ts, stream, text, hidden, full });
    if (this.lines.length > MAX_LINES) this.lines.splice(0, this.lines.length - MAX_LINES);
  }

  get connected() {
    return this.status === "open";
  }

  /** (Re)target the stream; a change tears down and reconnects. */
  set(id: string, mode: StreamMode) {
    if (id === this.#id && mode === this.#mode) return;
    this.#teardown();
    this.#id = id;
    this.#mode = mode;
    this.lines = [];
    this.stats = null;
    this.cpuHistory = [];
    this.memHistory = [];
    if (mode === "off" || !id) {
      this.status = "idle";
      return;
    }
    this.#attempt = 0;
    this.#connect();
  }

  /** Retry now instead of waiting out the backoff. */
  reconnect() {
    if (this.#mode === "off") return;
    this.#teardown();
    this.#attempt = 0;
    this.#connect();
  }

  send(command: string) {
    if (this.#ws && this.#ws.readyState === WebSocket.OPEN) {
      this.#ws.send(JSON.stringify({ type: "command", command }));
    }
  }

  destroy() {
    this.#teardown();
    this.status = "idle";
  }

  #teardown() {
    clearTimeout(this.#retryTimer);
    clearTimeout(this.#connectTimer);
    this.#retryTimer = undefined;
    this.#connectTimer = undefined;
    const ws = this.#ws;
    this.#ws = null;
    if (ws) {
      ws.onclose = null; // our own teardown must not schedule a reconnect
      ws.close();
    }
  }

  #scheduleRetry() {
    // A close after STABLE_MS of uptime earns a fresh ladder; a quick failure
    // keeps climbing so a broken stream backs off instead of spinning.
    const stable = this.#openedAt > 0 && Date.now() - this.#openedAt >= STABLE_MS;
    if (stable) this.#attempt = 0;
    const delay = BACKOFF_MS[Math.min(this.#attempt, BACKOFF_MS.length - 1)];
    this.#attempt += 1;
    this.status = "retrying";
    this.#retryTimer = setTimeout(() => this.#connect(), delay);
  }

  #connect() {
    this.status = "connecting";
    this.#openedAt = 0;
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const token = getToken() ?? "";
    const url = `${proto}://${location.host}/api/v1/servers/${this.#id}/stream/ws`;
    // The session token rides as a WS subprotocol rather than a query param,
    // so it never lands in URLs, access logs, or browser history.
    const ws = new WebSocket(url, ["kraken.token", token]);
    this.#ws = ws;

    this.#connectTimer = setTimeout(() => {
      if (this.#ws !== ws) return;
      ws.onclose = null;
      ws.close();
      this.#ws = null;
      if (this.#mode !== "live") {
        this.status = "ended";
        return;
      }
      this.#scheduleRetry();
    }, CONNECT_TIMEOUT_MS);

    ws.onopen = () => {
      clearTimeout(this.#connectTimer);
      this.#connectTimer = undefined;
      this.#openedAt = Date.now();
      // Backoff is NOT reset here — only after the socket proves it stayed
      // open. Every connection replays the tail, so drop what we were holding
      // or a reconnect duplicates the overlap.
      this.lines = [];
      this.status = "open";
    };
    ws.onerror = () => {
      // onclose always follows and owns the transition
    };
    ws.onclose = () => {
      clearTimeout(this.#connectTimer);
      this.#connectTimer = undefined;
      this.#ws = null;
      if (this.#mode !== "live") {
        // A replay ends when the log runs out — expected, not a fault.
        this.status = "ended";
        return;
      }
      this.#scheduleRetry();
    };
    ws.onmessage = (e) => this.#frame(e);
  }

  #frame(e: MessageEvent) {
    let f: StreamFrame;
    try {
      f = JSON.parse(e.data as string);
    } catch {
      return;
    }
    if (f.type === "console") {
      this.#say(f.ts ?? 0, f.stream ?? "stdout", f.text ?? "");
    } else if (f.type === "stats") {
      const memLimit = f.mem_limit_mb ?? 0;
      const memPct = memLimit > 0 ? ((f.mem_used_mb ?? 0) / memLimit) * 100 : 0;
      this.stats = {
        ts: f.ts ?? 0,
        cpu_percent: f.cpu_percent ?? 0,
        mem_used_mb: f.mem_used_mb ?? 0,
        mem_limit_mb: memLimit,
        net_rx_bytes: f.net_rx_bytes ?? 0,
        net_tx_bytes: f.net_tx_bytes ?? 0,
        uptime_seconds: f.uptime_seconds ?? 0,
        disk_used_mb: f.disk_used_mb ?? 0,
        players: f.players ?? 0,
        max_players: f.max_players ?? 0,
        players_known: f.players_known ?? false,
      };
      this.cpuHistory.push(f.cpu_percent ?? 0);
      if (this.cpuHistory.length > MAX_SAMPLES) this.cpuHistory.shift();
      this.memHistory.push(memPct);
      if (this.memHistory.length > MAX_SAMPLES) this.memHistory.shift();
    } else if (f.type === "error") {
      this.#say(Date.now(), "error", `[panel] ${f.message ?? "stream error"}`);
    }
  }
}
