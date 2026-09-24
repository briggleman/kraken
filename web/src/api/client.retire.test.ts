// The retire model's three calls (#360): what each sends, and that the delete
// is now the permanent one, answering a note rather than nothing.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError, api } from "./client";

const fetchMock = vi.fn();

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function sent(): { url: string; method: string; body: unknown } {
  const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
  return { url, method: String(init.method), body: init.body ? JSON.parse(String(init.body)) : undefined };
}

describe("retireServer", () => {
  it("takes the final backup unless told not to", async () => {
    fetchMock.mockImplementation(async () => jsonResponse(202, { id: "sv-1", state: "offline", retire: { phase: "stopping" } }));
    const sv = await api.retireServer("sv-1");
    expect(sent()).toEqual({ url: "/api/v1/servers/sv-1/retire", method: "POST", body: { final_backup: true } });
    expect(sv.retire?.phase).toBe("stopping");

    fetchMock.mockClear();
    await api.retireServer("sv-1", false);
    expect(sent().body).toEqual({ final_backup: false });
  });
});

describe("reviveServer", () => {
  it("sends what it was given, and nothing when given nothing", async () => {
    fetchMock.mockImplementation(async () => jsonResponse(202, { id: "sv-1", state: "installing" }));
    await api.reviveServer("sv-1", { restore_backup_id: "1700__final-before-retire", start: true });
    expect(sent()).toEqual({
      url: "/api/v1/servers/sv-1/revive",
      method: "POST",
      body: { restore_backup_id: "1700__final-before-retire", start: true },
    });

    fetchMock.mockClear();
    await api.reviveServer("sv-1");
    expect(sent().body).toEqual({});
  });

  it("carries the Panel's refusal code", async () => {
    fetchMock.mockImplementation(async () =>
      jsonResponse(409, { error: "removing this server's old containers and world is still queued", code: "removal_pending" }),
    );
    await expect(api.reviveServer("sv-1")).rejects.toMatchObject({ code: "removal_pending" });
  });
});

describe("deleteServer", () => {
  it("is the permanent delete and answers the note", async () => {
    fetchMock.mockImplementation(async () =>
      jsonResponse(200, { note: "archives on a shared backup target were kept (the network share)", removal_pending: false }),
    );
    const r = await api.deleteServer("sv-1");
    expect(sent()).toEqual({ url: "/api/v1/servers/sv-1", method: "DELETE", body: undefined });
    expect(r.note).toMatch(/kept/);
  });

  it("is refused on a live server with server_not_retired", async () => {
    fetchMock.mockImplementation(async () => jsonResponse(409, { error: "retire the server first", code: "server_not_retired" }));
    const e = await api.deleteServer("sv-1").catch((x: unknown) => x);
    expect(e).toBeInstanceOf(ApiError);
    expect((e as ApiError).code).toBe("server_not_retired");
  });
});
