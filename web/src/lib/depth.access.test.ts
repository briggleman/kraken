// The drill-in's access block (#146): the detail refresh reads who the server
// is shared with, a grant answers with the view as it now stands, and a write
// that lands after the drill-in moved to another server does not overwrite
// that server's view. A refused access read stays out of the error line —
// a role that cannot read it still gets the rest of the drill-in.

import { beforeEach, describe, expect, it, vi } from "vitest";

const getServerAccess = vi.fn();
const grantServerAccess = vi.fn();

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  const ok = (v: unknown) => async () => v;
  return {
    ApiError: actual.ApiError,
    errMsg: actual.errMsg,
    api: {
      getServer: ok({ id: "srv-1", state: "offline" }),
      listFiles: ok({ path: ".", entries: [] }),
      listBackups: ok({ backups: [], mirror: "" }),
      listSchedules: ok({ schedules: [] }),
      getServerDns: ok(null),
      getServerSettings: ok({ groups: [], values: {} }),
      getServerAccess: (...a: unknown[]) => getServerAccess(...a),
      grantServerAccess: (...a: unknown[]) => grantServerAccess(...a),
      getServerSftp: ok(null),
      getInstallLog: ok(null),
    },
  };
});

vi.mock("./fleet.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./fleet.svelte")>();
  return { ...actual, refreshFleet: vi.fn(async () => {}) };
});

vi.mock("./stream.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./stream.svelte")>();
  class QuietStream {
    lines: unknown[] = [];
    set() {}
  }
  return { ...actual, ServerStream: QuietStream };
});

import { ApiError } from "@/api/client";
import type { ServerAccess } from "@/api/types";
import { accessGrant, depth, openDepth } from "./depth.svelte";

const flush = () => new Promise((r) => setTimeout(r, 0));

function view(grants: string[]): ServerAccess {
  return {
    owner: { id: "olga", username: "olga", role: "Operator" },
    access: "owner",
    can_manage: true,
    can_transfer: false,
    grants: grants.map((g) => ({ id: g, username: g, role: "Read-only", created_at: "2026-10-21T00:00:00Z" })),
    candidates: [],
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  depth.error = null;
  depth.accessBusy = false;
});

describe("the drill-in's access block", () => {
  it("reads the access view on open", async () => {
    getServerAccess.mockResolvedValue(view([]));
    openDepth("srv-1", 0, 0);
    await flush();
    expect(depth.access?.owner?.username).toBe("olga");
  });

  it("takes the view a grant answers with", async () => {
    getServerAccess.mockResolvedValue(view([]));
    openDepth("srv-1", 0, 0);
    await flush();
    grantServerAccess.mockResolvedValue(view(["ron"]));
    expect(await accessGrant("ron")).toBe(true);
    expect(grantServerAccess).toHaveBeenCalledWith("srv-1", "ron");
    expect(depth.access?.grants?.map((g) => g.id)).toEqual(["ron"]);
    expect(depth.accessBusy).toBe(false);
  });

  it("does not let a grant for the last server land on the next one", async () => {
    getServerAccess.mockResolvedValue(view([]));
    openDepth("srv-1", 0, 0);
    await flush();
    let answer!: (v: ServerAccess) => void;
    grantServerAccess.mockReturnValue(new Promise((r) => (answer = r)));
    const pending = accessGrant("ron");
    getServerAccess.mockResolvedValue(view(["wren"]));
    openDepth("srv-2", 0, 0);
    await flush();
    answer(view(["ron"]));
    await pending;
    expect(depth.access?.grants?.map((g) => g.id)).toEqual(["wren"]);
  });

  it("keeps a refused access read out of the error line", async () => {
    getServerAccess.mockRejectedValue(new ApiError(404, "server not found"));
    openDepth("srv-1", 0, 0);
    await flush();
    expect(depth.access).toBeNull();
    expect(depth.error).toBeNull();
  });
});
