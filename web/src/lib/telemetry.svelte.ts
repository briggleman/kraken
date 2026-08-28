// The histories behind the node band's instruments.
//
// cpu, disk, network and temperature are host readings polled from the Panel's
// telemetry cache. Memory is the exception: the band shows the memory the
// scheduler has committed to servers (from the node record), because that is
// what decides whether the next server fits. The agent's host-memory reading is
// still carried on the API for anything that wants it.
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
import { fleet } from "./fleet.svelte";
import type { NodeTelemetry } from "@/api/types";

const POLL_MS = 5_000;

/** Samples kept per node — the dot-matrix track's column count. */
export const TELEMETRY_HISTORY = 48;

export interface NodeVitals {
  /** Latest reading, or undefined when the Panel has nothing fresh for this node. */
  now?: NodeTelemetry;
  /** cpu % per sample, oldest first. Sparse: gaps are dropped, not zero-filled. */
  cpu: number[];
  /**
   * Memory the scheduler has committed to servers, as a % of the node's total.
   * From the node record rather than the agent: this is the number that decides
   * whether the next server fits, and it is available even for a node whose
   * agent is unreachable. It only moves on deploy or delete — a flat line here
   * is the truth, not a dead feed.
   */
  alloc: number[];
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

/** Committed memory as a % of the node's total, from the node record. */
export function allocPct(node: { allocated_memory_mb: number; total_memory_mb: number } | undefined): number | undefined {
  if (!node) return undefined;
  return pct(node.allocated_memory_mb, node.total_memory_mb);
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
    // Keyed off the fleet, not the payload: allocation comes from the node
    // record, so a node whose agent is unreachable still gets an entry (and a
    // real memory readout) while its agent-fed instruments sit empty.
    const ids = new Set([...fleet.nodes.map((n) => n.id), ...Object.keys(incoming)]);
    const next: Record<string, NodeVitals> = {};
    for (const id of ids) {
      const prev = telemetry.byNode[id];
      const t = incoming[id];
      const v: NodeVitals = {
        now: t,
        cpu: prev ? [...prev.cpu] : [],
        alloc: prev ? [...prev.alloc] : [],
        disk: prev ? [...prev.disk] : [],
        netPeakMbps: prev?.netPeakMbps ?? 0,
      };
      push(v.cpu, t?.cpu_known ? t.cpu_percent : undefined);
      push(v.alloc, allocPct(fleet.nodes.find((n) => n.id === id)));
      push(v.disk, diskPct(t));
      const mbps = netMbps(t);
      if (mbps !== undefined) v.netPeakMbps = Math.max(v.netPeakMbps, mbps);
      next[id] = v;
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
