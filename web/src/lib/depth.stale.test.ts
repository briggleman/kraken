// Opening the drill-in fires one detail refresh — eight reads, one of which (the
// file listing) reaches the node and is slow. Its getServer read is taken when
// the refresh begins but only applied once all eight settle. A power action
// taken in between refreshes the fleet and puts the newer state on screen
// first. The regression this pins (#368): the refresh landing afterwards and
// putting its older state back — the chip read `running`, then dropped to
// `offline` until the next poll, and the stream was re-targeted with it.

import { beforeEach, describe, expect, it, vi } from "vitest";

const powerServer = vi.fn();
const listFiles = vi.fn();
const getServer = vi.fn();
const refreshFleet = vi.fn();

// Every read but the two under test answers at once.
vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  const ok = (v: unknown) => async () => v;
  return {
    ApiError: actual.ApiError,
    errMsg: actual.errMsg,
    api: {
      powerServer: (...a: unknown[]) => powerServer(...a),
      listFiles: (...a: unknown[]) => listFiles(...a),
      getServer: (...a: unknown[]) => getServer(...a),
      listBackups: ok({ backups: [], mirror: "" }),
      listSchedules: ok({ schedules: [] }),
      getServerDns: ok(null),
      getServerSettings: ok({ groups: [], values: {} }),
      getServerSftp: ok(null),
      getInstallLog: ok(null),
    },
  };
});

// power() refreshes the fleet after it acts; the test decides what that
// refresh reports.
vi.mock("./fleet.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./fleet.svelte")>();
  return { ...actual, refreshFleet: (...a: unknown[]) => refreshFleet(...a) };
});

// No socket, but a record of every re-target: the stream mode is the other
// half of what a stale read used to put back.
const retargets: [string, string][] = [];
vi.mock("./stream.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./stream.svelte")>();
  class RecordingStream {
    lines: unknown[] = [];
    set(id: string, mode: string) {
      retargets.push([id, mode]);
    }
  }
  return { ...actual, ServerStream: RecordingStream };
});

import type { Server } from "@/api/types";
import { fleet } from "./fleet.svelte";
import { depth, openDepth, power, syncDepthFromFleet } from "./depth.svelte";

const srv = (state: Server["state"]) => ({ id: "srv-1", state }) as Server;

/** A promise the test resolves or rejects when it chooses. */
function held<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

/** Let every settled promise's continuation run. */
const flush = () => new Promise((r) => setTimeout(r, 0));

const LISTING = { path: ".", entries: [] };

beforeEach(() => {
  vi.clearAllMocks();
  retargets.length = 0;
  depth.powerBusy = false;
  depth.error = null;
  fleet.servers = [srv("offline")];
  refreshFleet.mockResolvedValue(undefined);
  // The refresh's own read of the server, taken as it began: still offline.
  getServer.mockResolvedValue(srv("offline"));
  listFiles.mockResolvedValue(LISTING);
});

describe("detail refresh vs a newer server state", () => {
  it("keeps the state a power action put up while the refresh was in flight", async () => {
    const files = held<unknown>();
    listFiles.mockReturnValueOnce(files.promise);
    openDepth("srv-1", 0, 0);
    expect(depth.server?.state).toBe("offline");

    // Start, straight away, and it works: the fleet refresh reports running.
    powerServer.mockResolvedValueOnce({ state: "running" });
    refreshFleet.mockImplementationOnce(async () => {
      fleet.servers = [srv("running")];
    });
    await power("start");
    expect(depth.server?.state).toBe("running");

    // Now the slow read lands, and the refresh's older `offline` with it.
    files.resolve(LISTING);
    await flush();
    expect(depth.server?.state).toBe("running");
    // ...and the stream stayed on the live mode the running state implies.
    expect(retargets.at(-1)).toEqual(["srv-1", "live"]);
    // The other reads are not about the state, and still apply.
    expect(depth.files).toEqual(LISTING);
  });

  it("keeps a fleet push that landed while the refresh was in flight", async () => {
    const files = held<unknown>();
    listFiles.mockReturnValueOnce(files.promise);
    openDepth("srv-1", 0, 0);

    // Another operator started it; the fleet poll brings the news.
    fleet.servers = [srv("starting")];
    syncDepthFromFleet();

    files.resolve(LISTING);
    await flush();
    expect(depth.server?.state).toBe("starting");
  });

  it("applies the refresh's read when nothing wrote the state meanwhile", async () => {
    const files = held<unknown>();
    listFiles.mockReturnValueOnce(files.promise);
    // The fleet's cached row is behind; the refresh's own read is the news.
    getServer.mockResolvedValueOnce(srv("crashed"));
    openDepth("srv-1", 0, 0);
    expect(depth.server?.state).toBe("offline");

    files.resolve(LISTING);
    await flush();
    expect(depth.server?.state).toBe("crashed");
    expect(retargets.at(-1)).toEqual(["srv-1", "replay"]);
  });

  it("lets a reopen's refresh win over one still in flight from the last open", async () => {
    const first = held<unknown>();
    listFiles.mockReturnValueOnce(first.promise);
    getServer.mockResolvedValueOnce(srv("offline"));
    openDepth("srv-1", 0, 0);

    // Reopened on the same server; this refresh reads it running and lands.
    getServer.mockResolvedValueOnce(srv("running"));
    openDepth("srv-1", 0, 0);
    await flush();
    expect(depth.server?.state).toBe("running");

    // The first open's refresh lands last, with the older read.
    first.resolve(LISTING);
    await flush();
    expect(depth.server?.state).toBe("running");
  });
});
