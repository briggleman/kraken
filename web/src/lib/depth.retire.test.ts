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
      // The drill-in's detail reads, when a test opens it: never answering, so
      // nothing they would carry lands over what the test put on screen.
      ...Object.fromEntries(
        [
          "getServer",
          "listBackups",
          "listSchedules",
          "getServerDns",
          "getServerSettings",
          "listFiles",
          "getServerSftp",
          "getInstallLog",
        ].map((k) => [k, () => new Promise(() => {})]),
      ),
    },
  };
});

/** The drill-in re-targets its console stream on open, and the stream reaches
 *  for the global WebSocket when it does; nothing here reads it. */
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

vi.mock("./fleet.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./fleet.svelte")>();
  return { ...actual, refreshFleet: () => refreshFleet() };
});

import { ApiError } from "@/api/client";
import { bootDeepLinks, depth, openDepth, powerControls, surface, syncDepthFromFleet } from "./depth.svelte";
import { fleet, fleetPollMs, gridServers } from "./fleet.svelte";
import {
  CD_SERVER_BODY,
  confirmGo,
  confirmWord,
  openConfirm,
  retireConfirmBody,
  retireNote,
  ui,
} from "./state.svelte";
import {
  chipKind,
  deadNote,
  pendingRemovalsNote,
  removalKind,
  retireAbandoned,
  retirePhaseWord,
  serverMeta,
} from "./views.svelte";
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
    // The mock's confirmation body: the note's sentence, plus where it goes.
    expect(CD_SERVER_BODY).toBe(
      "this stops the server, takes a final backup, then removes its world and config from the node. " +
        "its backups are kept, and it can be revived later from the retired list.",
    );
    expect(retireConfirmBody(true)).toBe(CD_SERVER_BODY);
    expect(retireNote(true)).toMatch(/^retiring stops this server, takes a final backup, then removes its world and config/);
    expect(retireNote(true)).toMatch(/its backups are kept, and it can be revived later from any of them\.$/);
    for (const s of [CD_SERVER_BODY, retireConfirmBody(false), retireNote(true), retireNote(false)]) {
      expect(s).not.toMatch(/cannot be undone/);
    }
  });

  it("never claims a final backup the toggle turned off", () => {
    expect(retireConfirmBody(false)).not.toMatch(/takes a final backup/);
    expect(retireConfirmBody(false)).toMatch(/no final backup is taken/);
    expect(retireNote(false)).not.toMatch(/takes a final backup/);
    expect(retireNote(false)).toMatch(/without a final backup/);
  });

  it("sends the toggle with the retire, and every open starts it back on", async () => {
    retireServer.mockResolvedValue(server("sv-live", "retiring"));
    depth.retireFinalBackup = false;
    openConfirm("valheim", null, { noun: "server", verb: "retire", body: retireConfirmBody(false) });
    await confirmGo();
    expect(retireServer).toHaveBeenCalledWith("sv-live", false);

    fleet.servers = [server("sv-live", "offline")];
    openDepth("sv-live", 0, 0, null);
    expect(depth.retireFinalBackup).toBe(true);
    surface();
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

  it("names its phase on the card", () => {
    const job = (phase: "stopping" | "backing_up" | "removing") =>
      server("a", "retiring", { retire: { phase, final_backup: "requested", started_at: "" } });
    expect(retirePhaseWord(job("backing_up"))).toBe("backing up");
    expect(serverMeta(job("backing_up"))).toMatch(/· retiring · backing up$/);
    expect(deadNote(job("backing_up"))).toBe("retiring — taking the final backup, then its world leaves the node");
    expect(deadNote(job("removing"))).toBe("retiring — its world is leaving the node");
    expect(serverMeta(job("removing"))).toMatch(/· retiring · removing$/);
    // no job reported yet: the note still says what is coming
    expect(deadNote(server("a", "retiring"))).toBe("retiring — final backup, then its world leaves the node");
  });

  it("says an abandoned retire until the next start clears it", () => {
    const why = "retire abandoned: the final backup failed: disk full; nothing was removed";
    expect(retireAbandoned(server("a", "offline", { last_error: why, retire_note: why }))).toBe(why);
    // the next start cleared last_error; retire_note still holds the sentence
    expect(retireAbandoned(server("a", "running", { retire_note: why }))).toBe("");
    expect(retireAbandoned(server("a", "offline", { last_error: "start after revive: node offline" }))).toBe("");
    expect(retireAbandoned(server("a", "retiring", { last_error: why }))).toBe("");
  });

  it("surfaces an open drill-in once the fleet reads the server retired", () => {
    fleet.servers = [server("sv-live", "retiring")];
    openDepth("sv-live", 0, 0, null);
    expect(depth.open).toBe(true);
    fleet.servers = [server("sv-live", "retired")];
    syncDepthFromFleet();
    expect(depth.open).toBe(false);
  });
});

describe("a stopped card's note", () => {
  it("says a refused start or a failed restore instead of the plain line", () => {
    expect(deadNote(server("a", "offline", { last_error: "start after revive: node titan is offline" }))).toBe(
      "stopped · start after revive: node titan is offline",
    );
    expect(
      deadNote(
        server("a", "offline", {
          restore_result: { backup_id: "b", ok: false, error: "gzip: invalid header", finished_at: "" },
        }),
      ),
    ).toBe("stopped · last restore failed — gzip: invalid header");
    expect(
      deadNote(server("a", "offline", { restore_result: { backup_id: "b", ok: true, finished_at: "" } })),
    ).toBe("stopped · world saved on shutdown");
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
