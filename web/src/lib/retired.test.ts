// The retired group at the foot of the fleet (#360). These pin what a row
// says — where it was, when it went, what it kept, what the retire could not
// do — how its archives are read (from the node it left, once, and only when
// that can work), and what delete for good asks and answers.

import { beforeEach, describe, expect, it, vi } from "vitest";

const listBackups = vi.fn();
const deleteServer = vi.fn();
const refreshFleet = vi.fn(async () => {});

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ...actual,
    api: {
      listBackups: (...a: unknown[]) => listBackups(...a),
      deleteServer: (...a: unknown[]) => deleteServer(...a),
    },
  };
});

vi.mock("./fleet.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./fleet.svelte")>();
  return { ...actual, refreshFleet: () => refreshFleet() };
});

import { ApiError } from "@/api/client";
import { auth } from "./auth.svelte";
import { fleet } from "./fleet.svelte";
import {
  keepLabel,
  keepOf,
  keepStale,
  loadKeep,
  openPurge,
  purge,
  retired,
  retiredNote,
  retiredServers,
  retiredSlug,
  retiredWhen,
  rosterEmptyNote,
} from "./retired.svelte";
import { CD_PURGE_BODY, confirmGo, confirmWord, ui } from "./state.svelte";
import type { Backup, Node, Role, Server } from "@/api/types";

function server(id: string, extra: Partial<Server> = {}): Server {
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
    ...extra,
  };
}

const NODE = { id: "node-1", name: "behemoth", status: "online" } as Node;

function backup(id: string, state: Backup["state"], size: number): Backup {
  return { id, name: id, size, created_ms: 1, state, replication: "" };
}

beforeEach(() => {
  vi.clearAllMocks();
  fleet.servers = [];
  fleet.nodes = [NODE];
  retired.keep = {};
  retired.busy = {};
  retired.note = "";
  retired.noteFailed = false;
  ui.confirm = null;
  auth.role = { permissions: ["*"] } as Role;
});

describe("retiredServers", () => {
  it("is the retired rows only, the most recently retired first", () => {
    const rows = retiredServers([
      server("old", { retired_at: "2026-08-30T00:00:00Z" }),
      server("live", { state: "running", retired_at: undefined }),
      server("new", { retired_at: "2026-09-21T00:00:00Z" }),
    ]);
    expect(rows.map((s) => s.id)).toEqual(["new", "old"]);
  });
});

describe("a retired row's readings", () => {
  it("says the game and where it was, or that its node is gone", () => {
    expect(retiredSlug(server("a"), "RuneScape Dragonwilds", NODE)).toBe("runescape dragonwilds · was on behemoth");
    expect(retiredSlug(server("a"), "V Rising", undefined)).toBe("v rising · its node is gone");
  });

  it("says when it went, as a day and how long ago", () => {
    const at = Date.parse("2026-09-21T03:00:00Z");
    const when = retiredWhen(server("a"), at + 3 * 86_400_000 + 3_600_000);
    expect(when).toMatch(/^retired sep 2[01] · 3d ago$/);
    expect(retiredWhen(server("a", { retired_at: undefined }))).toBe("retired");
  });

  it("says only a final backup the retire could not take, the whole note in its title", () => {
    const skipped = "final backup skipped: node titan did not answer; removing its containers and world is queued for node titan";
    expect(retiredNote(server("a", { retire_note: skipped }))).toEqual({ word: "final backup skipped", title: skipped });
    // a queued removal alone is the node band's to say
    expect(retiredNote(server("a", { retire_note: "removing its containers and world is queued for node titan" }))).toBeNull();
    expect(retiredNote(server("a"))).toBeNull();
  });

  it("counts only ready archives toward what it kept", () => {
    expect(keepOf([backup("a", "ready", 2048), backup("b", "failed", 0), backup("c", "pending", 0), backup("d", "ready", 1024)])).toEqual({
      count: 2,
      bytes: 3072,
    });
    expect(keepLabel({ kind: "ok", count: 4, bytes: 9.6 * 1024 ** 3 })).toBe("4 backups · 9.6G kept");
    expect(keepLabel({ kind: "ok", count: 1, bytes: 1024 })).toBe("1 backup · 1.0K kept");
    expect(keepLabel({ kind: "ok", count: 0, bytes: 0 })).toBe("no backups kept");
  });

  it("does not claim an empty fleet when every server is retired", () => {
    expect(rosterEmptyNote([])).toMatch(/^no servers yet/);
    expect(rosterEmptyNote([server("a")])).toMatch(/^every server is retired — revive one below/);
  });
});

