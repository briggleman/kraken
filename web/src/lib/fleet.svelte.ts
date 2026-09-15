// Fleet data — servers, specs, and nodes polled together, pausing while the
// tab is hidden and catching up immediately on return.

import { api, errMsg } from "@/api/client";
import type { AuditEntry, Node, Server, Spec } from "@/api/types";

/** The resting cadence, and the old Fleet page's. */
const POLL_MS = 10_000;

// ...and the cadence while anything in the fleet is mid-transition. These
// states resolve on their own within seconds to minutes, and 10s of them is
// long enough that a finished install still reads as running — which is the
// stale-card complaint in #313 arriving by a second road. Watching the state
// itself covers the fleet grid and the drill-in alike, and needs no console log
// to parse. Every imperative act (power, reinstall, create, delete) already
// refreshes off its own response, so this is only about transitions the Panel
// makes on its own.
const TRANSIENT_POLL_MS = 2_500;
const TRANSIENT_STATES: readonly Server["state"][] = ["installing", "starting", "stopping"];

// How many resting poll intervals may pass with no *fully* successful refresh
// before the header stops claiming the deck is live. Three is one tick for the
// blip itself plus two for a retry to land — short enough that an operator
// watching an install pass sees the freeze inside half a minute, long enough
// that a single slow /nodes call does not flicker the indicator (#313).
export const STALE_POLLS = 3;
export const STALE_AFTER_MS = STALE_POLLS * POLL_MS;

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
  // The reads that failed on the most recent tick, each named, or null when the
  // tick was whole. The header hands this to the operator as the title on its
  // stale reading — it is the only account of *what* went quiet.
  lastError: null as string | null,
  // When the most recent tick in which nothing failed completed. A poll that
  // loses one sibling keeps the other two, so "the deck is current" stopped
  // being the same question as "the last request succeeded" — this is what
  // answers it (#313).
  lastOkMs: 0,
});

export function specOf(server: Server): Spec | undefined {
  return fleet.specs.find((s) => s.id === server.spec_id);
}

export function nodeOf(server: Server): Node | undefined {
  return fleet.nodes.find((n) => n.id === server.node_id);
}

/** How often the fleet should be re-read right now. */
export function fleetPollMs(servers: readonly Server[]): number {
  return servers.some((s) => TRANSIENT_STATES.includes(s.state)) ? TRANSIENT_POLL_MS : POLL_MS;
}

// Every read must have answered at least once before the deck is called loaded:
// the empty-roster line points at a node band's New Server as the only way to
// create anything, so rendering it with a failed /nodes read offers an
// instruction the screen cannot carry out, and a failed /specs read renders
// every card's spec as a raw UUID. Subsequent partial failures keep the
// previous values, which is the whole point of the change — this is only about
// the very first paint.
const everAnswered = { servers: false, specs: false, nodes: false };

// Responses are applied in issue order, not arrival order. The timer, the
// visibility catch-up and a dozen imperative callers overlap freely, and a slow
// older response landing last would write `starting` back over `running` and
// re-stamp freshness with data from before the flip.
let issued = 0;
let appliedGen = 0;

/** One poll tick. Partial-failure tolerant on purpose: the three reads are
 *  independent resources, and a rejected `/nodes` used to discard the servers
 *  the same tick had already fetched — so every server state froze on the last
 *  good snapshot with nothing on screen saying so (#313). Whatever answered is
 *  applied; whatever did not keeps its previous value and is named in
 *  `fleet.lastError`, and only a whole tick re-stamps freshness. */
