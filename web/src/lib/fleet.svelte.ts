// Fleet data — servers, specs, and nodes polled together every 10s, exactly
// the old Fleet page's cadence, pausing while the tab is hidden and catching
// up immediately on return.

import { api } from "@/api/client";
import type { AuditEntry, Node, Server, Spec } from "@/api/types";

const POLL_MS = 10_000;

export const fleet = $state({
  servers: [] as Server[],
  specs: [] as Spec[],
  nodes: [] as Node[],
  audit: [] as AuditEntry[],
  panelVersion: "",
  pingMs: 0,
  loaded: false,
  lastError: null as string | null,
});

export function specOf(server: Server): Spec | undefined {
  return fleet.specs.find((s) => s.id === server.spec_id);
}

export function nodeOf(server: Server): Node | undefined {
  return fleet.nodes.find((n) => n.id === server.node_id);
}

export async function refreshFleet(): Promise<void> {
  const t0 = performance.now();
  try {
    const [s, sp, n, a] = await Promise.all([
      api.listServers(),
      api.listSpecs(),
      api.listNodes(),
      api.listAudit().catch(() => ({ entries: null })), // viewers may lack audit read
    ]);
    fleet.pingMs = Math.round(performance.now() - t0);
    fleet.servers = s.servers ?? [];
    fleet.specs = sp.specs ?? [];
    fleet.nodes = n.nodes ?? [];
    fleet.audit = a.entries ?? [];
    fleet.panelVersion = n.panel_version ?? "";
    fleet.loaded = true;
    fleet.lastError = null;
  } catch (e) {
    fleet.lastError = e instanceof Error ? e.message : String(e);
  }
}

let timer: ReturnType<typeof setInterval> | undefined;
let started = false;

export function startFleetPolling() {
  if (started) return;
  started = true;
  const arm = () => {
    if (timer === undefined) timer = setInterval(() => void refreshFleet(), POLL_MS);
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
      void refreshFleet(); // catch up immediately on return, then resume
      arm();
    }
  });
  void refreshFleet();
  arm();
}

export function stopFleetPolling() {
  if (timer !== undefined) {
    clearInterval(timer);
    timer = undefined;
  }
  started = false;
}
