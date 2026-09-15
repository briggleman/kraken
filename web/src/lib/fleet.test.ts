// The fleet poll's failure behaviour (#313). The poll reads three independent
// resources on one tick, and the bug was that it treated them as one: a single
// rejected sibling discarded the whole tick, so every server state froze on the
// last good snapshot and nothing on screen said so. These tests pin what
// survives a partial failure, what "ping" and "loaded" are allowed to mean
// afterwards, when the header stops calling the deck live, and the cadence that
// keeps a transition from waiting out a resting interval.

import { beforeEach, describe, expect, it, vi } from "vitest";

const listServers = vi.fn();
const listSpecs = vi.fn();
const listNodes = vi.fn();
const listAudit = vi.fn();

vi.mock("@/api/client", () => ({
  api: {
    listServers: () => listServers(),
    listSpecs: () => listSpecs(),
    listNodes: () => listNodes(),
    listAudit: () => listAudit(),
  },
  errMsg: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}));

import {
  STALE_AFTER_MS,
  fleet,
  fleetHealth,
  fleetPollMs,
  refreshFleet,
  resumeStaleClock,
  startFleetPolling,
  stopFleetPolling,
  suspendStaleClock,
} from "./fleet.svelte";
import { fmtAge } from "./fmt";
import type { Node, Server, Spec } from "@/api/types";

function server(id: string, state: Server["state"]): Server {
  return {
    id,
    name: id,
    spec_id: "spec-1",
    node_id: "node-1",
    kind: "windows-native",
    state,
    vars: {},
    ports: { game: 27015 },
    memory_mb: 8192,
    created_at: "2026-09-15T09:46:00Z",
  };
}

const SPEC = { id: "spec-1", name: "Dragonwilds" } as unknown as Spec;
const NODE = { id: "node-1", name: "abyss-win" } as unknown as Node;

function allOk() {
  listServers.mockResolvedValue({ servers: [server("srv-1", "installing")] });
  listSpecs.mockResolvedValue({ specs: [SPEC] });
  listNodes.mockResolvedValue({ nodes: [NODE], panel_version: "0.50.1" });
  listAudit.mockResolvedValue({ entries: [] });
}

/** A promise that resolves only when the test says so. */
function deferred<T>() {
  let settle!: (v: T) => void;
  const promise = new Promise<T>((res) => (settle = res));
  return { promise, settle };
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.useRealTimers();
  // Also the reset hook for the first-paint guarantee and the hidden clock.
  stopFleetPolling();
  fleet.servers = [];
  fleet.specs = [];
  fleet.nodes = [];
  fleet.audit = [];
  fleet.panelVersion = "";
  fleet.pingMs = 0;
  fleet.lastError = null;
  fleet.lastOkMs = 0;
  fleet.loaded = false;
  allOk();
});

