// How a failed request becomes an ApiError (#352). The Panel answers an Agent
// failure with `{error, code}` under a status whose body survives the edge; the
// client has to put that sentence on screen — and, when an edge did eat the
// body, still say something. These pin the two calls that used to fall back to
// a bare statusText, which is "" on HTTP/2: the raw-body spec upload and the
// multipart file upload.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError, api } from "./client";

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** What an edge proxy that replaced the body leaves: a status, no reason
 *  phrase (HTTP/2), nothing parseable. */
function emptyResponse(status: number): Response {
  return new Response("", { status, statusText: "" });
}

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

async function caught(p: Promise<unknown>): Promise<ApiError> {
  try {
    await p;
  } catch (e) {
    expect(e).toBeInstanceOf(ApiError);
    return e as ApiError;
  }
  throw new Error("expected the call to fail");
}

const upload = () => api.uploadFiles("srv-1", "/data", [new File(["x"], "world.sav")]);
const specRaw = () => api.createSpecRaw("name: x\n");

describe.each([
  ["uploadFiles", upload],
  ["createSpecRaw (requestRaw)", specRaw],
])("%s", (_name, call) => {
  it("surfaces the Panel's message and code from a JSON error body", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(409, {
        error: "/data/Saves/world.sav is in use by another process — a game container may still be running",
        code: "file_in_use",
      }),
    );
    const e = await caught(call());
    expect(e.status).toBe(409);
    expect(e.code).toBe("file_in_use");
    expect(e.message).toContain("in use by another process");
  });

  it("never has an empty message when the body is gone", async () => {
    fetchMock.mockResolvedValue(emptyResponse(503));
    const e = await caught(call());
    expect(e.status).toBe(503);
    expect(e.message).toBe("HTTP 503");
    expect(e.code).toBeUndefined();
  });
});
