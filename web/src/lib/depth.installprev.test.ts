// #381, the drill-in half. On 2026-09-25 an update pass on dragonwilds-01 was
// interrupted, REINSTALL was pressed, and the reinstall's fresh buffer erased
// the only record of why the pass had failed. Then the reinstall landed
// offline with no container on the node — expected, since START is what
// recreates it — and the empty console read as a dark server. These pin the
// three pieces the console pane now says about both.

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

import {
  FRESH_INSTALL_WINDOW_MS,
  depth,
  emptyConsoleNote,
  freshlyInstalled,
  previousAttemptHeader,
  syncDepthFromFleet,
} from "./depth.svelte";
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

describe("freshlyInstalled", () => {
  const now = Date.parse("2026-09-25T09:00:00Z");
  const at = (minutesAgo: number) => new Date(now - minutesAgo * 60_000).toISOString();

  it("is true for an offline server inside the Panel's 30-minute window", () => {
    expect(FRESH_INSTALL_WINDOW_MS).toBe(30 * 60_000);
    expect(freshlyInstalled({ state: "offline", provisioned_at: at(0) }, now)).toBe(true);
    expect(freshlyInstalled({ state: "offline", provisioned_at: at(29) }, now)).toBe(true);
  });

  it("is false once the window has passed", () => {
    expect(freshlyInstalled({ state: "offline", provisioned_at: at(30) }, now)).toBe(false);
  });

  it("is false for a stamp in the future, as the Panel reads one", () => {
    expect(freshlyInstalled({ state: "offline", provisioned_at: at(-5) }, now)).toBe(false);
  });

  it("is false without a stamp, with a bad one, or in any state but offline", () => {
    expect(freshlyInstalled({ state: "offline" }, now)).toBe(false);
    expect(freshlyInstalled({ state: "offline", provisioned_at: "not a time" }, now)).toBe(false);
    // Started: the container exists again, and the console is its output.
    expect(freshlyInstalled({ state: "running", provisioned_at: at(1) }, now)).toBe(false);
    expect(freshlyInstalled({ state: "crashed", provisioned_at: at(1) }, now)).toBe(false);
    expect(freshlyInstalled(null, now)).toBe(false);
  });
});

describe("emptyConsoleNote after a fresh install", () => {
  it("says there is no container until START instead of calling the server dark", () => {
    expect(emptyConsoleNote({ installing: false, hasRetained: true, freshInstall: true })).toBe(
      "installed · no container until START",
    );
    expect(emptyConsoleNote({ installing: false, hasRetained: true, freshInstall: false })).toBe(
      "no output — server is dark",
    );
  });

  it("leaves the install-time notes alone", () => {
    expect(emptyConsoleNote({ installing: true, hasRetained: true, freshInstall: true })).toContain(
      "chip above",
    );
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
  beforeEach(() => {
    getInstallLog.mockReset();
    getInstallLog.mockResolvedValue({ server_id: "srv-1", lines: [], done: false, retained: true, previous: null });
    depth.open = true;
    depth.serverId = "srv-1";
  });

  // The header names the attempt the new one replaced, which only a read taken
  // after the install began can know: the drill-in's last read holds the log
  // as it was before the button was pressed.
  it("re-reads the log when an offline server enters installing", async () => {
    depth.server = server("offline");
    fleet.servers = [server("installing")];
    syncDepthFromFleet();
    await Promise.resolve();
    expect(getInstallLog).toHaveBeenCalledWith("srv-1");
  });

  it("does not re-read while nothing changed", () => {
    depth.server = server("offline");
    fleet.servers = [server("offline")];
    syncDepthFromFleet();
    expect(getInstallLog).not.toHaveBeenCalled();
  });
});
