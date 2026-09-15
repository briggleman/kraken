// Fleet data — servers, specs, and nodes polled together every 10s, exactly
// the old Fleet page's cadence, pausing while the tab is hidden and catching
// up immediately on return.

import { api } from "@/api/client";
import type { AuditEntry, Node, Server, Spec } from "@/api/types";

const POLL_MS = 10_000;

// How many poll intervals may pass with no *fully* successful refresh before
// the header stops claiming the deck is live. Three is one tick for the blip
// itself plus two for a retry to land — short enough that an operator watching
// an install pass sees the freeze inside half a minute, long enough that a
// single slow /nodes call does not flicker the indicator (#313).
export const STALE_POLLS = 3;
export const STALE_AFTER_MS = STALE_POLLS * POLL_MS;

// After a poke (a Panel lifecycle line, see pokeFleet) the fleet is re-read
// off-cadence. Bursts of lines arrive together, so they coalesce into one
// request on this delay rather than one request per line.
const POKE_MS = 400;

/** Which of the poll's three required reads failed on the last tick. */
export type FleetSource = "servers" | "specs" | "nodes";

export const fleet = $state({
  servers: [] as Server[],
  specs: [] as Spec[],
  nodes: [] as Node[],
  audit: [] as AuditEntry[],
  panelVersion: "",
  // The embedded agent build's SHA-256 per "os/arch". Preferred over panelVersion
  // for skew detection (#93): a panel-only release leaves the agent artifact
  // byte-identical, and flagging the fleet for that trains operators to ignore
  // the flag. Absent from a panel built without embedded agent binaries.
  panelAgentSha: {} as Record<string, string>,
  pingMs: 0,
  loaded: false,
  lastError: null as string | null,
  // Which reads failed on the most recent tick, and when the most recent tick
  // in which *none* of them failed completed. A poll that loses one sibling
  // keeps the other two, so "the deck is current" is no longer the same
  // question as "the last request succeeded" — this is the pair that answers
  // it (#313).
  failed: [] as FleetSource[],
  lastOkMs: 0,
});

export function specOf(server: Server): Spec | undefined {
  return fleet.specs.find((s) => s.id === server.spec_id);
}

export function nodeOf(server: Server): Node | undefined {
  return fleet.nodes.find((n) => n.id === server.node_id);
}

function why(r: PromiseRejectedResult): string {
  const e = r.reason;
  return e instanceof Error ? e.message : String(e);
}

/** One poll tick. Partial-failure tolerant on purpose: the three reads are
 *  independent resources, and a rejected `/nodes` used to discard the servers
 *  the same tick had already fetched — so every server state froze on the last
 *  good snapshot with nothing on screen saying so (#313). Whatever answered is
 *  applied; whatever did not keeps its previous value and is named in
 *  `fleet.failed`, which is what turns the header's live dot amber. */
export async function refreshFleet(): Promise<void> {
  const t0 = performance.now();
  const [s, sp, n, a] = await Promise.allSettled([
    api.listServers(),
    api.listSpecs(),
    api.listNodes(),
    api.listAudit().catch(() => ({ entries: null })), // viewers may lack audit read
  ]);
  fleet.pingMs = Math.round(performance.now() - t0);

  const failed: FleetSource[] = [];
  const errors: string[] = [];

  if (s.status === "fulfilled") {
    fleet.servers = s.value.servers ?? [];
    // The empty-roster line is about the servers read and nothing else, so it
    // unblocks as soon as that one answers.
    fleet.loaded = true;
  } else {
    failed.push("servers");
    errors.push("servers: " + why(s));
  }

  if (sp.status === "fulfilled") {
    fleet.specs = sp.value.specs ?? [];
  } else {
    failed.push("specs");
    errors.push("specs: " + why(sp));
  }

  if (n.status === "fulfilled") {
    fleet.nodes = n.value.nodes ?? [];
    fleet.panelVersion = n.value.panel_version ?? "";
    fleet.panelAgentSha = n.value.panel_agent_sha ?? {};
  } else {
    failed.push("nodes");
    errors.push("nodes: " + why(n));
  }

  // Audit is already best-effort above (a viewer may not be allowed to read
  // it), so it never counts against freshness.
  if (a.status === "fulfilled") fleet.audit = a.value.entries ?? [];

  fleet.failed = failed;
  if (failed.length === 0) {
    fleet.lastOkMs = Date.now();
    fleet.lastError = null;
  } else {
    fleet.lastError = errors.join("; ");
  }
}

