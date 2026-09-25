// The retired group at the foot of the fleet (#360). These pin what a row
// says — where it was, when it went, what it kept, what the retire could not
// do — how its archives are read (from the node it left, once, and only when
// that can work), and what delete for good asks and answers.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const listBackups = vi.fn();
const deleteServer = vi.fn();
const reviveServer = vi.fn();
const logoutApi = vi.fn(async () => {});
const refreshFleet = vi.fn(async () => {});

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ...actual,
    api: {
      listBackups: (...a: unknown[]) => listBackups(...a),
      deleteServer: (...a: unknown[]) => deleteServer(...a),
      reviveServer: (...a: unknown[]) => reviveServer(...a),
      logout: () => logoutApi(),
    },
  };
});

vi.mock("./fleet.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./fleet.svelte")>();
  return { ...actual, refreshFleet: () => refreshFleet() };
});

import { ApiError } from "@/api/client";
import { auth, logout } from "./auth.svelte";
import { fleet, fleetIssued } from "./fleet.svelte";
import {
  KEEP_READ_TIMEOUT_MS,
  KEEP_RETRY_MS,
  keepLabel,
  keepOf,
  keepStale,
  loadKeep,
  openPurge,
  purge,
  resetRetired,
  retired,
  retiredNote,
  retiredServers,
  retiredSlug,
  retiredWhen,
  reviveRetired,
  rosterEmptyNote,
  syncRetired,
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
  // Reset, not clear: an implementation one case sets (a read that never
  // settles, say) must not carry into the next and start reads there that
  // hold the module's slots — the cases would then pass or fail by order.
  vi.resetAllMocks();
  resetRetired();
  fleet.servers = [];
  fleet.nodes = [NODE];
  ui.confirm = null;
  auth.role = { permissions: ["*"] } as Role;
});

afterEach(() => {
  vi.useRealTimers();
});