describe("refreshFleet", () => {
  it("applies every read that answered and keeps the last value of the one that did not", async () => {
    await refreshFleet();
    expect(fleet.servers[0].state).toBe("installing");
    expect(fleet.specs).toHaveLength(1);
    expect(fleet.nodes).toHaveLength(1);

    // The next tick: /nodes falls over while the server finishes its pass.
    // Before the fix the whole tick was discarded and the card stayed
    // `installing` until something forced a reload.
    listServers.mockResolvedValue({ servers: [server("srv-1", "running")] });
    listNodes.mockRejectedValue(new Error("502 bad gateway"));
    await refreshFleet();

    expect(fleet.servers[0].state).toBe("running");
    expect(fleet.specs).toHaveLength(1);
    expect(fleet.nodes).toEqual([NODE]); // the previous value, not an empty fleet
    expect(fleet.panelVersion).toBe("0.50.1");
    expect(fleet.lastError).toContain("nodes: 502 bad gateway");
  });

  it("names every failed sibling and lets the survivors through", async () => {
    listSpecs.mockRejectedValue(new Error("timeout"));
    listNodes.mockRejectedValue(new Error("timeout"));
    await refreshFleet();

    expect(fleet.servers).toHaveLength(1);
    expect(fleet.lastError).toContain("specs: timeout");
    expect(fleet.lastError).toContain("nodes: timeout");
  });

  it("clears the error and re-stamps freshness once a whole tick lands", async () => {
    listNodes.mockRejectedValue(new Error("502"));
    await refreshFleet();
    expect(fleet.lastError).not.toBeNull();
    expect(fleet.lastOkMs).toBe(0); // a partial tick is not a fresh one

    allOk();
    await refreshFleet();
    expect(fleet.lastError).toBeNull();
    expect(fleet.lastOkMs).toBeGreaterThan(0);
  });

  it("never counts audit against freshness — a viewer may not be allowed to read it", async () => {
    // client.listAudit is already wrapped in a .catch by the poll; this is the
    // rejection that gets past a caller who did not wrap it.
    listAudit.mockRejectedValue(new Error("403 forbidden"));
    await expect(refreshFleet()).resolves.toBeUndefined();
    expect(fleet.lastError).toBeNull();
    expect(fleet.lastOkMs).toBeGreaterThan(0);
  });

  it("only calls it a ping when the whole tick answered", async () => {
    await refreshFleet();
    const good = fleet.pingMs;
    expect(good).toBeLessThan(50);

    // A read that sits out its timeout and then rejects measures the failure,
    // not the link. Rendering that as "live · ping 20.0s" reports a round trip
    // the header is not showing the result of.
    listSpecs.mockImplementation(
      () => new Promise((_, rej) => setTimeout(() => rej(new Error("timeout")), 120)),
    );
    await refreshFleet();
    expect(fleet.lastError).toContain("specs: timeout");
    expect(fleet.pingMs).toBe(good); // still the last honest measurement
  });

  it("discards a slow older response instead of letting it undo a newer one", async () => {
    // Two ticks in flight at once — the timer and an imperative caller. The
    // first is slower, and used to land last and write `starting` back over the
    // `running` the second had already applied.
    const slow = deferred<{ servers: Server[] }>();
    listServers.mockReturnValueOnce(slow.promise);
    const first = refreshFleet();

    listServers.mockResolvedValue({ servers: [server("srv-1", "running")] });
    await refreshFleet();
    expect(fleet.servers[0].state).toBe("running");
    const fresh = fleet.lastOkMs;

    slow.settle({ servers: [server("srv-1", "starting")] });
    await first;
    expect(fleet.servers[0].state).toBe("running");
    expect(fleet.lastOkMs).toBe(fresh); // and it does not re-stamp freshness either
  });
});

describe("fleet.loaded", () => {
  it("waits for servers, specs and nodes to each have answered once", async () => {
    // The empty-roster line points at a node band's New Server as the only way
    // to create anything, and a card with no spec renders a raw UUID — so a
    // first paint with either read missing offers an instruction the screen
    // cannot carry out.
    listNodes.mockRejectedValue(new Error("502"));
    await refreshFleet();
    expect(fleet.servers).toHaveLength(1);
    expect(fleet.loaded).toBe(false);

    allOk();
    await refreshFleet();
    expect(fleet.loaded).toBe(true);

    // Once the deck has been painted, a later partial failure keeps it — the
    // previous values are still on screen, which is the point of the change.
    listNodes.mockRejectedValue(new Error("502"));
    await refreshFleet();
    expect(fleet.loaded).toBe(true);
  });
});