export async function refreshFleet(): Promise<void> {
  const gen = ++issued;
  const t0 = performance.now();
  const [s, sp, n, a] = await Promise.allSettled([
    api.listServers(),
    api.listSpecs(),
    api.listNodes(),
    api.listAudit().catch(() => ({ entries: null })), // viewers may lack audit read
  ]);
  if (gen <= appliedGen) return; // a newer tick already landed; this one is history
  appliedGen = gen;

  const errors: string[] = [];

  /** Apply one read, or account for why it is missing. */
  function take<T>(name: keyof typeof everAnswered, r: PromiseSettledResult<T>, apply: (v: T) => void) {
    if (r.status === "fulfilled") {
      apply(r.value);
      everAnswered[name] = true;
      return;
    }
    errors.push(name + ": " + errMsg(r.reason));
  }

  take("servers", s, (v) => {
    fleet.servers = v.servers ?? [];
  });
  take("specs", sp, (v) => {
    fleet.specs = v.specs ?? [];
  });
  take("nodes", n, (v) => {
    fleet.nodes = v.nodes ?? [];
    fleet.panelVersion = v.panel_version ?? "";
    fleet.panelAgentSha = v.panel_agent_sha ?? {};
  });

  // Audit is already best-effort above (a viewer may not be allowed to read
  // it), so it never counts against freshness.
  if (a.status === "fulfilled") fleet.audit = a.value.entries ?? [];

  if (everAnswered.servers && everAnswered.specs && everAnswered.nodes) fleet.loaded = true;

  if (errors.length === 0) {
    // Only a whole tick is a round trip worth reporting. A rejection can come
    // back in a millisecond or sit out a 20s timeout, and either one rendered
    // as "ping" describes the failure rather than the link.
    fleet.pingMs = Math.round(performance.now() - t0);
    fleet.lastOkMs = Date.now();
    lastOkIsReal = true; // earned, so the hidden-tab clock may carry it forward
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

// --- the staleness clock across a hidden tab --------------------------------
// Polling disarms while the tab is hidden, so without this the age would run on
// through a lunch break and the header would paint "stale · 2h" on return for
// the moment before the catch-up poll lands. That is a lie about the panel: the
// deck is not out of date, it was not being asked. The clock holds still for
// exactly as long as nobody is looking.

let hiddenAt = 0;

// Whether `lastOkMs` is a stamp a whole tick actually earned, or the placeholder
// the first start puts there so the header has an age to read before the first
// tick lands. Only an earned one may be carried across a hidden stretch: the
// placeholder shifted forward by a two-hour background sit would have the deck
// paint the live dot for a full budget on return, for a Panel that has never
// once answered (#320).
let lastOkIsReal = false;

export function suspendStaleClock(nowMs: number = Date.now()) {
  if (!hiddenAt) hiddenAt = nowMs;
}

export function resumeStaleClock(nowMs: number = Date.now()) {
  if (!hiddenAt) return;
  if (lastOkIsReal && fleet.lastOkMs) fleet.lastOkMs += Math.max(0, nowMs - hiddenAt);
  hiddenAt = 0;
}

let timer: ReturnType<typeof setTimeout> | undefined;
let started = false;
let onVisibility: (() => void) | undefined;

function disarm() {
  if (timer !== undefined) {
    clearTimeout(timer);
    timer = undefined;
  }
}

// A chained timeout rather than an interval: the cadence is a function of what
// the fleet is doing, so it has to be re-decided after every tick.
function arm() {
  disarm();
  timer = setTimeout(() => void refreshThenArm(), fleetPollMs(fleet.servers));
}

/** The one way the poll advances: read, then re-arm on the roster that read
 *  just brought back.
 *
 *  The re-arm is in a `finally` because it is the only thing holding the chain
 *  together. `refreshFleet` accounts for a *rejected* read itself, but a throw
 *  from the call site of one — `api.listAudit()` raising before its own
 *  `.catch` can be attached, say — escapes the whole function, and a re-arm
 *  written after the await would simply not run. Polling would then stay dead
 *  until the next visibilitychange, which on a tab nobody switches away from is
 *  forever. Swallowing it here is deliberate for the same reason, and it is not
 *  swallowed silently: a throw at that level is a fault in this file rather
 *  than an unreachable Panel, and it goes on the header's error line like any
 *  other reason the deck stopped being current. */
async function refreshThenArm() {
  timer = undefined;
  try {
    await refreshFleet();
  } catch (e) {
    fleet.lastError = errMsg(e);
  } finally {
    if (started && !document.hidden) arm();
  }
}

export function startFleetPolling() {
  if (started) return;
  started = true;
  // Only on the very first start. Re-stamping on every start would let the
  // auth effect's stop/start cycle hide a real outage for another 30 seconds.
  if (!fleet.lastOkMs) fleet.lastOkMs = Date.now();
  onVisibility = () => {
    if (document.hidden) {
      disarm();
      suspendStaleClock();
    } else {
      resumeStaleClock();
      // Catch up immediately on return, and arm on what comes back rather than
      // on the roster from before: a server that went into an install while the
      // tab was away would otherwise get a resting interval to itself.
      void refreshThenArm();
    }
  };
  document.addEventListener("visibilitychange", onVisibility);
  // A start on a hidden tab is a start that will not poll again: refreshThenArm
  // declines to arm while hidden, exactly as the visibility handler's disarm
  // would. The clock has to be told the same thing here, or `hiddenAt` stays 0
  // through a background load and the resume has nothing to shift — the age
  // would then have run the whole time the tab sat unopened, and the header
  // would paint a stale reading for a deck nobody had asked about. One catch-up
  // read still goes out, so the first paint on return is not an empty deck, and
  // if it fails the clock resumes against a placeholder it is not allowed to
  // shift — a Panel that has never answered still reads stale.
  if (document.hidden) suspendStaleClock();
  void refreshThenArm();
}

export function stopFleetPolling() {
  disarm();
  if (onVisibility) {
    // The auth effect toggles this pair, so a listener left behind accumulates
    // one more disarm/rearm racer per sign-out.
    document.removeEventListener("visibilitychange", onVisibility);
    onVisibility = undefined;
  }
  // Fold any open suspension in rather than dropping it: stopping while hidden
  // is reachable (a background 401 takes the auth effect through this pair), and
  // a dropped one leaves the age counting a stretch nobody was looking at.
  resumeStaleClock();
  // lastOkMs deliberately survives: a stop/start during an outage must not
  // reset the age that is reporting it. Its *provenance* does not survive — the
  // next session re-earns the right to carry it across a hidden stretch with
  // its own successful tick, which errs toward reporting stale, the safe
  // direction. The first-paint guarantee does not survive either: the next
  // session gets its own complete first read before the deck claims to be
  // showing the estate.
  lastOkIsReal = false;
  everAnswered.servers = false;
  everAnswered.specs = false;
  everAnswered.nodes = false;
  fleet.loaded = false;
  started = false;
}
