// The retired group's head note under the list's own fleet-sync effect, with
// the real fleet poll (#380). retired.test.ts mocks refreshFleet, so no poll is
// ever issued there and every read's poll number is passed by hand; that hid
// whether the number the effect hands syncRetired (serversReadIssued) really
// names the poll whose servers are on screen. Here the poll runs for real
// against mocked API answers, and `followRetired` is mounted exactly as
// RetiredList mounts it.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushSync } from "svelte";

const listServers = vi.fn();
const deleteServer = vi.fn();

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  const ok = (v: unknown) => async () => v;
  return {
    ...actual,
    api: {
      listServers: () => listServers(),
      listSpecs: ok({ specs: [] }),
      listNodes: ok({ nodes: [{ id: "node-1", name: "behemoth", status: "online" }], panel_version: "" }),
      listAudit: ok({ entries: [] }),
      listBackups: ok({ backups: [], mirror: "" }),
      deleteServer: (...a: unknown[]) => deleteServer(...a),
    },
  };
});

import type { Role, Server } from "@/api/types";
import { auth } from "./auth.svelte";
import { refreshFleet } from "./fleet.svelte";
import { followRetired, purge, resetRetired, retired } from "./retired.svelte";

function server(id: string): Server {
  return {
    id,
    name: id,
    spec_id: "spec-1",
    node_id: "",
    kind: "linux-native",
    state: "retired",
    vars: {},
    ports: {},
    memory_mb: 4096,
    retired_from_node_id: "node-1",
    retired_at: "2026-09-21T03:00:00Z",
    created_at: "2026-09-01T00:00:00Z",
  };
}

const rows = (...ids: string[]) => ({ servers: ids.map(server) });

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

const NOTE = "2 archives on the shared target were kept";

let unmount: () => void = () => {};

beforeEach(async () => {
  vi.resetAllMocks();
  resetRetired();
  auth.role = { permissions: ["*"] } as Role;
  listServers.mockResolvedValueOnce(rows("a", "b"));
  await refreshFleet();
  // RetiredList's effect, exactly as it mounts it.
  unmount = $effect.root(() => {
    $effect(followRetired);
  });
  flushSync();
});

afterEach(() => {
  unmount();
});

describe("the head note under the retired list's fleet-sync effect", () => {
  it("is not cleared by a poll that was already out when the delete answered", async () => {
    // A poll starts, and is slow.
    const early = held<unknown>();
    listServers.mockReturnValueOnce(early.promise);
    const earlyPoll = refreshFleet();

    // The delete answers; purge speaks the note and starts its own poll.
    const own = held<unknown>();
    listServers.mockReturnValueOnce(own.promise);
    deleteServer.mockResolvedValueOnce({ note: NOTE, removal_pending: false });
    const purging = purge("a");
    await settle();
    expect(retired.note).toBe(NOTE);

    // The early poll lands now, still carrying the deleted row: the note stays.
    early.resolve(rows("a", "b"));
    await earlyPoll;
    await settle();
    expect(retired.note).toBe(NOTE);

    // purge's own poll takes the row away and the note stays.
    own.resolve(rows("b"));
    await purging;
    await settle();
    expect(retired.note).toBe(NOTE);

    // A later poll showing the same picture keeps it.
    listServers.mockResolvedValueOnce(rows("b"));
    await refreshFleet();
    await settle();
    expect(retired.note).toBe(NOTE);

    // Another operator retires a server: the note goes.
    listServers.mockResolvedValueOnce(rows("b", "c"));
    await refreshFleet();
    await settle();
    expect(retired.note).toBe("");
  });
});
