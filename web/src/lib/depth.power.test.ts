// The drill-in's power controls and the notice above them. A start the panel
// refuses puts its reason on screen — for a required setting, the field to fill
// in and where. The regression these pin: that notice outliving its own fix,
// so a server the operator had just repaired and started ran under a banner
// still saying it could not start.

import { beforeEach, describe, expect, it, vi } from "vitest";

const powerServer = vi.fn();

// ApiError and errMsg are the real ones: what the notice says here is what it
// says in the app.
vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ApiError: actual.ApiError,
    errMsg: actual.errMsg,
    api: { powerServer: (...a: unknown[]) => powerServer(...a) },
  };
});

// power() refreshes the fleet after it acts; nothing here is about the fleet.
vi.mock("./fleet.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./fleet.svelte")>();
  return { ...actual, refreshFleet: vi.fn(async () => {}) };
});

import { ApiError } from "@/api/client";
import { depth, power } from "./depth.svelte";

const REFUSAL = "Owner Player ID is required before this server can start — set it on the Settings tab";

beforeEach(() => {
  vi.clearAllMocks();
  depth.serverId = "srv-1";
  depth.server = null;
  depth.powerBusy = false;
  depth.error = null;
});

describe("power", () => {
  it("puts a refusal's reason on screen", async () => {
    powerServer.mockRejectedValueOnce(new ApiError(409, REFUSAL, "required_settings_missing"));
    await power("start");
    expect(depth.error).toBe(REFUSAL);
    expect(depth.powerBusy).toBe(false);
  });

  it("clears the refusal once a later attempt succeeds", async () => {
    powerServer.mockRejectedValueOnce(new ApiError(409, REFUSAL, "required_settings_missing"));
    await power("start");
    expect(depth.error).toBe(REFUSAL);

    // The operator fills in the field and starts again, and this time it works.
    powerServer.mockResolvedValueOnce({ state: "running" });
    await power("start");
    expect(depth.error).toBeNull();
  });

  it("replaces an old notice with the new attempt's own reason", async () => {
    powerServer.mockRejectedValueOnce(new ApiError(409, REFUSAL, "required_settings_missing"));
    await power("start");
    powerServer.mockRejectedValueOnce(new ApiError(503, "node local is offline"));
    await power("start");
    expect(depth.error).toBe("node local is offline");
  });
});
