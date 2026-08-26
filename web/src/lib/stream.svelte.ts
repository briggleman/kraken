// ServerStream — the Svelte port of useServerStream: the Panel's per-server
// WebSocket carrying console lines, live stats, and a command channel. All
// reconnect discipline is verbatim from the hook: backoff ladder, handshake
// watchdog, stable-uptime reset, replay-vs-live close semantics.

import { getToken } from "@/api/client";

export interface StreamConsoleLine {
  ts: number;
  stream: string;
  text: string;
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
      this.lines.push({ ts: f.ts ?? 0, stream: f.stream ?? "stdout", text: f.text ?? "" });
      if (this.lines.length > MAX_LINES) this.lines.splice(0, this.lines.length - MAX_LINES);
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
      this.lines.push({
        ts: Date.now(),
        stream: "error",
        text: `[panel] ${f.message ?? "stream error"}`,
      });
    }
  }
}