/** Settle the microtasks a queued read hangs off. */
const flush = () => new Promise((r) => setTimeout(r, 0));

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

  it("says only a skipped final backup, the whole note in its title", () => {
    const skipped = "final backup skipped: node titan did not answer; removing its containers and world is queued for node titan";
    expect(retiredNote(server("a", { retire_note: skipped }))).toEqual({ word: "final backup skipped", title: skipped });
    // a queued removal alone is the node band's to say
    expect(retiredNote(server("a", { retire_note: "removing its containers and world is queued for node titan" }))).toBeNull();
    // a failed final backup abandons the retire, so it never reaches a row
    expect(retiredNote(server("a", { retire_note: "final backup failed: disk full" }))).toBeNull();
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
    fleet.servers = [server("a")];
    listBackups.mockResolvedValueOnce({ backups: [backup("a", "ready", 2048)], mirror: "" });
    await loadKeep(server("a"));
    expect(listBackups).toHaveBeenCalledWith("a", expect.any(AbortSignal));
    expect(retired.keep.a.reading).toEqual({ kind: "ok", count: 1, bytes: 2048 });
    // read once: the next poll does not read again
    expect(keepStale(server("a"))).toBe(false);
  });

  it("says why it did not ask, in the text and the title", async () => {
    fleet.servers = [server("a"), server("b"), server("c")];
    auth.role = { permissions: ["server.view"] } as Role;
    await loadKeep(server("a"));
    expect(listBackups).not.toHaveBeenCalled();
    expect(retired.keep.a.reading).toMatchObject({
      label: "backups on behemoth · no permission to read them",
      title: "backups on behemoth · no permission to read them",
    });
    auth.role = { permissions: ["*"] } as Role;
    fleet.nodes = [{ ...NODE, status: "offline" }];
    await loadKeep(server("b"));
    expect(keepLabel(retired.keep.b.reading)).toBe("backups on behemoth · unreadable while behemoth is offline");
    fleet.nodes = [];
    await loadKeep(server("c"));
    expect(keepLabel(retired.keep.c.reading)).toBe("its node is gone, and its archives went with it");
  });

  it("does not ask an offline node, and reads again once it is back", async () => {
    fleet.servers = [server("a")];
    fleet.nodes = [{ ...NODE, status: "offline" }];
    await loadKeep(server("a"));
    expect(listBackups).not.toHaveBeenCalled();
    expect(keepStale(server("a"))).toBe(false);
    fleet.nodes = [NODE];
    expect(keepStale(server("a"))).toBe(true);
  });

  it("names a node the Panel says is gone, and does not ask again", async () => {
    fleet.servers = [server("b")];
    listBackups.mockRejectedValueOnce(
      new ApiError(404, "the node this server was retired from no longer exists, and its archives went with it", "not_found"),
    );
    await loadKeep(server("b"));
    expect(keepLabel(retired.keep.b.reading)).toBe("backups on behemoth · its node is gone, and its archives went with it");
    expect(keepStale(server("b"), Date.now() + KEEP_RETRY_MS * 10)).toBe(false);
  });

  it("reads a failed row again once the wait is over, while its node stays online", async () => {
    fleet.servers = [server("a")];
    listBackups.mockRejectedValueOnce(new ApiError(504, "node behemoth did not answer within the time allowed", "node_timeout"));
    const t0 = 1_000_000;
    await loadKeep(server("a"), () => t0);
    expect(retired.keep.a.reading).toMatchObject({
      label: "backups on behemoth · could not be read",
      title: expect.stringMatching(/did not answer/),
    });
    expect(keepStale(server("a"), t0 + KEEP_RETRY_MS - 1)).toBe(false);
    expect(keepStale(server("a"), t0 + KEEP_RETRY_MS)).toBe(true);
  });

  it("never lands a reading on a row that was revived while it was out", async () => {
    fleet.servers = [server("a")];
    let answer: (v: unknown) => void = () => {};
    listBackups.mockReturnValueOnce(new Promise((r) => (answer = r)));
    const pending = loadKeep(server("a"));
    fleet.servers = [server("a", { state: "installing", node_id: "node-1" })];
    syncRetired(fleet.servers, 0);
    answer({ backups: [backup("x", "ready", 2048)] });
    await pending;
    expect(retired.keep.a).toBeUndefined();
  });

  it("drops the reading of a row another operator revived", async () => {
    fleet.servers = [server("a")];
    listBackups.mockResolvedValueOnce({ backups: [backup("x", "ready", 2048)] });
    await loadKeep(server("a"));
    expect(retired.keep.a).toBeDefined();
    syncRetired([server("a", { state: "installing" })], 0);
    expect(retired.keep.a).toBeUndefined();
  });

  it("reads two rows at a time, not every row at once", async () => {
    const rows = ["a", "b", "c", "d", "e"].map((id) => server(id));
    fleet.servers = rows;
    const answers: ((v: unknown) => void)[] = [];
    listBackups.mockImplementation(() => new Promise((r) => answers.push(r)));
    syncRetired(rows, 0);
    expect(listBackups).toHaveBeenCalledTimes(2);
    // a second poll while they are out queues nothing twice
    syncRetired(rows, 0);
    expect(listBackups).toHaveBeenCalledTimes(2);
    answers[0]({ backups: [] });
    await flush();
    expect(listBackups).toHaveBeenCalledTimes(3);
    for (let i = 1; i < 5; i++) {
      answers[i]?.({ backups: [] });
      await flush();
    }
    expect(listBackups).toHaveBeenCalledTimes(5);
    expect(Object.values(retired.keep).every((k) => k.reading.kind === "ok")).toBe(true);
  });

  it("gives every slot back with the session, and a read from the last one takes none of the next one's", async () => {
    fleet.servers = ["a", "b", "c"].map((id) => server(id));
    const old: ((v: unknown) => void)[] = [];
    listBackups.mockImplementation(() => new Promise((r) => old.push(r)));
    syncRetired(fleet.servers, 0);
    expect(listBackups).toHaveBeenCalledTimes(2);
    resetRetired();
    // the next session reads two at once again, although the last one's two are still out
    const next: ((v: unknown) => void)[] = [];
    listBackups.mockReset();
    listBackups.mockImplementation(() => new Promise((r) => next.push(r)));
    fleet.servers = ["d", "e", "f"].map((id) => server(id));
    syncRetired(fleet.servers, 0);
    expect(listBackups.mock.calls.map((c) => c[0])).toEqual(["d", "e"]);
    // the old reads settling do not free a slot of this session's
    old.forEach((answer) => answer({ backups: [] }));
    await flush();
    expect(listBackups).toHaveBeenCalledTimes(2);
    expect(retired.keep.a).toBeUndefined();
    // one of this session's settling does
    next[0]({ backups: [] });
    await flush();
    expect(listBackups.mock.calls.map((c) => c[0])).toEqual(["d", "e", "f"]);
  });

  it("gives up on a read that never answers, cancels it, frees its slot, and reads the row again later", async () => {
    vi.useFakeTimers();
    fleet.servers = ["a", "b", "c"].map((id) => server(id));
    listBackups.mockImplementation(() => new Promise(() => {}));
    syncRetired(fleet.servers, 0);
    expect(listBackups).toHaveBeenCalledTimes(2);
    const signal = listBackups.mock.calls[0][1] as AbortSignal;
    // past the Panel's own 20s Agent limit: its answer, slow or not, still lands
    expect(KEEP_READ_TIMEOUT_MS).toBeGreaterThan(20_000);
    await vi.advanceTimersByTimeAsync(KEEP_READ_TIMEOUT_MS - 1);
    expect(listBackups).toHaveBeenCalledTimes(2);
    expect(retired.keep.a.reading.kind).toBe("loading");
    expect(signal.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    // the request is cancelled, not only abandoned: it holds no connection
    expect(signal.aborted).toBe(true);
    // failed like any other passing failure: said, and read again after the wait
    expect(retired.keep.a.reading).toMatchObject({
      kind: "unread",
      label: "backups on behemoth · could not be read",
      title: `the read of its backups timed out after ${KEEP_READ_TIMEOUT_MS / 1000}s`,
    });
    const r = retired.keep.a.reading;
    expect(r.kind === "unread" && r.retryAt !== undefined).toBe(true);
    // and the slots went back: the third row is being read
    expect(listBackups.mock.calls.map((c) => c[0])).toEqual(["a", "b", "c"]);
    expect(keepStale(server("a"), Date.now() + KEEP_RETRY_MS)).toBe(true);
  });

  it("forgets every reading when the session changes", async () => {
    fleet.servers = [server("a")];
    listBackups.mockResolvedValueOnce({ backups: [] });
    await loadKeep(server("a"));
    retired.note = "2 archives on the shared target were kept";
    await logout();
    expect(retired.keep).toEqual({});
    expect(retired.note).toBe("");
  });
});

