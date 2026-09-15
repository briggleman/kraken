// How the Files tab decides to save a file (#304). The download pill has two
// routes to disk: a one-time tokenised URL the browser streams by itself, and
// the old Blob it buffers in tab memory. These tests pin which one is taken and
// when — the token route by default, the Blob only when the Panel is too old to
// have the mint route at all, and neither when the Panel refuses for a real
// reason.

import { beforeEach, describe, expect, it, vi } from "vitest";

const mintDownloadToken = vi.fn();
const downloadFile = vi.fn();
const downloadZip = vi.fn();

// ApiError and errMsg are the real ones: the fallback decision is a check on
// the error's type and status, so a stand-in would test the stand-in.
vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ApiError: actual.ApiError,
    errMsg: actual.errMsg,
    api: {
      mintDownloadToken: (...a: unknown[]) => mintDownloadToken(...a),
      downloadFile: (...a: unknown[]) => downloadFile(...a),
      downloadZip: (...a: unknown[]) => downloadZip(...a),
    },
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
  // Blob URLs are not implemented in jsdom.
  globalThis.URL.createObjectURL = vi.fn(() => "blob:fake");
  globalThis.URL.revokeObjectURL = vi.fn();
});

describe("filesDownload", () => {
  it("navigates to the tokenised URL and never touches the Blob path", async () => {
    mintDownloadToken.mockResolvedValue({
      url: "/api/v1/servers/srv-1/files/raw?path=%2Fdata%2Fserver.cfg&token=abc",
      token: "abc",
      kind: "raw",
      expires_at: "2026-09-15T10:00:00Z",
      expires_in_seconds: 60,
    });

    await filesDownload(entry());

    expect(mintDownloadToken).toHaveBeenCalledWith("srv-1", { path: "/data/server.cfg" });
    expect(downloadFile).not.toHaveBeenCalled();
    expect(clicked).toHaveLength(1);
    expect(clicked[0].href).toContain("token=abc");
    expect(clicked[0].download).toBe("server.cfg");
    expect(depth.error).toBeNull();
    expect(depth.downloading).toBeNull();
  });

  it("mints a folder as a path set, so the Panel streams it as a zip", async () => {
    mintDownloadToken.mockResolvedValue({
      url: "/api/v1/servers/srv-1/files/download?token=zip1",
      token: "zip1",
      kind: "zip",
      expires_at: "2026-09-15T10:00:00Z",
      expires_in_seconds: 60,
    });

    await filesDownload(entry({ name: "saves", path: "/data/saves", is_dir: true }));

    expect(mintDownloadToken).toHaveBeenCalledWith("srv-1", { paths: ["/data/saves"] });
    expect(downloadZip).not.toHaveBeenCalled();
    expect(clicked[0].href).toContain("/files/download?token=zip1");
    expect(clicked[0].download).toBe("saves.zip");
  });

  it("falls back to the Blob when the Panel has no mint route (404)", async () => {
    mintDownloadToken.mockRejectedValue(new ApiError(404, "404 page not found"));
    downloadFile.mockResolvedValue({ blob: new Blob(["hello"]), filename: "server.cfg" });

    await filesDownload(entry());

    expect(downloadFile).toHaveBeenCalledWith("srv-1", "/data/server.cfg");
    expect(clicked[0].href).toBe("blob:fake");
    expect(clicked[0].download).toBe("server.cfg");
    expect(depth.error).toBeNull();
  });

  it("does not fall back on a real refusal — the notice says what happened", async () => {
    mintDownloadToken.mockRejectedValue(new ApiError(403, "missing permission: server.files.read"));

    await filesDownload(entry());

    expect(downloadFile).not.toHaveBeenCalled();
    expect(clicked).toHaveLength(0);
    expect(depth.error).toBe("missing permission: server.files.read");
    expect(depth.downloading).toBeNull();
  });

  it("still refuses a second download while one is in flight", async () => {
    depth.downloading = "/data/other";
    await filesDownload(entry());
    expect(mintDownloadToken).not.toHaveBeenCalled();
  });
});
