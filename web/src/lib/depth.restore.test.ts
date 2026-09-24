// The backup restore's reading in the drill-in (#361). The ledger used to draw
// a restore as nothing at all — the button said "restoring…" until one request
// came back — and its only progress bar, on an archive being created, was
// hard-wired to 60 %. These pin the rules that replaced both: the fill is the
// agent's figure or nothing, an unsized restore never prints a number, one
// restore at a time, and the operator is told how it ended.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const restoreBackup = vi.fn();
const getServer = vi.fn();
const listBackups = vi.fn();

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ...actual,
    api: {
      restoreBackup: (...a: unknown[]) => restoreBackup(...a),
      getServer: (...a: unknown[]) => getServer(...a),
      listBackups: (...a: unknown[]) => listBackups(...a),
    },
  };
});

vi.mock("./fleet.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./fleet.svelte")>();
  return { ...actual, refreshFleet: vi.fn(async () => {}) };
});

import {
  backupRestore,
  depth,
  restoreActive,
  restoreMeter,
  restoreOutcome,
  surface,
  syncDepthFromFleet,
} from "./depth.svelte";
import { fleet, fleetPollMs } from "./fleet.svelte";
import { chipKind, deadNote } from "./views.svelte";
import type { Backup, RestoreProgress, Server } from "@/api/types";

function job(p: Partial<RestoreProgress>): RestoreProgress {
  return {
    backup_id: "1700000000000__manual-2026-09-24",
    phase: "extracting",
    bytes_done: 0,
    bytes_total: 0,
    started_at: "2026-09-24T12:00:00Z",
    ...p,
  };
}

function srv(p: Partial<Server>): Server {
  return {
    id: "srv-1",
    name: "valheim-01",
    spec_id: "spec",
    node_id: "node",
    kind: "linux-native",
    state: "offline",
    vars: {},
    ports: {},
    memory_mb: 4096,
    created_at: "2026-09-01T00:00:00Z",
    ...p,
  };
}

/** The drill-in re-targets its console stream on every state push, and the
 *  stream reaches for the global WebSocket when it does; nothing here reads it. */
class FakeWebSocket {
  static readonly OPEN = 1;
  readyState = 0;
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: (() => void) | null = null;
  close() {}
  send() {}
}
(globalThis as unknown as { WebSocket: unknown }).WebSocket = FakeWebSocket;

const ARCHIVE: Backup = {
  id: "1700000000000__manual-2026-09-24",
  name: "manual-2026-09-24",
  size: 1024,
  created_ms: 1700000000000,
  state: "ready",
  replication: "",
};

describe("restoreMeter", () => {
  it("reads a sized restore that has not read a byte as 0 %, not as unknown", () => {
    expect(restoreMeter(job({ bytes_done: 0, bytes_total: 2000 }))).toEqual({ pct: 0, sized: true, label: "0%" });
  });

  it("reads half the archive as 50 %", () => {
    expect(restoreMeter(job({ bytes_done: 1000, bytes_total: 2000 }))).toEqual({ pct: 50, sized: true, label: "50%" });
  });

  it("never prints a number for an unsized restore, and never fills the bar", () => {
    // An old agent restoring through the unary call: the Panel knows a restore
    // is running and nothing else.
    const m = restoreMeter(job({ phase: "restoring", bytes_done: 0, bytes_total: 0 }));
    expect(m).toEqual({ pct: 0, sized: false, label: "restoring" });
    // Bytes without a total are not a percentage either.
    expect(restoreMeter(job({ bytes_done: 4096, bytes_total: 0 })).pct).toBe(0);
    expect(restoreMeter(job({ bytes_done: 4096, bytes_total: 0 })).label).toBe("extracting");
  });

  it("names the phase outside extraction and keeps the fill it earned", () => {
    expect(restoreMeter(job({ phase: "applying", bytes_done: 2000, bytes_total: 2000 }))).toEqual({
      pct: 100,
      sized: true,
      label: "applying",
    });
    expect(restoreMeter(job({ phase: "opening" })).label).toBe("opening");
    expect(restoreMeter(undefined)).toEqual({ pct: 0, sized: false, label: "opening" });
  });

  it("never draws past a full bar", () => {
    expect(restoreMeter(job({ bytes_done: 2500, bytes_total: 2000 })).pct).toBe(100);
  });
});

describe("restoreActive", () => {
  it("holds while the request is out, while the row is restoring, and while a job is reported", () => {
    expect(restoreActive(srv({ state: "offline" }), true)).toBe(true);
    expect(restoreActive(srv({ state: "restoring" }), false)).toBe(true);
    expect(restoreActive(srv({ state: "offline", restore: job({}) }), false)).toBe(true);
    expect(restoreActive(srv({ state: "offline" }), false)).toBe(false);
    expect(restoreActive(null, false)).toBe(false);
  });
});

