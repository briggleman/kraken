import { useEffect, useRef, useState } from "react";
import { getToken } from "./client";

export interface ConsoleLine {
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

interface StreamFrame {
  type: "console" | "stats" | "error";
  ts?: number;
  stream?: string;
  text?: string;
  cpu_percent?: number;
  mem_used_mb?: number;
  mem_limit_mb?: number;
  net_rx_bytes?: number;
  net_tx_bytes?: number;
  uptime_seconds?: number;
  disk_used_mb?: number;
  players?: number;
  max_players?: number;
  players_known?: boolean;
  message?: string;
}

const MAX_LINES = 500;
const MAX_SAMPLES = 40;
// Reconnect backoff for a live stream, in ms. The last value repeats.
const BACKOFF_MS = [1_000, 2_000, 4_000, 8_000, 15_000];
// A handshake that neither opens nor errors within this window is abandoned and
// retried on the backoff ladder. Without it a socket wedged in CONNECTING (seen
// through a proxy that swallows a stream to an unreachable node — #104) leaves
// the hook stuck forever with no reconnect and no way out but a manual retry.
const CONNECT_TIMEOUT_MS = 10_000;
// A connection must stay open at least this long to count as "healthy" and
// reset the backoff. An accept-then-immediately-close (the Panel upgrades the
// socket, then the agent stream fails) would otherwise reset the counter every
// cycle and hammer reconnect at the 1s floor forever, flushing the console each
// pass. Below this, the close is treated as a failed attempt and backoff climbs.
const STABLE_MS = 3_000;

/**
 * How to treat this server's output.
 *
 * - `live`   — output is still arriving (starting/running/stopping). Reconnect
 *              on an unexpected close; commands are accepted.
 * - `replay` — the container is stopped or dead (offline/crashed) but Docker
 *              still holds its logs until the next start, so the stream replays
 *              the tail and then ends. An end is expected here, not a fault.
 * - `off`    — don't open a socket at all.
 */
export type StreamMode = "off" | "live" | "replay";

/** Socket lifecycle, kept distinct from StreamMode so the UI never has to infer
 *  "is output still coming?" from "is a socket open?" — for a replayed crash the
 *  socket can stay open with nothing left to send. */
export type StreamStatus = "idle" | "connecting" | "open" | "retrying" | "ended";

/**
 * Opens the Panel stream WebSocket for a server and exposes console lines, the
 * latest stats, rolling CPU/memory history (for sparklines), socket status, a
 * manual `reconnect`, and a `send` for console commands.
 */
export function useServerStream(id: string, mode: StreamMode) {
  const [lines, setLines] = useState<ConsoleLine[]>([]);
  const [stats, setStats] = useState<LiveStats | null>(null);
  const [cpuHistory, setCpuHistory] = useState<number[]>([]);
  const [memHistory, setMemHistory] = useState<number[]>([]);
  const [status, setStatus] = useState<StreamStatus>("idle");
  const [manualRetry, setManualRetry] = useState(0);
  const wsRef = useRef<WebSocket | null>(null);
  const retryRef = useRef<number | undefined>(undefined);
  const attemptRef = useRef(0);

  useEffect(() => {
    if (mode === "off") {
      setStatus("idle");
      return;
    }
    let cancelled = false;

    // A handshake watchdog runs alongside the socket; both are cleared here.
    let connectTimer: number | undefined;
    let openedAt = 0;
    const clearRetry = () => {
      if (retryRef.current !== undefined) {
        window.clearTimeout(retryRef.current);
        retryRef.current = undefined;
      }
      if (connectTimer !== undefined) {
        window.clearTimeout(connectTimer);
        connectTimer = undefined;
      }
    };

    const handleFrame = (e: MessageEvent) => {
      let f: StreamFrame;
      try {
        f = JSON.parse(e.data as string);
      } catch {
        return;
      }
      if (f.type === "console") {
        setLines((prev) => {
          const next = [...prev, { ts: f.ts ?? 0, stream: f.stream ?? "stdout", text: f.text ?? "" }];
          return next.length > MAX_LINES ? next.slice(next.length - MAX_LINES) : next;
        });
      } else if (f.type === "stats") {
        const memLimit = f.mem_limit_mb ?? 0;
        const memPct = memLimit > 0 ? ((f.mem_used_mb ?? 0) / memLimit) * 100 : 0;
        setStats({
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
        });
        setCpuHistory((prev) => {
          const next = [...prev, f.cpu_percent ?? 0];
          return next.length > MAX_SAMPLES ? next.slice(next.length - MAX_SAMPLES) : next;
        });
        setMemHistory((prev) => {
          const next = [...prev, memPct];
          return next.length > MAX_SAMPLES ? next.slice(next.length - MAX_SAMPLES) : next;
        });
      } else if (f.type === "error") {
        setLines((prev) => [...prev, { ts: Date.now(), stream: "error", text: `[panel] ${f.message ?? "stream error"}` }]);
      }
    };

    // scheduleRetry advances the backoff ladder and arms the next connect. A
    // close that came after STABLE_MS of uptime earns a fresh ladder; a quick
    // failure keeps climbing so a broken stream backs off instead of spinning.
    const scheduleRetry = () => {
      const stable = openedAt > 0 && Date.now() - openedAt >= STABLE_MS;
      if (stable) attemptRef.current = 0;
      const delay = BACKOFF_MS[Math.min(attemptRef.current, BACKOFF_MS.length - 1)];
      attemptRef.current += 1;
      setStatus("retrying");
      retryRef.current = window.setTimeout(connect, delay);
    };

    const connect = () => {
      if (cancelled) return;
      setStatus("connecting");
      openedAt = 0;
      const proto = window.location.protocol === "https:" ? "wss" : "ws";
      const token = getToken() ?? "";
      const url = `${proto}://${window.location.host}/api/v1/servers/${id}/stream/ws`;
      // Carry the session token as a WS subprotocol rather than a query param, so
      // it never lands in URLs, access logs, or browser history.
      const ws = new WebSocket(url, ["kraken.token", token]);
      wsRef.current = ws;

      // Watchdog: if the handshake doesn't complete in time, abandon this
      // socket and fall into the backoff ladder rather than hang in CONNECTING.
      connectTimer = window.setTimeout(() => {
        if (cancelled || wsRef.current !== ws) return;
        ws.onclose = null; // we own this teardown; don't double-schedule
        ws.close();
        wsRef.current = null;
        if (mode !== "live") {
          setStatus("ended");
          return;
        }
        scheduleRetry();
      }, CONNECT_TIMEOUT_MS);

      ws.onopen = () => {
        if (cancelled) return;
        if (connectTimer !== undefined) {
          window.clearTimeout(connectTimer);
          connectTimer = undefined;
        }
        openedAt = Date.now();
        // The backoff counter is NOT reset here — only after the socket proves
        // it stayed open (see scheduleRetry). An accept-then-drop must not reset
        // it, or a failing stream reconnects at the 1s floor forever.
        // Every connection asks the Panel for a tail replay, so whatever arrives
        // next is the authoritative view of the log. Drop what we were holding or
        // a reconnect (or a live→replay flip on crash) duplicates the overlap.
        setLines([]);
        setStatus("open");
      };
      ws.onerror = () => {
        // No state change here — onclose always follows and owns the transition.
      };
      ws.onclose = () => {
        if (cancelled) return;
        if (connectTimer !== undefined) {
          window.clearTimeout(connectTimer);
          connectTimer = undefined;
        }
        wsRef.current = null;
        if (mode !== "live") {
          // A replay ends when the log runs out. That is the expected finish, not
          // a fault, and retrying would just re-send the same lines forever.
          setStatus("ended");
          return;
        }
        scheduleRetry();
      };
      ws.onmessage = handleFrame;
    };

    attemptRef.current = 0;
    connect();

    return () => {
      cancelled = true;
      clearRetry();
      const ws = wsRef.current;
      wsRef.current = null;
      if (ws) {
        ws.onclose = null; // our own teardown must not schedule a reconnect
        ws.close();
      }
    };
  }, [id, mode, manualRetry]);

  const send = (command: string) => {
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: "command", command }));
    }
  };

  /** Retry now instead of waiting out the backoff. */
  const reconnect = () => setManualRetry((n) => n + 1);

  return {
    lines,
    stats,
    cpuHistory,
    memHistory,
    status,
    connected: status === "open",
    reconnect,
    send,
  };
}
