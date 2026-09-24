// The delete button on a live server retires it (#360). These pin the wiring:
// the typed confirmation's go calls POST /retire with the final backup on —
// never the permanent DELETE, which a live server refuses — the warning says
// the backup comes first, and a retired server leaves the fleet grid.

import { beforeEach, describe, expect, it, vi } from "vitest";

const retireServer = vi.fn();
const deleteServer = vi.fn();
const deleteSpec = vi.fn();
const refreshFleet = vi.fn(async () => {});

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ...actual,
    api: {
      retireServer: (...a: unknown[]) => retireServer(...a),
      deleteServer: (...a: unknown[]) => deleteServer(...a),
      deleteSpec: (...a: unknown[]) => deleteSpec(...a),
    },
  };
});

vi.mock("./fleet.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./fleet.svelte")>();
  return { ...actual, refreshFleet: () => refreshFleet() };
});

import { ApiError } from "@/api/client";
import { bootDeepLinks, depth, powerControls } from "./depth.svelte";
import { fleet, fleetPollMs, gridServers } from "./fleet.svelte";
import { CD_SERVER_BODY, confirmGo, confirmWord, openConfirm, ui } from "./state.svelte";
import { chipKind, deadNote, pendingRemovalsNote, removalKind, serverMeta } from "./views.svelte";
import type { Node, PendingRemoval, Server, Spec } from "@/api/types";

function server(id: string, state: Server["state"], extra: Partial<Server> = {}): Server {
  return {
    id,
    name: id,
    spec_id: "spec-1",
    node_id: state === "retired" ? "" : "node-1",
    kind: "linux-native",
    state,
    vars: {},
    ports: state === "retired" ? {} : { game: 27015 },
    memory_mb: 4096,
    created_at: "2026-09-24T09:00:00Z",
    ...extra,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  fleet.servers = [];
  ui.confirm = null;
  depth.serverId = "sv-live";
  depth.error = null;
});

describe("the delete button", () => {
  it("retires with the final backup and never deletes permanently", async () => {
    retireServer.mockResolvedValueOnce(server("sv-live", "offline", { retire: { phase: "stopping", final_backup: "requested", started_at: "" } }));
    openConfirm("valheim", null, { noun: "server", verb: "retire" });
    await confirmGo();
    expect(retireServer).toHaveBeenCalledWith("sv-live", true);
    expect(deleteServer).not.toHaveBeenCalled();
    expect(refreshFleet).toHaveBeenCalled();
    expect(ui.confirm).toBeNull();
  });

  it("keeps the drill-in and says why when the Panel refuses", async () => {
    retireServer.mockRejectedValueOnce(
      new ApiError(409, "this server is installing; retire it once the install finishes", "server_busy"),
    );
    openConfirm("valheim", null, { noun: "server" });
    await confirmGo();
    expect(depth.error).toMatch(/installing/);
  });

  it("says what a retire does, in order, and that it can be undone", () => {
    expect(CD_SERVER_BODY).toMatch(/^retiring stops this server, takes a final backup, then removes its world and config/);
    expect(CD_SERVER_BODY).toMatch(/its backups are kept, and it can be revived later from any of them\.$/);
    expect(CD_SERVER_BODY).not.toMatch(/cannot be undone/);
  });

  it("asks for the word on its button: retire for a server, delete for the rest", () => {
    expect(confirmWord({ verb: "retire" })).toBe("retire");
    expect(confirmWord({ verb: "delete" })).toBe("delete");
    expect(confirmWord(null)).toBe("delete");
  });
});

describe("retiring", () => {
  it("offers no power controls while retiring or once retired", () => {
    expect(powerControls("retiring")).toBe("none");
    expect(powerControls("retired")).toBe("none");
    expect(powerControls("running")).toBe("stop");
    expect(powerControls("offline")).toBe("start");
  });

  it("is a transient state the fleet polls quickly, with a card note of its own", () => {
    expect(fleetPollMs([server("a", "retiring")])).toBeLessThan(fleetPollMs([server("a", "offline")]));
    expect(deadNote(server("a", "retiring"))).toMatch(/^retiring/);
  });
});

describe("a pending removal's hover text", () => {
  function owed(p: Partial<PendingRemoval>): PendingRemoval {
    return { server_id: "sv-x", delete_data: true, requested_at: "", attempts: 1, ...p };
  }

  it("names a retire's, a permanent delete's and a plain delete's apart", () => {
    fleet.servers = [server("sv-x", "retired")];
    expect(removalKind(owed({}))).toBe("retired");
    expect(removalKind(owed({ delete_backups: true }))).toBe("deleted for good");
    fleet.servers = [];
    expect(removalKind(owed({}))).toBe("deleted");
  });

  it("carries the kind on each line, first among its facts", () => {
    fleet.servers = [server("sv-x", "retired")];
    const note = pendingRemovalsNote({ id: "n", pending_removals: [owed({})] } as Node);
    // the retired row keeps its name, so the line is named by it
    expect(note?.title).toMatch(/\(retired · container and data · 1 attempt\)/);
  });
});

describe("a deep link to a retired server", () => {
  it("lands on the fleet instead of an empty drill-in", async () => {
    depth.open = false;
    history.pushState(null, "", "/servers/0000aaaa-0000-0000-0000-00000000beef");
    fleet.servers = [server("0000aaaa-0000-0000-0000-00000000beef", "retired")];
    await bootDeepLinks();
    expect(depth.open).toBe(false);
    expect(location.pathname).toBe("/");
  });
});

describe("a refused spec delete", () => {
  it("keeps the editor open and says why", async () => {
    fleet.specs = [{ id: "spec-1", name: "Valheim" } as Spec];
    ui.open = { specEdit: { ox: "50%", oy: "50%" } };
    deleteSpec.mockRejectedValueOnce(
      new ApiError(409, "1 server uses this spec (1 retired) — revive or delete them for good first", "spec_in_use"),
    );
    openConfirm("Valheim", null, { noun: "spec" });
    await confirmGo();
    expect(ui.specError).toMatch(/1 retired/);
    expect(ui.open.specEdit).toBeDefined();
  });
});

describe("a retired server", () => {
  it("is left out of the fleet grid, and nothing else is", () => {
    const all = [server("a", "running"), server("b", "retired"), server("c", "restoring"), server("d", "install_failed")];
    expect(gridServers(all).map((s) => s.id)).toEqual(["a", "c", "d"]);
  });

  it("reads without crashing wherever a state is read", () => {
    const sv = server("r", "retired", { retired_from_node_id: "node-1", retired_ports: { game: 27015 } });
    expect(chipKind(sv.state)).toBe("stop");
    expect(serverMeta(sv)).toMatch(/retired$/);
  });
});
