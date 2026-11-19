// The drill-in's share-link block (#475): the detail refresh reads the
// server's links, a create holds the URL for the one time it is shown and
// lists the link without it, a revoke drops the row, and a refused read or a
// failed write stays on the block's own line, never the drill-in's.

import { beforeEach, describe, expect, it, vi } from "vitest";

const listShareLinks = vi.fn();
const createShareLink = vi.fn();
const revokeShareLink = vi.fn();

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
      getServerAccess: ok(null),
      listShareLinks: (...a: unknown[]) => listShareLinks(...a),
      createShareLink: (...a: unknown[]) => createShareLink(...a),
      revokeShareLink: (...a: unknown[]) => revokeShareLink(...a),
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
import type { ShareLink } from "@/api/types";
import { depth, openDepth, shareCreate, shareDismissMinted, shareRevoke } from "./depth.svelte";

const flush = () => new Promise((r) => setTimeout(r, 0));

function link(id: string, extra: Partial<ShareLink> = {}): ShareLink {
  return {
    id,
    label: "",
    show_address: false,
    show_password: false,
    created_at: "2026-11-18T21:00:00Z",
    state: "live",
    ...extra,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  depth.error = null;
  depth.shareBusy = false;
});

describe("the drill-in's share-link block", () => {
  it("reads the links and whether a public address is set", async () => {
    listShareLinks.mockResolvedValue({ links: [link("a")], public_url_set: true });
    openDepth("srv-1", 0, 0);
    await flush();
    expect(listShareLinks).toHaveBeenCalledWith("srv-1");
    expect(depth.shareLinks?.map((l) => l.id)).toEqual(["a"]);
    expect(depth.sharePublicSet).toBe(true);
    expect(depth.shareMinted).toBeNull();
  });

  it("holds the new URL once and lists the link without it", async () => {
    listShareLinks.mockResolvedValue({ links: [link("a")], public_url_set: true });
    openDepth("srv-1", 0, 0);
    await flush();
    createShareLink.mockResolvedValue(link("b", { url: "https://kraken.example.com/s/tok", show_address: true }));

    const ok = await shareCreate({ label: "general", show_address: true, show_password: false, expires_in_days: 7 });
    expect(ok).toBe(true);
    expect(createShareLink).toHaveBeenCalledWith("srv-1", {
      label: "general",
      show_address: true,
      show_password: false,
      expires_in_days: 7,
    });
    expect(depth.shareMinted).toBe("https://kraken.example.com/s/tok");
    expect(depth.shareLinks?.map((l) => l.id)).toEqual(["b", "a"]);
    expect(depth.shareLinks?.[0].url).toBeUndefined();

    shareDismissMinted();
    expect(depth.shareMinted).toBeNull();
  });

  it("forgets the URL when the drill-in opens another server", async () => {
    listShareLinks.mockResolvedValue({ links: [], public_url_set: true });
    openDepth("srv-1", 0, 0);
    await flush();
    createShareLink.mockResolvedValue(link("b", { url: "https://kraken.example.com/s/tok" }));
    await shareCreate({ label: "", show_address: false, show_password: false, expires_in_days: 0 });
    expect(depth.shareMinted).not.toBeNull();
    openDepth("srv-2", 0, 0);
    expect(depth.shareMinted).toBeNull();
  });

  it("drops a revoked link", async () => {
    listShareLinks.mockResolvedValue({ links: [link("a"), link("b")], public_url_set: true });
    openDepth("srv-1", 0, 0);
    await flush();
    revokeShareLink.mockResolvedValue(undefined);
    expect(await shareRevoke("a")).toBe(true);
    expect(revokeShareLink).toHaveBeenCalledWith("srv-1", "a");
    expect(depth.shareLinks?.map((l) => l.id)).toEqual(["b"]);
  });

  it("keeps a failed create on its own line", async () => {
    listShareLinks.mockResolvedValue({ links: [], public_url_set: true });
    openDepth("srv-1", 0, 0);
    await flush();
    createShareLink.mockRejectedValue(new ApiError(409, "this server already has 25 share links — revoke one first"));
    expect(await shareCreate({ label: "", show_address: false, show_password: false, expires_in_days: 0 })).toBe(false);
    expect(depth.shareError).toContain("25 share links");
    expect(depth.error).toBeNull();
    expect(depth.shareMinted).toBeNull();
  });

  it("leaves the block out when the read is refused", async () => {
    listShareLinks.mockRejectedValue(new ApiError(403, "forbidden"));
    openDepth("srv-1", 0, 0);
    await flush();
    expect(depth.shareLinks).toBeNull();
    expect(depth.error).toBeNull();
  });
});
