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
const createBackup = vi.fn();

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ...actual,
    api: {
      restoreBackup: (...a: unknown[]) => restoreBackup(...a),
      getServer: (...a: unknown[]) => getServer(...a),
      listBackups: (...a: unknown[]) => listBackups(...a),
      createBackup: (...a: unknown[]) => createBackup(...a),
    },
  };
});

vi.mock("./fleet.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./fleet.svelte")>();
  return { ...actual, refreshFleet: vi.fn(async () => {}) };
});

import {
  backupCreate,
  backupRestore,
  depth,
  restoreActive,
  restoreMeter,
  restoreNoteText,
  restoreOutcome,
  surface,
  syncDepthFromFleet,
} from "./depth.svelte";
import { fmtWhen } from "./fmt";
import { fleet, fleetPollMs } from "./fleet.svelte";
import { chipKind, deadNote } from "./views.svelte";
import type { Backup, RestoreProgress, RestoreResult, Server } from "@/api/types";

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
    expect(restoreMeter(job({ bytes_done: 0, bytes_total: 2000 }))).toEqual({
      pct: 0,
      sized: true,
      numeric: true,
      phase: "extracting",
      label: "0%",
    });
  });

  it("reads half the archive as 50 %", () => {
    expect(restoreMeter(job({ bytes_done: 1000, bytes_total: 2000 }))).toEqual({
      pct: 50,
      sized: true,
      numeric: true,
      phase: "extracting",
      label: "50%",
    });
  });

  it("never prints a number for an unsized restore, and never fills the bar", () => {
    // An old agent restoring through the unary call: the Panel knows a restore
    // is running and nothing else.
    const m = restoreMeter(job({ phase: "restoring", bytes_done: 0, bytes_total: 0 }));
    expect(m).toEqual({ pct: 0, sized: false, numeric: false, phase: "restoring", label: "restoring" });
    // Bytes without a total are not a percentage either: the row narrates.
    const bytesOnly = restoreMeter(job({ bytes_done: 4096, bytes_total: 0 }));
    expect(bytesOnly.pct).toBe(0);
    expect(bytesOnly.numeric).toBe(false);
    expect(bytesOnly.phase).toBe("extracting");
    expect(bytesOnly.label).toBe("extracting");
  });

  it("names the phase outside extraction and keeps the fill it earned", () => {
    // A sized "applying" shows the phase word, never the number beside it —
    // but the fill keeps the 100 % the extraction earned.
    expect(restoreMeter(job({ phase: "applying", bytes_done: 2000, bytes_total: 2000 }))).toEqual({
      pct: 100,
      sized: true,
      numeric: false,
      phase: "applying",
      label: "applying",
    });
    expect(restoreMeter(job({ phase: "opening", bytes_total: 2000 })).numeric).toBe(false);
    expect(restoreMeter(job({ phase: "opening" })).label).toBe("opening");
    expect(restoreMeter(undefined)).toEqual({ pct: 0, sized: false, numeric: false, phase: "opening", label: "opening" });
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
  const STARTED = "2026-09-24T12:00:00Z";
  const watch = { serverId: "srv-1", backupId: ARCHIVE.id, since: STARTED };
  const result = (p: Partial<RestoreResult>): RestoreResult => ({
    backup_id: ARCHIVE.id,
    ok: true,
    finished_at: "2026-09-24T12:03:00Z",
    ...p,
  });

  it("waits while the server is still restoring", () => {
    expect(restoreOutcome(watch, srv({ state: "restoring", restore: job({}) }), [ARCHIVE])).toBeNull();
  });

  it("names the archive a restore that landed put back", () => {
    expect(restoreOutcome(watch, srv({ state: "offline", restore_result: result({}) }), [ARCHIVE])).toEqual({
      kind: "done",
      name: "manual-2026-09-24",
      when: ARCHIVE.created_ms,
      reason: "",
    });
  });

  it("carries the agent's reason for a restore that failed", () => {
    const failed = srv({
      state: "offline",
      restore_result: result({ ok: false, error: 'docker: restore stopped at "savegame"; the live tree was rolled back' }),
    });
    expect(restoreOutcome(watch, failed, [ARCHIVE])).toEqual({
      kind: "failed",
      name: "manual-2026-09-24",
      when: ARCHIVE.created_ms,
      reason: 'docker: restore stopped at "savegame"; the live tree was rolled back',
    });
  });

  it("falls back to the id, with no date, for an archive no longer listed", () => {
    const note = restoreOutcome(watch, srv({ state: "offline", restore_result: result({}) }), []);
    expect(note).toEqual({ kind: "done", name: ARCHIVE.id, when: 0, reason: "" });
  });

  it("never reads last_error: an install_failed server's own reason is not the restore's", () => {
    const landed = srv({
      state: "install_failed",
      last_error: "install failed: steamcmd exited 8",
      restore_result: result({}),
    });
    expect(restoreOutcome(watch, landed, [ARCHIVE])?.kind).toBe("done");
    // ...and a last_error that merely looks like a restore failure is not one.
    const stale = srv({ state: "offline", last_error: "restore failed: an older restore", restore_result: result({}) });
    expect(restoreOutcome(watch, stale, [ARCHIVE])?.kind).toBe("done");
  });

  it("settles a restore that finished within the millisecond it began", () => {
    // The Panel writes nanoseconds; Date.parse keeps milliseconds, so these two
    // compare equal.
    const w = { ...watch, since: "2026-09-24T12:00:00.123456789Z" };
    const quick = srv({ state: "offline", restore_result: result({ finished_at: "2026-09-24T12:00:00.123999999Z" }) });
    expect(restoreOutcome(w, quick, [ARCHIVE])?.kind).toBe("done");
  });

  it("does not settle on a result from before the watch began", () => {
    // A fleet read issued before the POST, landing after the 202: the row is
    // offline with no job — and with the PREVIOUS restore's result on it.
    const earlier = srv({ state: "offline", restore_result: result({ finished_at: "2026-09-24T11:00:00Z" }) });
    expect(restoreOutcome(watch, earlier, [ARCHIVE])).toBeNull();
    // Or with no result at all.
    expect(restoreOutcome(watch, srv({ state: "offline" }), [ARCHIVE])).toBeNull();
  });
});

