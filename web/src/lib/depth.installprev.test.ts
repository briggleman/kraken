// #381, the drill-in half. On 2026-09-25 an update pass on dragonwilds-01 was
// interrupted, REINSTALL was pressed, and the reinstall's fresh buffer erased
// the only record of why the pass had failed. The Panel now keeps that attempt
// as the log's `previous`; these pin how the console pane names it, and that
// the name is never the wrong attempt's.

import { beforeEach, describe, expect, it, vi } from "vitest";

const getInstallLog = vi.fn();

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ...actual,
    api: { ...actual.api, getInstallLog: (...a: unknown[]) => getInstallLog(...a) },
  };
});

// No socket: jsdom would only fail to connect it.
vi.mock("./stream.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./stream.svelte")>();
  class QuietStream {
    lines: unknown[] = [];
    set() {}
  }
  return { ...actual, ServerStream: QuietStream };
});

import { depth, previousAttemptHeader, refreshInstallLog, syncDepthFromFleet } from "./depth.svelte";
import { fleet } from "./fleet.svelte";
import type { InstallAttempt, Server } from "@/api/types";

/** A local-clock timestamp today, so the header's times read as HH:MM. */
function today(h: number, m: number): number {
  const d = new Date();
  d.setHours(h, m, 0, 0);
  return d.getTime();
}

function attempt(extra: Partial<InstallAttempt> = {}): InstallAttempt {
  return {
    done: true,
    started_ms: today(8, 2),
    finished_ms: today(8, 6),
    lines: [{ ts: today(8, 2), stream: "install", text: "[panel] updating dragonwilds-01" }],
    ...extra,
  };
}

describe("previousAttemptHeader", () => {
  it("reads a failed attempt as failed, with both times", () => {
    const failed = attempt({
      lines: [
        { ts: today(8, 2), stream: "install", text: " Update state (0x61) downloading" },
        { ts: today(8, 6), stream: "error", text: "[panel] install failed: state is 0x6" },
      ],
    });
    expect(previousAttemptHeader(failed)).toBe("previous attempt · started 08:02 · failed 08:06");
  });

  it("reads an attempt with no error line as finished", () => {
    expect(previousAttemptHeader(attempt())).toBe("previous attempt · started 08:02 · finished 08:06");
  });

  it("says an attempt superseded before its verdict did not finish", () => {
    expect(previousAttemptHeader(attempt({ finished_ms: undefined }))).toBe(
      "previous attempt · started 08:02 · did not finish",
    );
  });

  it("names the day for an attempt that was not today", () => {
    const d = new Date(today(8, 2));
    d.setDate(d.getDate() - 2);
    const header = previousAttemptHeader(attempt({ started_ms: d.getTime(), finished_ms: d.getTime() }));
    expect(header).toMatch(/^previous attempt · started [a-z]{3} \d{2} 08:02 · finished [a-z]{3} \d{2} 08:02$/);
  });
});

function server(state: Server["state"]): Server {
  return {
    id: "srv-1",
    name: "dragonwilds-01",
    spec_id: "spec-1",
    node_id: "node-1",
    kind: "windows-native",
    state,
    vars: {},
    ports: {},
    memory_mb: 8192,
    created_at: "2026-09-01T00:00:00Z",
  };
}

describe("the install log re-read when an install begins", () => {
  const stale = attempt({ started_ms: today(6, 0), finished_ms: today(6, 5) });
  const fresh = attempt({
    lines: [{ ts: today(8, 6), stream: "error", text: "[panel] install failed: state is 0x6" }],
  });

  beforeEach(() => {
    getInstallLog.mockReset();
    depth.open = true;
    depth.serverId = "srv-1";
    // What the drill-in held before REINSTALL: the failed 08:02 pass as the
    // current attempt, and an older one as its previous.
    depth.installLog = { server_id: "srv-1", lines: [], done: true, retained: true, previous: stale };
  });

  // The header names the attempt the new one replaced, which only a read taken
  // after the install began can know: until it lands there is no header at
  // all, rather than one naming the attempt before the wrong one.
  it("drops the held log at once and shows the new previous once the re-read lands", async () => {
    let answer!: (v: unknown) => void;
    getInstallLog.mockReturnValue(new Promise((r) => (answer = r)));
    depth.server = server("offline");
    fleet.servers = [server("installing")];
    syncDepthFromFleet();
    expect(getInstallLog).toHaveBeenCalledWith("srv-1");
    expect(depth.installLog).toBeNull();

    answer({ server_id: "srv-1", lines: [], done: false, retained: true, previous: fresh });
    await flush();
    expect(depth.installLog?.previous).toEqual(fresh);
    expect(previousAttemptHeader(depth.installLog!.previous!)).toBe(
      "previous attempt · started 08:02 · failed 08:06",
    );
  });

  it("leaves it dropped when the re-read fails", async () => {
    getInstallLog.mockRejectedValue(new Error("panel unreachable"));
    depth.server = server("install_failed");
    fleet.servers = [server("installing")];
    syncDepthFromFleet();
    await flush();
    expect(depth.installLog).toBeNull();
  });

  // An older read still in flight when the install began holds the log as it
  // was before the button press; it must not land over the newer one.
  it("ignores an older read that lands after the newer one", async () => {
    let answerOld!: (v: unknown) => void;
    getInstallLog.mockReturnValueOnce(new Promise((r) => (answerOld = r)));
    getInstallLog.mockResolvedValueOnce({ server_id: "srv-1", lines: [], done: false, retained: true, previous: fresh });
    const older = refreshInstallLog();
    depth.server = server("offline");
    fleet.servers = [server("installing")];
    syncDepthFromFleet();
    await flush();
    answerOld({ server_id: "srv-1", lines: [], done: true, retained: true, previous: stale });
    await older;
    expect(depth.installLog?.previous).toEqual(fresh);
  });

  it("does not re-read while nothing changed", () => {
    depth.server = server("offline");
    fleet.servers = [server("offline")];
    syncDepthFromFleet();
    expect(getInstallLog).not.toHaveBeenCalled();
  });
});

/** Let every settled promise's continuation run. */
const flush = () => new Promise((r) => setTimeout(r, 0));
