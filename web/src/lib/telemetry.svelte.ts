// Node host vitals — cpu, memory, disk, network and temperature, polled from
// the Panel's telemetry cache and accumulated into the history the node band's
// instruments draw.
//
// Polled separately from the 10s fleet sweep, and faster: vitals are the one
// thing on the pane that is supposed to move. The Panel refreshes its own cache
// every 5s, so polling at the same cadence lands roughly one fresh reading per
// request without asking for numbers that don't exist yet.
//
// Every metric is optional. A node the Panel has no fresh reading for is absent
// from the payload entirely, and within a node each group carries a *_known
// flag — false means "this host cannot report this", which the band draws as an
// empty instrument. Nothing here ever substitutes a zero for a missing value.

import { api } from "@/api/client";
import type { NodeTelemetry } from "@/api/types";

const POLL_MS = 5_000;

/** Samples kept per node — the dot-matrix track's column count. */
export const TELEMETRY_HISTORY = 48;

export interface NodeVitals {
  /** Latest reading, or undefined when the Panel has nothing fresh for this node. */
  now?: NodeTelemetry;
  /** cpu % per sample, oldest first. Sparse: gaps are dropped, not zero-filled. */
  cpu: number[];
  /** memory used % per sample. */
  mem: number[];
  /** disk used % per sample. */
  disk: number[];
  /**
   * Highest throughput seen on this node, in Mb/s — the reference the packet
   * channel scales its tempo against. Adaptive because there is no link speed
   * to normalize against: a 40Mb/s burst should look busy on a node that never
   * exceeds 50, and unremarkable on one that peaks at 900.
   */
  netPeakMbps: number;
}

export const telemetry = $state({
  byNode: {} as Record<string, NodeVitals>,
  loaded: false,
  lastError: null as string | null,
});

export function vitalsFor(nodeID: string): NodeVitals | undefined {
  return telemetry.byNode[nodeID];
}

function pct(used: number, total: number): number | undefined {
  if (!total || total <= 0 || used < 0) return undefined;
  return Math.max(0, Math.min(100, (used / total) * 100));
}

export function memPct(t: NodeTelemetry | undefined): number | undefined {
  if (!t?.mem_known) return undefined;
  return pct(t.mem_used_mb, t.mem_total_mb);
}

export function diskPct(t: NodeTelemetry | undefined): number | undefined {
  if (!t?.disk_known) return undefined;
  return pct(t.disk_used_mb, t.disk_total_mb);
}

/** Total throughput in megabits per second — the unit the network readout prints. */
export function netMbps(t: NodeTelemetry | undefined): number | undefined {
  if (!t?.net_known) return undefined;
  return ((t.net_rx_bps + t.net_tx_bps) * 8) / 1_000_000;
}

function push(history: number[], v: number | undefined) {
  // A sample the host couldn't supply is skipped rather than recorded as 0 —
  // a zero column in the history is a claim the node was idle.
  if (v === undefined) return;
  history.push(v);
  while (history.length > TELEMETRY_HISTORY) history.shift();
}

export async function refreshTelemetry(): Promise<void> {
  try {
    const res = await api.nodeTelemetry();
    const incoming = res.nodes ?? {};
    const next: Record<string, NodeVitals> = {};
    for (const [id, t] of Object.entries(incoming)) {
      const prev = telemetry.byNode[id];
      const v: NodeVitals = {
        now: t,
        cpu: prev ? [...prev.cpu] : [],
        mem: prev ? [...prev.mem] : [],
        disk: prev ? [...prev.disk] : [],
        netPeakMbps: prev?.netPeakMbps ?? 0,
      };
      push(v.cpu, t.cpu_known ? t.cpu_percent : undefined);
      push(v.mem, memPct(t));
      push(v.disk, diskPct(t));
      const mbps = netMbps(t);
      if (mbps !== undefined) v.netPeakMbps = Math.max(v.netPeakMbps, mbps);
      next[id] = v;
    }
    // Nodes absent from the payload keep their history but lose their reading,
    // so the band shows an emptying instrument rather than a frozen number.
    for (const [id, prev] of Object.entries(telemetry.byNode)) {
      if (!next[id]) {
        next[id] = {
          now: undefined,
          cpu: prev.cpu,
          mem: prev.mem,
          disk: prev.disk,
          netPeakMbps: prev.netPeakMbps,
        };
      }
    }
    telemetry.byNode = next;
    telemetry.loaded = true;
    telemetry.lastError = null;
  } catch (e) {
    telemetry.lastError = e instanceof Error ? e.message : String(e);
  }
}

let timer: ReturnType<typeof setInterval> | undefined;
let started = false;

export function startTelemetryPolling() {
  if (started) return;
  started = true;
  const arm = () => {
    if (timer === undefined) timer = setInterval(() => void refreshTelemetry(), POLL_MS);
  };
  const disarm = () => {
    if (timer !== undefined) {
      clearInterval(timer);
      timer = undefined;
    }
  };
  document.addEventListener("visibilitychange", () => {
    if (document.hidden) {
      disarm();
    } else {
      void refreshTelemetry(); // catch up immediately on return, then resume
      arm();
    }
  });
  void refreshTelemetry();
  arm();
}

export function stopTelemetryPolling() {
  if (timer !== undefined) {
    clearInterval(timer);
    timer = undefined;
  }
  started = false;
}