describe("restoreNoteText", () => {
  const when = ARCHIVE.created_ms;
  const stamp = fmtWhen(when);

  it("names the archive the way its row did, and the next step on a stopped server", () => {
    expect(restoreNoteText({ kind: "done", name: "nightly", when, reason: "" }, true)).toBe(
      `restored ${stamp} · nightly — start the server when ready`,
    );
  });

  it("offers no start when the server is not stopped", () => {
    // a restore that put the row back to crashed or install_failed
    expect(restoreNoteText({ kind: "done", name: "nightly", when, reason: "" }, false)).toBe(`restored ${stamp} · nightly`);
  });

  it("follows a failure's dash with the agent's reason", () => {
    expect(restoreNoteText({ kind: "failed", name: "nightly", when, reason: "gzip: invalid header" }, true)).toBe(
      `restore of ${stamp} · nightly failed — gzip: invalid header`,
    );
  });

  it("drops the date it does not have", () => {
    expect(restoreNoteText({ kind: "done", name: "1700__nightly", when: 0, reason: "" }, false)).toBe("restored 1700__nightly");
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

  const STARTED = "2026-09-24T12:00:00Z";
  const landed = (p: Partial<Server> = {}) =>
    srv({ state: "offline", restore_result: { backup_id: ARCHIVE.id, ok: true, finished_at: "2026-09-24T12:03:00Z" }, ...p });

  it("takes the 202's restoring server at once, refuses a second restore, and says how it ended", async () => {
    restoreBackup.mockResolvedValueOnce(srv({ state: "restoring", restore: job({ phase: "opening", started_at: STARTED }) }));
    await backupRestore(ARCHIVE);
    expect(restoreBackup).toHaveBeenCalledTimes(1);
    expect(depth.server?.state).toBe("restoring");
    expect(depth.restoreWatch).toEqual({ serverId: "srv-1", backupId: ARCHIVE.id, since: STARTED });

    // The button is disabled while this holds; the action refuses on its own too.
    await backupRestore(ARCHIVE);
    expect(restoreBackup).toHaveBeenCalledTimes(1);

    // The ledger poll reads the meter...
    getServer.mockResolvedValueOnce(srv({ state: "restoring", restore: job({ bytes_done: 500, bytes_total: 1000 }) }));
    await vi.advanceTimersByTimeAsync(2000);
    expect(restoreMeter(depth.server?.restore).pct).toBe(50);
    expect(depth.restoreNote).toBeNull();

    // ...and the fleet poll can be the one that sees it land.
    fleet.servers = [landed()];
    syncDepthFromFleet();
    expect(depth.restoreNote).toEqual({ kind: "done", name: "manual-2026-09-24", when: ARCHIVE.created_ms, reason: "" });
    expect(depth.restoreWatch).toBeNull();
  });

  it("does not claim the restore landed off a fleet read that predates it", async () => {
    restoreBackup.mockResolvedValueOnce(srv({ state: "restoring", restore: job({ started_at: STARTED }) }));
    await backupRestore(ARCHIVE);
    // The fleet tick that was already in flight when the POST went out.
    fleet.servers = [
      srv({ state: "offline", restore_result: { backup_id: "older", ok: true, finished_at: "2026-09-23T08:00:00Z" } }),
    ];
    syncDepthFromFleet();
    expect(depth.restoreNote).toBeNull();
    expect(depth.restoreWatch).not.toBeNull();
    // The real end still settles it.
    fleet.servers = [landed()];
    syncDepthFromFleet();
    expect(depth.restoreNote?.kind).toBe("done");
  });

  it("has no dismiss: the next backup or restore action replaces the note", async () => {
    // The mock draws the outcome note with no control; it is spoken once and
    // the ledger's next act is what clears it.
    depth.restoreNote = { kind: "done", name: "manual-2026-09-24", when: ARCHIVE.created_ms, reason: "" };
    createBackup.mockResolvedValueOnce(undefined);
    listBackups.mockResolvedValue([ARCHIVE]);
    await backupCreate();
    expect(createBackup).toHaveBeenCalledTimes(1);
    expect(depth.restoreNote).toBeNull();

    depth.restoreNote = { kind: "failed", name: "manual-2026-09-24", when: ARCHIVE.created_ms, reason: "x" };
    restoreBackup.mockResolvedValueOnce(srv({ state: "restoring", restore: job({ started_at: STARTED }) }));
    await backupRestore(ARCHIVE);
    expect(depth.restoreNote).toBeNull();
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
    fleet.servers = [srv({ state: "restoring", restore: job({ started_at: STARTED }) })];
    syncDepthFromFleet();
    expect(depth.restoreWatch).toEqual({ serverId: "srv-1", backupId: ARCHIVE.id, since: STARTED });
    getServer.mockResolvedValueOnce(
      landed({ restore_result: { backup_id: ARCHIVE.id, ok: false, error: "gzip: invalid header; the live tree was not touched", finished_at: "2026-09-24T12:01:00Z" } }),
    );
    await vi.advanceTimersByTimeAsync(2000);
    expect(depth.restoreNote).toEqual({
      kind: "failed",
      name: "manual-2026-09-24",
      when: ARCHIVE.created_ms,
      reason: "gzip: invalid header; the live tree was not touched",
    });
  });

  it("does nothing once the drill-in has closed under a late answer", async () => {
    let answer: (s: Server) => void = () => {};
    restoreBackup.mockReturnValueOnce(new Promise<Server>((res) => (answer = res)));
    const pending = backupRestore(ARCHIVE);
    surface(); // the operator surfaces while the POST is out
    answer(srv({ state: "restoring", restore: job({ started_at: STARTED }) }));
    await pending;
    expect(depth.restoreWatch).toBeNull();
    await vi.advanceTimersByTimeAsync(6000);
    expect(getServer).not.toHaveBeenCalled();
  });

  it("stops polling when a tick returns after the drill-in closed", async () => {
    restoreBackup.mockResolvedValueOnce(srv({ state: "restoring", restore: job({ started_at: STARTED }) }));
    await backupRestore(ARCHIVE);
    let answer: (s: Server) => void = () => {};
    getServer.mockReturnValueOnce(new Promise<Server>((res) => (answer = res)));
    await vi.advanceTimersByTimeAsync(2000); // the tick is now waiting on getServer
    expect(getServer).toHaveBeenCalledTimes(1);
    surface();
    depth.server = null;
    answer(srv({ state: "restoring", restore: job({ bytes_done: 1, bytes_total: 2 }) }));
    await vi.advanceTimersByTimeAsync(0);
    expect(depth.server).toBeNull(); // the late answer was not applied
    await vi.advanceTimersByTimeAsync(6000);
    expect(getServer).toHaveBeenCalledTimes(1);
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
