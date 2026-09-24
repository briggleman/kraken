// The container-drift badge's retire action and the pending-removal line
// (#354). The badge used to name an orphan and offer nothing; these pin the
// wiring from a drift reading to the Panel call it now makes, the plain (not
// typed) confirmation that gates it, what a refusal leaves on the band, and
// the quiet count a node carries while it owes removals.

import { beforeEach, describe, expect, it, vi } from "vitest";

const retireNodeContainer = vi.fn();
const refreshFleet = vi.fn();

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ApiError: actual.ApiError,
    errMsg: actual.errMsg,
    api: { retireNodeContainer: (...a: unknown[]) => retireNodeContainer(...a) },
  };
});

vi.mock("./fleet.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./fleet.svelte")>();
  return { ...actual, refreshFleet: () => refreshFleet() };
});

import { ApiError } from "@/api/client";
import { fleet } from "./fleet.svelte";
import { openRetire, openRetireAll, retire } from "./retire.svelte";
import { CD_CONTAINER_BODY, CD_SERVER_BODY, confirmGo, openConfirm, ui } from "./state.svelte";
import { containerDrift, pendingRemovalsNote, retirable } from "./views.svelte";
import type { Node, Server } from "@/api/types";

const NODE_ID = "node-1";

function node(extra: Partial<Node> = {}): Node {
  return { id: NODE_ID, name: "abyss-win", status: "online", agent_version: "0.55.0", ...extra } as Node;
}

function server(id: string, state: Server["state"]): Server {
  return {
    id,
    name: id + "-name",
    spec_id: "spec-1",
    node_id: NODE_ID,
    kind: "windows-native",
    state,
    vars: {},
    ports: { game: 27015 },
    memory_mb: 4096,
    created_at: "2026-09-17T09:00:00Z",
  };
}

const ORPHAN = { server_id: "f4030778", container_name: "kraken_f4030778" };

beforeEach(() => {
  vi.clearAllMocks();
  fleet.servers = [];
  ui.confirm = null;
  retire.errors = {};
  retire.busy = {};
});

describe("retirable", () => {
  it("offers the named untracked containers", () => {
    fleet.servers = [server("a", "running")];
    const drift = containerDrift(
      node({
        running_servers: 2,
        managed_containers: [{ server_id: "a", container_name: "kraken_a" }, ORPHAN],
      }),
    );
    expect(retirable(drift)).toEqual([{ server_id: "f4030778", label: "kraken_f4030778" }]);
  });

  it("offers nothing for a missing container — there is no container to retire", () => {
    fleet.servers = [server("a", "running"), server("b", "running")];
    const drift = containerDrift(
      node({ running_servers: 1, managed_containers: [{ server_id: "a", container_name: "kraken_a" }] }),
    );
    expect(drift?.word).toBe("missing");
    expect(retirable(drift)).toEqual([]);
  });

  it("offers nothing when an older agent reports only a count", () => {
    fleet.servers = [server("a", "running")];
    expect(retirable(containerDrift(node({ running_servers: 2 })))).toEqual([]);
  });

  it("offers nothing when the accounts agree", () => {
    expect(retirable(undefined)).toEqual([]);
  });
});

describe("openRetire", () => {
  it("asks a plain confirm that names the container and says the data stays", () => {
    openRetire(NODE_ID, { server_id: ORPHAN.server_id, label: ORPHAN.container_name }, null);
    expect(ui.confirm).toMatchObject({
      name: "kraken_f4030778",
      noun: "container",
      verb: "retire",
      typed: false,
      body: CD_CONTAINER_BODY,
    });
    expect(CD_CONTAINER_BODY).toMatch(/world, config and backups stay/);
    // Opening is not doing: nothing reaches the Panel until it is confirmed.
    expect(retireNodeContainer).not.toHaveBeenCalled();
  });

  it("retires that container on that node when confirmed, then refreshes", async () => {
    retireNodeContainer.mockResolvedValue(undefined);
    openRetire(NODE_ID, { server_id: ORPHAN.server_id, label: ORPHAN.container_name }, null);
    await confirmGo();
    expect(retireNodeContainer).toHaveBeenCalledTimes(1);
    expect(retireNodeContainer).toHaveBeenCalledWith(NODE_ID, "f4030778");
    expect(refreshFleet).toHaveBeenCalledTimes(1);
    expect(ui.confirm).toBeNull();
    expect(retire.errors[NODE_ID]).toBeUndefined();
    expect(retire.busy).toEqual({});
  });

  it("puts a refusal on the node's band instead of swallowing it", async () => {
    retireNodeContainer.mockRejectedValue(new ApiError(503, "node abyss-win is unreachable: connection refused"));
    openRetire(NODE_ID, { server_id: ORPHAN.server_id, label: ORPHAN.container_name }, null);
    await confirmGo();
    expect(retire.errors[NODE_ID]).toBe("node abyss-win is unreachable: connection refused");
    expect(refreshFleet).toHaveBeenCalledTimes(1);
    expect(retire.busy).toEqual({});
  });

  it("retire all retires each container, and stops at the first refusal", async () => {
    retireNodeContainer
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new ApiError(409, "server is managed by the Panel"));
    const items = ["a", "b", "c", "d"].map((id) => ({ server_id: id, label: "kraken_" + id }));
    openRetireAll(NODE_ID, items, null);
    expect(ui.confirm).toMatchObject({ noun: "containers", verb: "retire", typed: false });
    await confirmGo();
    expect(retireNodeContainer.mock.calls).toEqual([
      [NODE_ID, "a"],
      [NODE_ID, "b"],
    ]);
    expect(retire.errors[NODE_ID]).toBe("server is managed by the Panel");
  });
});

describe("the delete confirmation", () => {
  it("no longer claims to remove backups", () => {
    expect(CD_SERVER_BODY).not.toMatch(/world, backups/);
    expect(CD_SERVER_BODY).toMatch(/backups stay on the node/);
  });

  it("stays typed by default", () => {
    openConfirm("valheim", null, { noun: "server" });
    expect(ui.confirm).toMatchObject({ verb: "delete", typed: true, body: CD_SERVER_BODY });
  });
});

describe("pendingRemovalsNote", () => {
  it("is silent when the node owes nothing", () => {
    expect(pendingRemovalsNote(node())).toBeUndefined();
    expect(pendingRemovalsNote(node({ pending_removals: [] }))).toBeUndefined();
  });

  it("counts what the node owes and names each removal with how it last went", () => {
    const note = pendingRemovalsNote(
      node({
        status: "offline",
        pending_removals: [
          { server_id: "f4030778", delete_data: true, requested_at: "2026-09-23T10:00:00Z", attempts: 3, last_error: "node unreachable" },
          { server_id: "8f8d725c", delete_data: false, requested_at: "2026-09-23T10:05:00Z", attempts: 1 },
        ],
      }),
    );
    // Shown for an offline node too: an unreachable node is the usual reason.
    expect(note?.count).toBe(2);
    expect(note?.title).toContain("f4030778 — container and data, 3 attempts: node unreachable");
    expect(note?.title).toContain("8f8d725c — container, 1 attempt");
  });
});