/** How current the deck is, and whether it is current enough to still be
 *  called live. Pure over the store plus the caller's clock so the header can
 *  re-read it on the 1s tick — a poll that hangs rather than rejecting must
 *  still age. */
export function fleetHealth(nowMs: number = Date.now()): { stale: boolean; ageMs: number } {
  if (!fleet.lastOkMs) return { stale: false, ageMs: 0 };
  const ageMs = Math.max(0, nowMs - fleet.lastOkMs);
  return { stale: ageMs > STALE_AFTER_MS, ageMs };
}

/** The age beside a stale dot, in the readouts' voice: 45s, 4m, 2h. Coarse on
 *  purpose — the number is there to say "older than you think", not to be
 *  read to the second. */
export function fmtAge(ms: number): string {
  const sec = Math.floor(ms / 1000);
  if (sec < 60) return sec + "s";
  const min = Math.floor(sec / 60);
  if (min < 60) return min + "m";
  return Math.floor(min / 60) + "h";
}

// --- lifecycle pokes --------------------------------------------------------
// The Panel writes its own lines into a server's install buffer as a run moves
// through its phases, and those lines reach the browser over the console
// socket that is already open. They are the earliest notice the client gets
// that a server's state is about to change — earlier than the next poll, and
// available even when that poll fails — so a lifecycle line asks for a refresh
// instead of leaving the flip to the timer (#313).

/** The prefix the Panel puts on every line it writes itself (handlers_server.go
 *  — provisioning, install complete, updating, update complete, install
 *  failed). Container output never carries it. */
export const PANEL_LINE_PREFIX = "[panel] ";

let pokeTimer: ReturnType<typeof setTimeout> | undefined;
let seenLifecycleSeq = -1;

/** Re-poll shortly, coalescing a burst of callers into one request. */
export function pokeFleet() {
  if (pokeTimer !== undefined) return;
  pokeTimer = setTimeout(() => {
    pokeTimer = undefined;
    void refreshFleet();
  }, POKE_MS);
}

/** Fold the console's current lines into the poke. Returns whether this call
 *  found a lifecycle line it had not seen before — the seq is monotonic per
 *  stream instance, so a replayed buffer re-arms nothing and a poke costs one
 *  request per phase, not one per repaint. */
export function notePanelLifecycle(lines: readonly { seq: number; text: string }[]): boolean {
  let newest = seenLifecycleSeq;
  for (const l of lines) {
    if (l.seq > newest && l.text.startsWith(PANEL_LINE_PREFIX)) newest = l.seq;
  }
  if (newest === seenLifecycleSeq) return false;
  seenLifecycleSeq = newest;
  pokeFleet();
  return true;
}

/** Forget which lifecycle lines have been seen — a different server's stream is
 *  a different account of a different run. */
export function resetLifecycleWatch() {
  seenLifecycleSeq = -1;
  if (pokeTimer !== undefined) {
    clearTimeout(pokeTimer);
    pokeTimer = undefined;
  }
}

let timer: ReturnType<typeof setInterval> | undefined;
let started = false;

export function startFleetPolling() {
  if (started) return;
  started = true;
  // The clock starts now, not at the first success: a session whose very first
  // poll never lands is exactly the case that must go stale rather than sit on
  // an empty deck claiming to be live.
  fleet.lastOkMs = Date.now();
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
  resetLifecycleWatch();
  fleet.lastOkMs = 0;
  started = false;
}