describe("reading what a row kept", () => {
  it("reads the archives from the node it left", async () => {
    listBackups.mockResolvedValueOnce({ backups: [backup("a", "ready", 2048)], mirror: "" });
    await loadKeep(server("a"));
    expect(listBackups).toHaveBeenCalledWith("a");
    expect(retired.keep.a.reading).toEqual({ kind: "ok", count: 1, bytes: 2048 });
    // read once: the next poll does not read again
    expect(keepStale(server("a"))).toBe(false);
  });

  it("does not ask without backup.manage, and names where they are", async () => {
    auth.role = { permissions: ["server.view"] } as Role;
    await loadKeep(server("a"));
    expect(listBackups).not.toHaveBeenCalled();
    expect(keepLabel(retired.keep.a.reading)).toBe("backups on behemoth");
  });

  it("does not ask an offline node, and reads again once it is back", async () => {
    fleet.nodes = [{ ...NODE, status: "offline" }];
    await loadKeep(server("a"));
    expect(listBackups).not.toHaveBeenCalled();
    expect(keepLabel(retired.keep.a.reading)).toBe("backups unreadable while behemoth is offline");
    expect(keepStale(server("a"))).toBe(false);
    fleet.nodes = [NODE];
    expect(keepStale(server("a"))).toBe(true);
  });

  it("says the archives went with a node that is gone", async () => {
    fleet.nodes = [];
    await loadKeep(server("a"));
    expect(keepLabel(retired.keep.a.reading)).toBe("its archives went with its node");
    listBackups.mockRejectedValueOnce(
      new ApiError(404, "the node this server was retired from no longer exists, and its archives went with it", "not_found"),
    );
    fleet.nodes = [NODE];
    await loadKeep(server("b"));
    expect(keepLabel(retired.keep.b.reading)).toBe("its archives went with its node");
  });
});

describe("delete for good", () => {
  it("is the typed gate on the word delete, with the one warning that says it cannot be undone", () => {
    openPurge(server("dragonwilds-02"), null);
    expect(ui.confirm).toMatchObject({ name: "dragonwilds-02", noun: "server", verb: "delete", typed: true, body: CD_PURGE_BODY });
    expect(confirmWord(ui.confirm)).toBe("delete");
    expect(CD_PURGE_BODY).toMatch(/cannot be undone/);
    expect(CD_PURGE_BODY).toMatch(/shared backup target are kept/);
  });

  it("deletes through the confirmation and speaks the Panel's note", async () => {
    deleteServer.mockResolvedValueOnce({ note: "2 archives on the shared target were kept", removal_pending: false });
    openPurge(server("a"), null);
    await confirmGo();
    expect(deleteServer).toHaveBeenCalledWith("a");
    expect(retired.note).toBe("2 archives on the shared target were kept");
    expect(retired.noteFailed).toBe(false);
    expect(refreshFleet).toHaveBeenCalled();
  });

  it("speaks a refusal and holds nothing busy", async () => {
    deleteServer.mockRejectedValueOnce(new ApiError(409, "a removal is still owed for this server on node titan", "removal_pending"));
    await purge("a");
    expect(retired.note).toMatch(/removal is still owed/);
    expect(retired.noteFailed).toBe(true);
    expect(retired.busy.a).toBeUndefined();
  });
});
