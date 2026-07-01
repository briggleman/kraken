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

/**
 * Opens the Panel stream WebSocket for a server and exposes live console lines,
 * the latest stats, a rolling CPU history (for sparklines), connection status,
 * and a `send` for console commands. Reconnects when `enabled` flips.
 */
export function useServerStream(id: string, enabled: boolean) {
  const [lines, setLines] = useState<ConsoleLine[]>([]);
  const [stats, setStats] = useState<LiveStats | null>(null);
  const [cpuHistory, setCpuHistory] = useState<number[]>([]);
  const [memHistory, setMemHistory] = useState<number[]>([]);
  const [connected, setConnected] = useState(false);
  const wsRef = useRef<WebSocket | null>(null);

  useEffect(() => {
    if (!enabled) return;
    const proto = window.location.protocol === "https:" ? "wss" : "ws";
    const token = getToken() ?? "";
    const url = `${proto}://${window.location.host}/api/v1/servers/${id}/stream/ws`;
    // Carry the session token as a WS subprotocol rather than a query param, so
    // it never lands in URLs, access logs, or browser history.
    const ws = new WebSocket(url, ["kraken.token", token]);
    wsRef.current = ws;

    ws.onopen = () => setConnected(true);
    ws.onclose = () => setConnected(false);
    ws.onerror = () => setConnected(false);
    ws.onmessage = (e) => {
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

    return () => {
      ws.close();
      wsRef.current = null;
    };
  }, [id, enabled]);

  const send = (command: string) => {
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: "command", command }));
    }
  };

  return { lines, stats, cpuHistory, memHistory, connected, send };
}
