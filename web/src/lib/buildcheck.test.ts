import { describe, expect, it } from "vitest";
import { buildLine, cardUpdateAvailable, fleetUpdatesNote, nodeUpdatesNote } from "./buildcheck";
import type { Node, Server, ServerUpdate } from "@/api/types";

const NOW = Date.parse("2026-10-09T15:00:00Z");
const threeHoursAgo = new Date(NOW - 3 * 3_600_000).toISOString();

function server(state: Server["state"], update?: ServerUpdate, extra: Partial<Server> = {}): Server {
  return { id: "s1", name: "dragonwilds-01", node_id: "n1", state, update, ...extra } as Server;
}

const current: ServerUpdate = { status: "current", installed_build: "25630937", available_build: "25630937", checked_at: threeHoursAgo };
const available: ServerUpdate = { status: "available", installed_build: "25630937", available_build: "25805654", checked_at: threeHoursAgo };

describe("buildLine", () => {
  it("says nothing for a server with no build to check, or no report yet", () => {
    expect(buildLine(server("running", { status: "unsupported" }), false, NOW)).toBeNull();
    expect(buildLine(server("running", undefined), false, NOW)).toBeNull();
    expect(buildLine(null, false, NOW)).toBeNull();
  });

  it("is plain when current, with how long ago it was checked", () => {
    expect(buildLine(server("running", current), false, NOW)).toEqual({
      build: "25630937",
      status: "current · checked 3h ago",
      avail: false,
    });
  });

  it("turns violet and names what applies it when a newer build is waiting", () => {
    expect(buildLine(server("offline", available), false, NOW)).toEqual({
      build: "25630937",
      status: "update available · 25805654 · start to apply",
      avail: true,
    });
    expect(buildLine(server("running", available), false, NOW)?.status).toBe(
      "update available · 25805654 · restart to apply",
    );
    // Mid-flight states only say it waits.
    expect(buildLine(server("stopping", available), false, NOW)?.status).toBe("update available · 25805654");
  });

  it("says the pass is applying it while the pre-start update runs", () => {
    expect(buildLine(server("installing", available), true, NOW)).toEqual({
      build: "25630937",
      status: "→ 25805654 · updating now",
      avail: true,
    });
    // A create or reinstall (no updating latch) is not "updating now".
    expect(buildLine(server("installing", available), false, NOW)?.status).toBe("update available · 25805654");
  });

  it("shows no status word when the last check could not compare, with the reason in the title", () => {
    expect(
      buildLine(server("running", { status: "unknown", installed_build: "25630937", error: "node abyss-win is offline" }), false, NOW),
    ).toEqual({ build: "25630937", status: "", avail: false, title: "node abyss-win is offline" });
    expect(buildLine(server("running", { status: "unknown" }), false, NOW)).toEqual({
      build: "—",
      status: "",
      avail: false,
      title: undefined,
    });
  });
});

describe("the fleet's counts", () => {
  const behind = server("offline", available);
  const ok = server("running", current, { id: "s2", name: "palworld-01" });
  const elsewhere = server("running", available, { id: "s3", name: "valheim-01", node_id: "n2" });
  const retiring = server("retiring", available, { id: "s4", name: "gone-01" });

  it("a card says so only when a newer build is waiting on a server that is staying", () => {
    expect(cardUpdateAvailable(behind)).toBe(true);
    expect(cardUpdateAvailable(ok)).toBe(false);
    expect(cardUpdateAvailable(retiring)).toBe(false);
  });

  it("a node counts its own servers that are behind, with the roll call", () => {
    expect(nodeUpdatesNote({ id: "n1" } as Node, [behind, ok, elsewhere, retiring])).toEqual({
      count: 1,
      title: "dragonwilds-01 · 25630937 → 25805654",
    });
    expect(nodeUpdatesNote({ id: "n9" } as Node, [behind, ok])).toBeNull();
  });

  it("the top bar counts the whole fleet and is absent at zero", () => {
    expect(fleetUpdatesNote([behind, ok, elsewhere])?.label).toBe("2 updates available");
    expect(fleetUpdatesNote([behind, ok])?.label).toBe("1 update available");
    expect(fleetUpdatesNote([ok])).toBeNull();
  });
});