describe("fleetHealth", () => {
  it("flips to stale after the miss budget and recovers on the next good poll", async () => {
    await refreshFleet();
    const ok = fleet.lastOkMs;

    expect(fleetHealth(ok).stale).toBe(false);
    expect(fleetHealth(ok + STALE_AFTER_MS).stale).toBe(false); // exactly N: still live
    expect(fleetHealth(ok + STALE_AFTER_MS + 1).stale).toBe(true);
    expect(fleetHealth(ok + STALE_AFTER_MS + 1).ageMs).toBeGreaterThan(STALE_AFTER_MS);

    // Whatever was failing starts answering again: the stamp moves and the
    // indicator is live again on that tick, with no extra state to unwind.
    fleet.lastOkMs = ok + STALE_AFTER_MS + 1;
    expect(fleetHealth(ok + STALE_AFTER_MS + 1).stale).toBe(false);
  });

  it("stays live before anything has ever polled", () => {
    fleet.lastOkMs = 0;
    expect(fleetHealth(Date.now()).stale).toBe(false);
  });

  it("holds the clock still while the tab is hidden", () => {
    // Polling disarms while hidden, so an age that ran on through a lunch break
    // would paint "stale · 2h" on return — a lie about the panel, which was
    // never asked.
    const t0 = 1_000_000;
    fleet.lastOkMs = t0;
    suspendStaleClock(t0 + 1_000);
    resumeStaleClock(t0 + 1_000 + 40 * 60_000);
    expect(fleetHealth(t0 + 1_000 + 40 * 60_000).stale).toBe(false);
    expect(fleetHealth(t0 + 1_000 + 40 * 60_000).ageMs).toBe(1_000);
  });

  it("does not let a stop/start cycle hide a real outage", async () => {
    // The auth effect toggles the pair, and re-stamping on every start would
    // buy the outage another full budget of silence.
    listServers.mockRejectedValue(new Error("down"));
    listSpecs.mockRejectedValue(new Error("down"));
    listNodes.mockRejectedValue(new Error("down"));
    const t0 = Date.now() - (STALE_AFTER_MS + 5_000);
    fleet.lastOkMs = t0;

    startFleetPolling();
    expect(fleet.lastOkMs).toBe(t0);
    expect(fleetHealth().stale).toBe(true);
    stopFleetPolling();
    await Promise.resolve();
    await Promise.resolve();
    expect(fleet.lastOkMs).toBe(t0); // and stopping does not clear it either
    expect(fleetHealth().stale).toBe(true);
  });

  it("writes the age the way the readouts do", () => {
    expect(fmtAge(45_000)).toBe("45s");
    expect(fmtAge(59_999)).toBe("59s");
    expect(fmtAge(60_000)).toBe("1m");
    expect(fmtAge(4 * 60_000)).toBe("4m");
    expect(fmtAge(2 * 3_600_000)).toBe("2h");
  });
});