describe("restoreOutcome", () => {
  const watch = { serverId: "srv-1", backupId: ARCHIVE.id };

  it("waits while the server is still restoring", () => {
    expect(restoreOutcome(watch, srv({ state: "restoring", restore: job({}) }), [ARCHIVE])).toBeNull();
  });

  it("names the archive a restore that landed put back", () => {
    expect(restoreOutcome(watch, srv({ state: "offline" }), [ARCHIVE])).toEqual({
      kind: "done",
      name: "manual-2026-09-24",
      reason: "",
    });
  });

  it("carries the agent's reason for a restore that failed", () => {
    const failed = srv({
      state: "offline",
      last_error: 'restore failed: docker: restore stopped at "savegame"; the live tree was rolled back',
    });
    expect(restoreOutcome(watch, failed, [ARCHIVE])).toEqual({
      kind: "failed",
      name: "manual-2026-09-24",
      reason: 'docker: restore stopped at "savegame"; the live tree was rolled back',
    });
  });

  it("does not read an install's old failure as the restore's", () => {
    const sv = srv({ state: "install_failed", last_error: "install failed: steamcmd exited 8" });
    expect(restoreOutcome(watch, sv, [ARCHIVE])?.kind).toBe("done");
  });
});

describe("backupRestore", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    depth.open = true;
    depth.serverId = "srv-1";
    depth.server = srv({ state: "offline" });
    depth.backups = [ARCHIVE];
    depth.restoringBackup = null;
    depth.restoreWatch = null;
    depth.restoreNote = null;
    depth.error = null;
  });

  afterEach(() => {
    surface();
    vi.useRealTimers();
  });

  it("takes the 202's restoring server at once, refuses a second restore, and says how it ended", async () => {
    restoreBackup.mockResolvedValueOnce(srv({ state: "restoring", restore: job({ phase: "opening" }) }));
    await backupRestore(ARCHIVE);
    expect(restoreBackup).toHaveBeenCalledTimes(1);
    expect(depth.server?.state).toBe("restoring");
    expect(depth.restoreWatch).toEqual({ serverId: "srv-1", backupId: ARCHIVE.id });

    // The button is disabled while this holds; the action refuses on its own too.
    await backupRestore(ARCHIVE);
    expect(restoreBackup).toHaveBeenCalledTimes(1);

    // The ledger poll reads the meter...
    getServer.mockResolvedValueOnce(srv({ state: "restoring", restore: job({ bytes_done: 500, bytes_total: 1000 }) }));
    await vi.advanceTimersByTimeAsync(2000);
    expect(restoreMeter(depth.server?.restore).pct).toBe(50);
    expect(depth.restoreNote).toBeNull();

    // ...and the fleet poll can be the one that sees it land.
    fleet.servers = [srv({ state: "offline" })];
    syncDepthFromFleet();
    expect(depth.restoreNote).toEqual({ kind: "done", name: "manual-2026-09-24", reason: "" });
    expect(depth.restoreWatch).toBeNull();
  });

  it("puts a refusal on screen and leaves nothing watched", async () => {
    const { ApiError } = await import("@/api/client");
    restoreBackup.mockRejectedValueOnce(new ApiError(409, "a restore is already in progress for this server", "restore_in_progress"));
    await backupRestore(ARCHIVE);
    expect(depth.error).toBe("a restore is already in progress for this server");
    expect(depth.restoreWatch).toBeNull();
    expect(depth.restoringBackup).toBeNull();
  });

  it("adopts a restore it finds in flight when the drill-in opens mid-restore", async () => {
    fleet.servers = [srv({ state: "restoring", restore: job({}) })];
    syncDepthFromFleet();
    expect(depth.restoreWatch).toEqual({ serverId: "srv-1", backupId: ARCHIVE.id });
    getServer.mockResolvedValueOnce(srv({ state: "offline", last_error: "restore failed: gzip: invalid header; the live tree was not touched" }));
    await vi.advanceTimersByTimeAsync(2000);
    expect(depth.restoreNote).toEqual({
      kind: "failed",
      name: "manual-2026-09-24",
      reason: "gzip: invalid header; the live tree was not touched",
    });
  });
});

describe("the fleet card's restoring state", () => {
  it("reads like installing: a stopped chip, a note saying why start waits, and the fast poll", () => {
    const sv = srv({ state: "restoring" });
    expect(chipKind(sv.state)).toBe("stop");
    expect(deadNote(sv)).toBe("restoring a backup — start waits for it");
    expect(fleetPollMs([sv])).toBe(fleetPollMs([srv({ state: "installing" })]));
    expect(fleetPollMs([sv])).toBeLessThan(fleetPollMs([srv({ state: "offline" })]));
  });
});
