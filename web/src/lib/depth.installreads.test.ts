// #387: which install-log read the drill-in applies. A read's answer is applied
// only when it succeeded, only if no newer read has already succeeded, and
// only if it was started after the last time the held log was deliberately
// reset — an open, an install that begins, an install that ends. These cover
// the paths depth.installprev.test.ts does not reach: the detail refresh an
// open fires (whose install-log read waits on the slow file listing), a reopen
// with a read left over, and an install that ends with an older read out.

import { beforeEach, describe, expect, it, vi } from "vitest";

const getInstallLog = vi.fn();
const listFiles = vi.fn();

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  const ok = (v: unknown) => async () => v;
  return {
    ApiError: actual.ApiError,
    errMsg: actual.errMsg,
    api: {
      getInstallLog: (...a: unknown[]) => getInstallLog(...a),
      listFiles: (...a: unknown[]) => listFiles(...a),
      getServer: ok({ id: "srv-1", state: "offline" }),
      listBackups: ok({ backups: [], mirror: "" }),
      listSchedules: ok({ schedules: [] }),
      getServerDns: ok(null),
      getServerSettings: ok({ groups: [], values: {} }),
      getServerSftp: ok(null),
    },
  };
});

// No socket: jsdom would only fail to connect it.
vi.mock("./stream.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./stream.svelte")>();
  class QuietStream {
    lines: unknown[] = [];
    set() {}
  }
  return { ...actual, ServerStream: QuietStream };
});

import { depth, openDepth, refreshInstallLog, surface, syncDepthFromFleet } from "./depth.svelte";
import { fleet } from "./fleet.svelte";
import type { InstallLog, Server } from "@/api/types";

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

function log(tag: string, extra: Partial<InstallLog> = {}): InstallLog {
  return {
    server_id: "srv-1",
    done: true,
    retained: true,
    lines: [{ ts: 1, stream: "install", text: tag }],
    previous: null,
    ...extra,
  };
}

function server(state: Server["state"]): Server {
  return {
    id: "srv-1",
    name: "dragonwilds-01",
    spec_id: "spec-1",
    node_id: "node-1",
    kind: "windows-native",
    state,
    vars: {},
    ports: {},
    memory_mb: 8192,
    created_at: "2026-09-01T00:00:00Z",
  };
}

beforeEach(() => {
  getInstallLog.mockReset();
  listFiles.mockReset();
  listFiles.mockResolvedValue({ path: ".", entries: [] });
  fleet.servers = [server("offline")];
});

describe("the detail refresh's install-log read", () => {
  // A failed read claims nothing, so it cannot shut out an older read still
  // out; here the older read is the refresh's own, held up by the listing.
  it("applies once the listing lands, though a newer read failed meanwhile", async () => {
    const files = held<unknown>();
    listFiles.mockReturnValueOnce(files.promise);
    getInstallLog.mockResolvedValueOnce(log("from the refresh"));
    openDepth("srv-1", 0, 0);

    getInstallLog.mockRejectedValueOnce(new Error("panel unreachable"));
    await refreshInstallLog(); // newer, fails
    expect(depth.installLog).toBeNull();

    files.resolve({ path: ".", entries: [] });
    await flush();
    expect(depth.installLog?.lines[0].text).toBe("from the refresh");
  });

  it("writes nothing when it fails, and a later success applies", async () => {
    getInstallLog.mockRejectedValueOnce(new Error("panel unreachable"));
    openDepth("srv-1", 0, 0);
    await flush();
    expect(depth.installLog).toBeNull();

    getInstallLog.mockResolvedValueOnce(log("later"));
    await refreshInstallLog();
    expect(depth.installLog?.lines[0].text).toBe("later");

    // And a failure after that leaves what the success put there.
    getInstallLog.mockRejectedValueOnce(new Error("panel unreachable"));
    await refreshInstallLog();
    expect(depth.installLog?.lines[0].text).toBe("later");
  });
});

describe("reads left over across a reset", () => {
  // The first open's refresh waits on the listing; the server is closed and
  // opened again, and the new open's read fails. The leftover read holds the
  // log from before the reopen and must not land over it — for good, since
  // nothing reads again until something changes.
  it("ignores a read from an earlier open of the same server", async () => {
    const files = held<unknown>();
    listFiles.mockReturnValueOnce(files.promise);
    getInstallLog.mockResolvedValueOnce(log("before the reopen"));
    openDepth("srv-1", 0, 0);
    surface();

    getInstallLog.mockRejectedValueOnce(new Error("panel unreachable"));
    openDepth("srv-1", 0, 0);
    await flush();
    expect(depth.installLog).toBeNull();

    files.resolve({ path: ".", entries: [] });
    await flush();
    expect(depth.installLog).toBeNull();
  });

  // An install that ends re-reads for its verdict. A read started while it was
  // still running holds a snapshot cut off before the failure lines; if the
  // verdict's re-read fails, that snapshot must not become the log shown.
  it("ignores a mid-install read that lands after the install ended and its re-read failed", async () => {
    getInstallLog.mockResolvedValueOnce(log("held while installing", { done: false }));
    openDepth("srv-1", 0, 0);
    await flush();
    const heldLog = depth.installLog;
    expect(heldLog?.lines[0].text).toBe("held while installing");

    depth.server = server("installing");
    const midInstall = held<InstallLog>();
    getInstallLog.mockReturnValueOnce(midInstall.promise);
    const older = refreshInstallLog();

    getInstallLog.mockRejectedValueOnce(new Error("panel unreachable"));
    fleet.servers = [server("install_failed")];
    syncDepthFromFleet();
    await flush();
    expect(getInstallLog).toHaveBeenCalledTimes(3);

    midInstall.resolve(log("cut off mid-install", { done: false }));
    await older;
    expect(depth.installLog).toBe(heldLog);
  });
});
