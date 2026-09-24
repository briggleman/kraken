// The drill-in with App's fleet-sync effect mounted: the same `followFleet`
// App hands to $effect. The store-level tests beside this one (depth.stale) run
// with no effect at all, and that hid the half of #368 that lived in App. The
// effect used to call syncDepthFromFleet bare, so it re-ran on every read that
// function makes: depth.open, depth.serverId, depth.server, stream.lines. Every
// open and every other write of depth.server was then followed by the fleet
// row being pushed back over it. The refresh's own read never applied, and the
// restore POST's and the restore poll's writes were undone on the next flush.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushSync } from "svelte";

const listFiles = vi.fn();
const getServer = vi.fn();
const restoreBackup = vi.fn();

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  const ok = (v: unknown) => async () => v;
  return {
    ApiError: actual.ApiError,
    errMsg: actual.errMsg,
    api: {
      listFiles: (...a: unknown[]) => listFiles(...a),
      getServer: (...a: unknown[]) => getServer(...a),
      restoreBackup: (...a: unknown[]) => restoreBackup(...a),
      listBackups: ok({ backups: [], mirror: "" }),
      listSchedules: ok({ schedules: [] }),
      getServerDns: ok(null),
      getServerSettings: ok({ groups: [], values: {} }),
      getServerSftp: ok(null),
      getInstallLog: ok(null),
    },
  };
});

// The fleet is whatever the test assigns; the restore's own refresh of it is a
// no-op here.
vi.mock("./fleet.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./fleet.svelte")>();
  return { ...actual, refreshFleet: vi.fn(async () => {}) };
});

// A stream with a reactive `lines`, like the real one: the effect used to
// depend on it through syncUpdatePass, and a plain field would hide that.
vi.mock("./stream.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./stream.svelte")>();
  class QuietStream {
    lines = $state<{ text: string }[]>([]);
    set() {}
  }
  return { ...actual, ServerStream: QuietStream };
});

import type { Backup, Server } from "@/api/types";
import { fleet } from "./fleet.svelte";
import { backupRestore, depth, followFleet, openDepth, surface } from "./depth.svelte";

const srv = (state: Server["state"], extra: Partial<Server> = {}) =>
  ({ id: "srv-1", state, ...extra }) as Server;

function held<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

/** Let settled promises continue, then run whatever effects they scheduled. */
async function settle() {
  await new Promise((r) => setTimeout(r, 0));
  flushSync();
}

const LISTING = { path: ".", entries: [] };

let unmount: () => void = () => {};

beforeEach(() => {
  vi.clearAllMocks();
  depth.powerBusy = false;
  depth.restoringBackup = null;
  depth.error = null;
  fleet.servers = [srv("offline")];
  getServer.mockResolvedValue(srv("offline"));
  listFiles.mockResolvedValue(LISTING);
  // App's effect, exactly as App mounts it.
  unmount = $effect.root(() => {
    $effect(followFleet);
  });
  flushSync();
});

afterEach(() => {
  surface(); // stops any ledger poll a restore started
  flushSync();
  unmount();
});

describe("the drill-in under App's fleet-sync effect", () => {
  it("applies the refresh's own read on open", async () => {
    const files = held<unknown>();
    listFiles.mockReturnValueOnce(files.promise);
    // The fleet's row is behind; the refresh's read is the news.
    getServer.mockResolvedValueOnce(srv("crashed"));
    openDepth("srv-1", 0, 0);
    flushSync();

    files.resolve(LISTING);
    await settle();
    expect(depth.server?.state).toBe("crashed");
  });

  it("keeps the restore POST's server rather than reverting to the fleet row", async () => {
    openDepth("srv-1", 0, 0);
    await settle();

    const started = new Date().toISOString();
    restoreBackup.mockResolvedValueOnce(
      srv("restoring", {
        restore: { backup_id: "bk-1", started_at: started, phase: "opening", bytes_done: 0, bytes_total: 0 },
      } as Partial<Server>),
    );
    await backupRestore({ id: "bk-1", name: "nightly" } as Backup);
    flushSync();
    expect(depth.server?.state).toBe("restoring");
    expect(depth.server?.restore?.backup_id).toBe("bk-1");
  });

  it("still lets a fleet poll with a newer state win over the refresh's older read", async () => {
    const files = held<unknown>();
    listFiles.mockReturnValueOnce(files.promise);
    openDepth("srv-1", 0, 0);
    flushSync();

    fleet.servers = [srv("running")];
    flushSync();
    expect(depth.server?.state).toBe("running");

    files.resolve(LISTING);
    await settle();
    expect(depth.server?.state).toBe("running");
  });

  it("does not let a poll that re-delivers the same row discard the refresh's read", async () => {
    const files = held<unknown>();
    listFiles.mockReturnValueOnce(files.promise);
    getServer.mockResolvedValueOnce(srv("crashed"));
    openDepth("srv-1", 0, 0);
    flushSync();

    // A tick with nothing new: a fresh array and a fresh row, the same facts.
    fleet.servers = [srv("offline")];
    flushSync();

    files.resolve(LISTING);
    await settle();
    expect(depth.server?.state).toBe("crashed");
  });
});