describe("the group's head note", () => {
  // refreshFleet is mocked in this file, so no poll is ever issued and
  // fleetIssued() stays put: "after" is the number the next poll would carry.
  // The real poll numbering is covered end to end in retired.follow.svelte.test.
  it("outlives the delete that spoke it, and goes when the retired set changes under it", async () => {
    fleet.servers = [server("a"), server("b")];
    deleteServer.mockResolvedValueOnce({ note: "2 archives on the shared target were kept", removal_pending: false });
    await purge("a");
    const after = fleetIssued() + 1; // the first poll started after the answer
    // the fleet read that takes the row away keeps the answer
    syncRetired([server("b")], after);
    expect(retired.note).toBe("2 archives on the shared target were kept");
    // the same picture again keeps it too
    syncRetired([server("b")], after + 1);
    expect(retired.note).toBe("2 archives on the shared target were kept");
    // another operator retires a server: the answer is about a picture no longer on screen
    syncRetired([server("b"), server("c")], after + 2);
    expect(retired.note).toBe("");
  });

  it("stands for the set the Panel reports after the delete, not the one on screen before it", async () => {
    // Another operator retired c since the last poll: the fleet on screen does
    // not have it, and the delete's own follow-up read does.
    fleet.servers = [server("a"), server("b")];
    deleteServer.mockResolvedValueOnce({ note: "2 archives on the shared target were kept", removal_pending: false });
    await purge("a");
    const after = fleetIssued() + 1;
    syncRetired([server("b"), server("c")], after);
    expect(retired.note).toBe("2 archives on the shared target were kept");
    // and when c is revived or deleted by them later, the note goes
    syncRetired([server("b")], after + 1);
    expect(retired.note).toBe("");
  });

  it("survives two deletes close together", async () => {
    // The second delete is sent before the first one's fleet read lands, so the
    // fleet on screen still holds both rows when its answer is spoken.
    fleet.servers = [server("a"), server("b"), server("c")];
    deleteServer.mockResolvedValueOnce({ note: "", removal_pending: false });
    await purge("a");
    deleteServer.mockResolvedValueOnce({ note: "an archive on the shared target was kept", removal_pending: false });
    await purge("b");
    syncRetired([server("c")], fleetIssued() + 1);
    expect(retired.note).toBe("an archive on the shared target was kept");
  });

  it("does not speak an answer that lands after the session changed", async () => {
    fleet.servers = [server("a")];
    let answer: (v: unknown) => void = () => {};
    deleteServer.mockReturnValueOnce(new Promise((r) => (answer = r)));
    const going = purge("a");
    resetRetired(); // a logout, and the next operator's login
    answer({ note: "2 archives on the shared target were kept", removal_pending: false });
    await going;
    expect(retired.note).toBe("");
    // a refusal likewise
    let refuse: (e: unknown) => void = () => {};
    deleteServer.mockReturnValueOnce(new Promise((_, r) => (refuse = r)));
    const refused = purge("a");
    resetRetired();
    refuse(new ApiError(409, "a removal is still owed for this server on node titan", "removal_pending"));
    await refused;
    expect(retired.note).toBe("");
    expect(retired.noteFailed).toBe(false);
  });

  it("is not cleared by a poll that was already out when the delete committed", async () => {
    fleet.servers = [server("a"), server("b")];
    const before = fleetIssued(); // a poll started before the delete answered
    deleteServer.mockResolvedValueOnce({ note: "2 archives on the shared target were kept", removal_pending: false });
    await purge("a");
    // it lands after the note and still carries the deleted row: not the set changing under it
    syncRetired([server("a"), server("b")], before);
    expect(retired.note).toBe("2 archives on the shared target were kept");
    // the poll purge itself started brings the set without the row, and the note stands
    syncRetired([server("b")], fleetIssued() + 1);
    expect(retired.note).toBe("2 archives on the shared target were kept");
  });

  it("does not keep a refusal once someone else changes the set", async () => {
    fleet.servers = [server("a")];
    deleteServer.mockRejectedValueOnce(new ApiError(409, "a removal is still owed for this server on node titan", "removal_pending"));
    await purge("a");
    const after = fleetIssued() + 1;
    syncRetired(fleet.servers, after);
    syncRetired(fleet.servers, after + 1);
    expect(retired.noteFailed).toBe(true);
    syncRetired([], after + 2);
    expect(retired.note).toBe("");
    expect(retired.noteFailed).toBe(false);
  });
});