describe("startFleetPolling / stopFleetPolling", () => {
  it("takes its visibilitychange listener back down", async () => {
    // The auth effect toggles the pair on every sign-in and sign-out, so a
    // listener left behind accumulates one more disarm/rearm racer per cycle.
    const add = vi.spyOn(document, "addEventListener");
    const remove = vi.spyOn(document, "removeEventListener");
    try {
      startFleetPolling();
      const registered = add.mock.calls.filter((c) => c[0] === "visibilitychange");
      expect(registered).toHaveLength(1);

      stopFleetPolling();
      const removed = remove.mock.calls.filter((c) => c[0] === "visibilitychange");
      expect(removed).toHaveLength(1);
      expect(removed[0][1]).toBe(registered[0][1]); // the same function, not a new closure
      await Promise.resolve();
    } finally {
      add.mockRestore();
      remove.mockRestore();
      stopFleetPolling();
    }
  });

  it("keeps the chain alive when a refresh throws outright", async () => {
    vi.useFakeTimers();
    try {
      // A throw from the *call site* of a read rather than a rejected read:
      // listAudit raising before the poll's own catch can be attached escapes
      // the whole function, and a re-arm written after the await would not run.
      // Polling would then stay dead until a visibilitychange, which on a tab
      // nobody switches away from is forever.
      listAudit.mockImplementation(() => {
        throw new Error("boom");
      });

      startFleetPolling();
      await vi.advanceTimersByTimeAsync(0);
      expect(listServers).toHaveBeenCalledTimes(1);
      expect(fleet.lastError).toBe("boom");
      expect(vi.getTimerCount()).toBe(1); // armed anyway

      await vi.advanceTimersByTimeAsync(10_000);
      expect(listServers).toHaveBeenCalledTimes(2); // ...and it kept going

      stopFleetPolling();
      expect(vi.getTimerCount()).toBe(0); // no orphan left behind
      await vi.advanceTimersByTimeAsync(60_000);
      expect(listServers).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
      stopFleetPolling();
    }
  });

  it("starts the stale clock suspended when the tab is already hidden", async () => {
    vi.useFakeTimers();
    const hidden = vi.spyOn(document, "hidden", "get").mockReturnValue(true);
    try {
      // A panel opened into a background tab, or a sign-in the operator walked
      // away from. The poll declines to arm while hidden, so the deck simply
      // is not being asked — but `hiddenAt` stayed 0, so the resume had nothing
      // to shift and the header painted "stale · 40m" on return for a freeze
      // that never happened (#320).
      startFleetPolling();
      await vi.advanceTimersByTimeAsync(0);
      expect(listServers).toHaveBeenCalledTimes(1); // one catch-up read, then quiet
      expect(vi.getTimerCount()).toBe(0); // nothing armed behind a hidden tab

      const stamped = fleet.lastOkMs;
      expect(stamped).toBeGreaterThan(0);
      await vi.advanceTimersByTimeAsync(40 * 60_000);
      expect(fleetHealth().stale).toBe(true); // ...until the clock is resumed

      hidden.mockReturnValue(false);
      document.dispatchEvent(new Event("visibilitychange"));
      expect(fleet.lastOkMs).toBeGreaterThanOrEqual(stamped + 40 * 60_000 - 1_000);
      expect(fleetHealth().stale).toBe(false);

      await vi.advanceTimersByTimeAsync(0);
      expect(listServers).toHaveBeenCalledTimes(2); // and polling picks up again
      expect(vi.getTimerCount()).toBe(1);
    } finally {
      hidden.mockRestore();
      vi.useRealTimers();
      stopFleetPolling();
    }
  });

  it("re-decides the cadence from the roster each tick brought back", async () => {
    vi.useFakeTimers();
    try {
      listServers.mockResolvedValue({ servers: [server("srv-1", "installing")] });
      startFleetPolling();
      await vi.advanceTimersByTimeAsync(0);
      expect(listServers).toHaveBeenCalledTimes(1);

      // Mid-install: the transient cadence, decided on what the first read
      // brought back rather than on the empty roster it was armed from.
      await vi.advanceTimersByTimeAsync(2_500);
      expect(listServers).toHaveBeenCalledTimes(2);

      // The pass ends. The tick that observes it arms at the resting cadence.
      listServers.mockResolvedValue({ servers: [server("srv-1", "running")] });
      await vi.advanceTimersByTimeAsync(2_500);
      expect(listServers).toHaveBeenCalledTimes(3);
      await vi.advanceTimersByTimeAsync(2_500);
      expect(listServers).toHaveBeenCalledTimes(3); // no longer every 2.5s
      await vi.advanceTimersByTimeAsync(7_500);
      expect(listServers).toHaveBeenCalledTimes(4);
    } finally {
      vi.useRealTimers();
      stopFleetPolling();
    }
  });
});

describe("fleetPollMs", () => {
  it("speeds up while anything is mid-transition and rests otherwise", () => {
    const resting = fleetPollMs([server("a", "running"), server("b", "offline")]);
    expect(resting).toBe(10_000);

    for (const st of ["installing", "starting", "stopping"] as Server["state"][]) {
      expect(fleetPollMs([server("a", "running"), server("b", st)]), st).toBeLessThan(resting);
    }
    // A settled fleet of failures is not a transition — nothing resolves on its
    // own, so there is nothing to watch for.
    expect(fleetPollMs([server("a", "crashed"), server("b", "install_failed")])).toBe(resting);
    expect(fleetPollMs([])).toBe(resting);
  });
});
