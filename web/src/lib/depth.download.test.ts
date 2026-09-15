// How the Files tab saves a file (#304). A download is a mint plus a
// navigation: the pill asks the Panel for a one-time token and points an anchor
// at the URL it returns, and the browser does the transfer. These tests pin
// what the store is responsible for — minting the right shape for a file and
// for a folder, putting a refusal on screen rather than retrying it, and not
// minting twice for one pill.

import { beforeEach, describe, expect, it, vi } from "vitest";

const mintDownloadToken = vi.fn();

// ApiError and errMsg are the real ones: what the pane's notice says here is
// what it says in the app.
vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ApiError: actual.ApiError,
    errMsg: actual.errMsg,
    api: { mintDownloadToken: (...a: unknown[]) => mintDownloadToken(...a) },
  };
});

import { ApiError } from "@/api/client";
import { depth, filesDownload } from "./depth.svelte";
import type { FileEntry } from "@/api/types";

function entry(over: Partial<FileEntry> = {}): FileEntry {
  return {
    name: "server.cfg",
    path: "/data/server.cfg",
    is_dir: false,
    size: 42,
    modified_ms: 0,
    ...over,
  };
}

function minted(over: Record<string, unknown> = {}) {
  return {
    url: "/api/v1/servers/srv-1/files/raw?path=%2Fdata%2Fserver.cfg&token=abc",
    token: "abc",
    kind: "raw",
    expires_at: "2026-09-15T10:00:00Z",
    expires_in_seconds: 60,
    ...over,
  };
}

/** The anchor the save goes through, captured instead of clicked — jsdom has no
 *  download manager, and what it was pointed at is the whole assertion. */
let clicked: { href: string; download: string }[] = [];

// Bound once, before anything spies on it: re-binding a live spy every
// beforeEach would have the spy call itself.
const nativeCreateElement = document.createElement.bind(document);

beforeEach(() => {
  vi.restoreAllMocks();
  vi.clearAllMocks();
  clicked = [];
  depth.serverId = "srv-1";
  depth.downloading = null;
  depth.error = null;
  vi.spyOn(document, "createElement").mockImplementation(((tag: string) => {
    const el = nativeCreateElement(tag);
    if (tag === "a") {
      const a = el as HTMLAnchorElement;
      a.click = () => clicked.push({ href: a.getAttribute("href") ?? "", download: a.download });
    }
    return el;
  }) as typeof document.createElement);
});

describe("filesDownload", () => {
  it("mints a file as a single path and navigates to the tokenised URL", async () => {
    mintDownloadToken.mockResolvedValue(minted());

    await filesDownload(entry());

    expect(mintDownloadToken).toHaveBeenCalledWith("srv-1", { path: "/data/server.cfg" });
    expect(clicked).toHaveLength(1);
    expect(clicked[0].href).toContain("token=abc");
    expect(clicked[0].download).toBe("server.cfg");
    expect(depth.error).toBeNull();
    expect(depth.downloading).toBeNull();
  });

  it("mints a folder as a path set, so the Panel streams it as a zip", async () => {
    mintDownloadToken.mockResolvedValue(
      minted({ url: "/api/v1/servers/srv-1/files/download?token=zip1", token: "zip1", kind: "zip" }),
    );

    await filesDownload(entry({ name: "saves", path: "/data/saves", is_dir: true }));

    expect(mintDownloadToken).toHaveBeenCalledWith("srv-1", { paths: ["/data/saves"] });
    expect(clicked[0].href).toContain("/files/download?token=zip1");
    expect(clicked[0].download).toBe("saves.zip");
  });

  it("puts a refusal in the notice and saves nothing — there is no second attempt", async () => {
    mintDownloadToken.mockRejectedValue(new ApiError(403, "missing permission: server.files.read"));

    await filesDownload(entry());

    expect(mintDownloadToken).toHaveBeenCalledTimes(1);
    expect(clicked).toHaveLength(0);
    expect(depth.error).toBe("missing permission: server.files.read");
    expect(depth.downloading).toBeNull();
  });

  it("refuses a second pill while the first mint is still in flight", async () => {
    // A mint that has not settled: the guard is what stops the second click
    // from asking for a token of its own.
    let release: (v: unknown) => void = () => {};
    mintDownloadToken.mockReturnValueOnce(new Promise((r) => (release = r)));

    const first = filesDownload(entry());
    expect(depth.downloading).toBe("/data/server.cfg");

    await filesDownload(entry({ name: "saves", path: "/data/saves", is_dir: true }));
    expect(mintDownloadToken).toHaveBeenCalledTimes(1);
    expect(clicked).toHaveLength(0);

    release(minted());
    await first;
    expect(clicked).toHaveLength(1);
    expect(depth.downloading).toBeNull();
  });
});
