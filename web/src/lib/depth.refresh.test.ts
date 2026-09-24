// Opening the drill-in fires one detail refresh — eight reads, one of which (the
// file listing) reaches the node and is slow. A power refusal does not reach the
// node at all, so Start pressed right after opening gets its 409 back first.
// The regression this pins: the refresh landing afterwards and blanking the
// notice with its own "no errors", so the refusal flashed and vanished.

import { beforeEach, describe, expect, it, vi } from "vitest";

const powerServer = vi.fn();
const listFiles = vi.fn();
const getServer = vi.fn();

// ApiError and errMsg are the real ones: what the notice says here is what it
// says in the app. Every read but the two under test answers at once.
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

// power() refreshes the fleet after it acts; nothing here is about the fleet.
vi.mock("./fleet.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./fleet.svelte")>();
  return { ...actual, refreshFleet: vi.fn(async () => {}) };
});

// No socket: the console stream is not what this is about, and jsdom would
// only fail to connect it.
vi.mock("./stream.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./stream.svelte")>();
  class QuietStream {
    lines: unknown[] = [];
    set() {}
  }
  return { ...actual, ServerStream: QuietStream };
});

import { ApiError } from "@/api/client";
import { depth, openDepth, power } from "./depth.svelte";

const REFUSAL = "Owner Player ID is required before this server can start — set it on the Settings tab";

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

beforeEach(() => {
  vi.clearAllMocks();
  depth.powerBusy = false;
  depth.error = null;
  getServer.mockResolvedValue({ id: "srv-1", state: "offline" });
});

describe("detail refresh vs a power refusal", () => {
  it("keeps a refusal that arrived while the refresh was in flight", async () => {
    const files = held<unknown>();
    listFiles.mockReturnValueOnce(files.promise);
    openDepth("srv-1", 0, 0);

    // Start, straight away: refused before the node is contacted.
    powerServer.mockRejectedValueOnce(new ApiError(409, REFUSAL, "required_settings_missing"));
    await power("start");
    expect(depth.error).toBe(REFUSAL);

    // Now the slow read lands, cleanly. Its "no errors" is about the reads, and
    // must not overwrite a notice put up since.
    files.resolve({ path: ".", entries: [] });
    await flush();
    expect(depth.error).toBe(REFUSAL);
  });

  it("keeps a refusal even when the refresh itself failed", async () => {
    const files = held<unknown>();
    listFiles.mockReturnValueOnce(files.promise);
    openDepth("srv-1", 0, 0);

    powerServer.mockRejectedValueOnce(new ApiError(409, REFUSAL, "required_settings_missing"));
    await power("start");

    files.reject(new Error("node reef-01 did not answer"));
    await flush();
    expect(depth.error).toBe(REFUSAL);
  });

  it("still reports the refresh's own failure when nothing else spoke", async () => {
    const files = held<unknown>();
    listFiles.mockReturnValueOnce(files.promise);
    openDepth("srv-1", 0, 0);

    files.reject(new Error("node reef-01 did not answer"));
    await flush();
    expect(depth.error).toBe("node reef-01 did not answer");
  });
});
