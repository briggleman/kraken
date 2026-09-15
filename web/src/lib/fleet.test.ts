// The fleet poll's failure behaviour (#313). The poll reads three independent
// resources on one tick, and the bug was that it treated them as one: a single
// rejected sibling discarded the whole tick, so every server state froze on the
// last good snapshot and nothing on screen said so. These tests pin the three
// halves of the fix — what survives a partial failure, when the header stops
// calling the deck live, and what re-polls it off-cadence.

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
}));

import {
  PANEL_LINE_PREFIX,
  STALE_AFTER_MS,
  fleet,
  fleetHealth,
  fmtAge,
  notePanelLifecycle,
  refreshFleet,
  resetLifecycleWatch,
} from "./fleet.svelte";
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

beforeEach(() => {
  vi.clearAllMocks();
  vi.useRealTimers();
  resetLifecycleWatch();
  fleet.servers = [];
  fleet.specs = [];
  fleet.nodes = [];
  fleet.audit = [];
  fleet.panelVersion = "";
  fleet.failed = [];
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
    expect(fleet.failed).toEqual([]);

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
    expect(fleet.failed).toEqual(["nodes"]);
    expect(fleet.lastError).toContain("nodes: 502 bad gateway");
  });

  it("names every failed sibling and lets the survivors through", async () => {
    listSpecs.mockRejectedValue(new Error("timeout"));
    listNodes.mockRejectedValue(new Error("timeout"));
    await refreshFleet();

    expect(fleet.servers).toHaveLength(1);
    expect(fleet.failed).toEqual(["specs", "nodes"]);
    expect(fleet.loaded).toBe(true); // the servers read is what that line gates
  });

  it("clears the error and re-stamps freshness once a whole tick lands", async () => {
    listNodes.mockRejectedValue(new Error("502"));
    await refreshFleet();
    const stamped = fleet.lastOkMs;
    expect(fleet.lastError).not.toBeNull();

    allOk();
    await refreshFleet();
    expect(fleet.failed).toEqual([]);
    expect(fleet.lastError).toBeNull();
    expect(fleet.lastOkMs).toBeGreaterThanOrEqual(stamped);
  });

  it("never counts audit against freshness — a viewer may not be allowed to read it", async () => {
    // client.listAudit is already wrapped in a .catch by the poll; this is the
    // rejection that gets past a caller who did not wrap it.
    listAudit.mockRejectedValue(new Error("403 forbidden"));
    await expect(refreshFleet()).resolves.toBeUndefined();
    expect(fleet.failed).toEqual([]);
    expect(fleet.lastError).toBeNull();
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

  it("writes the age the way the readouts do", () => {
    expect(fmtAge(45_000)).toBe("45s");
    expect(fmtAge(59_999)).toBe("59s");
    expect(fmtAge(60_000)).toBe("1m");
    expect(fmtAge(4 * 60_000)).toBe("4m");
    expect(fmtAge(2 * 3_600_000)).toBe("2h");
  });
});

describe("notePanelLifecycle", () => {
  function line(seq: number, text: string) {
    return { seq, text };
  }

  it("re-polls on a lifecycle line and coalesces the burst behind it", async () => {
    vi.useFakeTimers();
    const lines = [line(1, "LogInit: Display: BuildId 240163")];
    expect(notePanelLifecycle(lines)).toBe(false); // container output is not a phase

    lines.push(line(2, PANEL_LINE_PREFIX + "update complete — starting dragonwilds-01"));
    expect(notePanelLifecycle(lines)).toBe(true);
    lines.push(line(3, PANEL_LINE_PREFIX + "install complete — dragonwilds-01 is ready to start"));
    expect(notePanelLifecycle(lines)).toBe(true);

    expect(listServers).not.toHaveBeenCalled(); // still coalescing
    await vi.advanceTimersByTimeAsync(1_000);
    expect(listServers).toHaveBeenCalledTimes(1); // two lines, one request
    vi.useRealTimers();
  });

  it("does not re-arm on lines it has already seen", async () => {
    vi.useFakeTimers();
    const lines = [line(7, PANEL_LINE_PREFIX + "provisioning dragonwilds-01 on abyss-win")];
    expect(notePanelLifecycle(lines)).toBe(true);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(listServers).toHaveBeenCalledTimes(1);

    // The same buffer re-read on a repaint, and an older line arriving behind
    // it: the seq is monotonic, so neither is new.
    expect(notePanelLifecycle(lines)).toBe(false);
    expect(notePanelLifecycle([line(3, PANEL_LINE_PREFIX + "updating dragonwilds-01"), ...lines])).toBe(
      false,
    );
    await vi.advanceTimersByTimeAsync(1_000);
    expect(listServers).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });
});