describe("revive from the group", () => {
  it("holds the row from the click until the fleet read after it has landed", async () => {
    fleet.servers = [server("a")];
    let answer: (v: unknown) => void = () => {};
    reviveServer.mockReturnValueOnce(new Promise((r) => (answer = r)));
    let refreshed: () => void = () => {};
    refreshFleet.mockReturnValueOnce(new Promise<void>((r) => (refreshed = r)));
    const going = reviveRetired("a", {});
    expect(retired.reviving.a).toBe(true);
    // a second click is refused before it reaches the Panel
    expect(await reviveRetired("a", {})).toBe("");
    expect(reviveServer).toHaveBeenCalledTimes(1);
    answer(server("a", { state: "installing" }));
    await flush();
    expect(retired.reviving.a).toBe(true); // the fleet read is still out
    refreshed();
    expect(await going).toBe("");
    expect(retired.reviving.a).toBeUndefined();
  });

  it("does not read the archives of a row with a revive in flight", async () => {
    fleet.servers = [server("a")];
    listBackups.mockResolvedValue({ backups: [backup("x", "ready", 2048)] });
    await loadKeep(server("a"));
    listBackups.mockClear();
    reviveServer.mockResolvedValueOnce(server("a", { state: "installing" }));
    let refreshed: () => void = () => {};
    refreshFleet.mockReturnValueOnce(new Promise<void>((r) => (refreshed = r)));
    const going = reviveRetired("a", {});
    await flush();
    // the 202 is in and its reading dropped; a poll lands before the fleet read
    // after the revive, still showing the row retired
    expect(retired.keep.a).toBeUndefined();
    syncRetired([server("a")], 0);
    expect(listBackups).not.toHaveBeenCalled();
    refreshed();
    expect(await going).toBe("");
    expect(retired.reviving.a).toBeUndefined();
    // once the revive has let go, a row that still reads retired is read again
    syncRetired([server("a")], 0);
    expect(listBackups).toHaveBeenCalledWith("a", expect.any(AbortSignal));
  });

  it("does not read a row that was already waiting in the queue when its revive was sent", async () => {
    fleet.servers = ["a", "b", "c"].map((id) => server(id));
    const answers: ((v: unknown) => void)[] = [];
    listBackups.mockImplementation(() => new Promise((r) => answers.push(r)));
    syncRetired(fleet.servers, 0);
    expect(listBackups.mock.calls.map((c) => c[0])).toEqual(["a", "b"]); // c waits in the queue
    let refuse: (e: unknown) => void = () => {};
    reviveServer.mockReturnValueOnce(new Promise((_, r) => (refuse = r)));
    const going = reviveRetired("c", {});
    // a slot frees while the revive is out: c is not read
    answers[0]({ backups: [] });
    await flush();
    expect(listBackups.mock.calls.map((c) => c[0])).toEqual(["a", "b"]);
    // the revive is refused: c is still retired, and the next sync reads it
    refuse(new ApiError(409, "only a retired server can be revived", "server_not_retired"));
    expect(await going).toMatch(/only a retired server/);
    syncRetired(fleet.servers, 0);
    expect(listBackups.mock.calls.map((c) => c[0])).toEqual(["a", "b", "c"]);
  });

  it("returns the Panel's refusal and lets go of the row", async () => {
    reviveServer.mockRejectedValueOnce(new ApiError(409, "only a retired server can be revived", "server_not_retired"));
    expect(await reviveRetired("a", {})).toMatch(/only a retired server/);
    expect(retired.reviving.a).toBeUndefined();
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

