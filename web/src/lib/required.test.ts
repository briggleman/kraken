// Required game settings. The panel refuses a start while a required setting
// is empty; these pin that the UI asks the same question the same way, so the
// deploy form never offers an auto-start the panel will turn down.

import { describe, expect, it } from "vitest";

import { fieldList, isFromSpec, isMissing, missingOnDeploy } from "./required";
import type { SettingField, Spec } from "@/api/types";

const field = (over: Partial<SettingField>): SettingField => ({ key: "k", type: "string", ...over });

function specWith(fields: SettingField[]): Spec {
  return {
    id: "s", name: "Game", slug: "game", version: 1, platforms: [],
    resources: { min_memory_mb: 1024 },
    settings: { groups: [{ id: "server", fields }] },
  };
}

describe("isMissing", () => {
  it("is only ever true for a required field", () => {
    expect(isMissing(field({}), "")).toBe(false);
    expect(isMissing(field({ required: true }), "")).toBe(true);
  });

  it("treats whitespace and absence as missing, like the panel", () => {
    const f = field({ required: true });
    expect(isMissing(f, "   ")).toBe(true);
    expect(isMissing(f, undefined)).toBe(true);
    expect(isMissing(f, "00023a5e")).toBe(false);
  });
});

describe("missingOnDeploy", () => {
  it("names the required fields a fresh server boots without — the Dragonwilds case", () => {
    const spec = specWith([
      field({ key: "OwnerId", label: "Owner Player ID", required: true, default: "" }),
      field({ key: "ServerName", default: "Kraken" }),
    ]);
    expect(missingOnDeploy(spec).map((f) => f.key)).toEqual(["OwnerId"]);
  });

  it("counts a required field as satisfied when its default fills it", () => {
    const spec = specWith([field({ key: "Region", required: true, default: "us" })]);
    expect(missingOnDeploy(spec)).toEqual([]);
  });

  it("is empty for a spec with no settings, and for no spec at all", () => {
    expect(missingOnDeploy(specWith([]))).toEqual([]);
    expect(missingOnDeploy({ ...specWith([]), settings: undefined })).toEqual([]);
    expect(missingOnDeploy(undefined)).toEqual([]);
  });

  it("keeps declared order across groups", () => {
    const spec: Spec = {
      ...specWith([]),
      settings: {
        groups: [
          { id: "a", fields: [field({ key: "First", required: true })] },
          { id: "b", fields: [field({ key: "Second", required: true })] },
        ],
      },
    };
    expect(missingOnDeploy(spec).map((f) => f.key)).toEqual(["First", "Second"]);
  });
});

describe("fieldList", () => {
  it("joins by label, falling back to the key", () => {
    expect(fieldList([field({ key: "OwnerId", label: "Owner Player ID" })])).toBe("Owner Player ID");
    expect(fieldList([field({ key: "a", label: "A" }), field({ key: "b" })])).toBe("A and b");
    expect(fieldList([field({ label: "A" }), field({ label: "B" }), field({ label: "C" })])).toBe("A, B and C");
    expect(fieldList([])).toBe("");
  });
});

describe("isFromSpec", () => {
  const owner = field({ key: "OwnerId", required: true });

  it("marks a required field the panel says came from the spec", () => {
    expect(isFromSpec(owner, ["OwnerId"], undefined)).toBe(true);
  });

  it("drops the moment the operator types, even back to empty", () => {
    expect(isFromSpec(owner, ["OwnerId"], "0")).toBe(false);
    expect(isFromSpec(owner, ["OwnerId"], "")).toBe(false);
  });

  it("is false for the server's own value", () => {
    expect(isFromSpec(owner, [], undefined)).toBe(false);
    expect(isFromSpec(owner, undefined, undefined)).toBe(false);
  });

  it("never marks a field that is not required", () => {
    expect(isFromSpec(field({ key: "ServerName" }), ["ServerName"], undefined)).toBe(false);
  });
});
