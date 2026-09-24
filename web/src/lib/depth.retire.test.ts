// The delete button on a live server retires it (#360). These pin the wiring:
// the typed confirmation's go calls POST /retire with the final backup on —
// never the permanent DELETE, which a live server refuses — the warning says
// the backup comes first, and a retired server leaves the fleet grid.

import { beforeEach, describe, expect, it, vi } from "vitest";

const retireServer = vi.fn();
const deleteServer = vi.fn();
const refreshFleet = vi.fn(async () => {});

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ...actual,
    api: {
      retireServer: (...a: unknown[]) => retireServer(...a),
      deleteServer: (...a: unknown[]) => deleteServer(...a),
    },
  };
});

vi.mock("./fleet.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./fleet.svelte")>();
  return { ...actual, refreshFleet: () => refreshFleet() };
});

import { ApiError } from "@/api/client";
import { depth } from "./depth.svelte";
import { fleet, gridServers } from "./fleet.svelte";
import { CD_SERVER_BODY, confirmGo, openConfirm, ui } from "./state.svelte";
import { chipKind, serverMeta } from "./views.svelte";
import type { Server } from "@/api/types";

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
    retireServer.mockResolvedValueOnce(server("sv-live", "offline", { retire: { phase: "stopping", final_backup: true, started_at: "" } }));
    openConfirm("valheim", null, { noun: "server" });
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

  it("warns that a final backup is taken first, and that the backups are kept", () => {
    expect(CD_SERVER_BODY).toMatch(/^a final backup is taken first/);
    expect(CD_SERVER_BODY).toMatch(/its backups are kept/);
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
