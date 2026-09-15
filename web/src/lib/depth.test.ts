// The drill-in's `updating` label (#311). The pass that re-runs a spec's
// install script before a start (#307) has no state of its own — it reuses
// `installing` and is named by its opening console line — so these tests pin
// the one rule that keeps that honest: the store state decides, the log line
// only ever refines it.

import { beforeEach, describe, expect, it } from "vitest";
import {
  UPDATE_PASS_LINE,
  canShowInstallLog,
  consoleRepin,
  depth,
  powerControls,
  stateLabel,
  stream,
  surface,
  syncDepthFromFleet,
  syncUpdatePass,
} from "./depth.svelte";
import { fleet } from "./fleet.svelte";
import { serverMeta } from "./views.svelte";
import type { Server } from "@/api/types";

const UPDATING_LINE = {
  text: UPDATE_PASS_LINE + "dragonwilds-01 — re-running the install script before start",
};
const CONTAINER_LINE = { text: "LogInit: Display: BuildId 240163 — session created" };

function server(state: Server["state"]): Server {
  return {
    id: "srv-1",
    name: "dragonwilds-01",
    spec_id: "spec-1",
    node_id: "node-1",
    kind: "windows-native",
    state,
    vars: {},
    ports: { game: 27015 },
    memory_mb: 8192,
    created_at: "2026-09-15T09:46:00Z",
  };
}

/** Nothing here opens a socket — but syncDepthFromFleet re-targets the stream,
 *  and ServerStream reaches for the global WebSocket when it does. */
class FakeWebSocket {
  static readonly OPEN = 1;
  readyState = 0;
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: (() => void) | null = null;
  close() {}
  send() {}
}

beforeEach(() => {
  (globalThis as unknown as { WebSocket: unknown }).WebSocket = FakeWebSocket;
  depth.updatePass = false;
  depth.open = false;
  depth.serverId = null;
  depth.server = null;
  fleet.servers = [];
  fleet.specs = [];
  stream.set("", "off");
});

describe("syncUpdatePass", () => {
  it("labels an installing server `updating` once the pass's own line arrives", () => {
    syncUpdatePass("installing", []);
    expect(depth.updatePass).toBe(false);
    expect(stateLabel("installing", depth.updatePass)).toBe("installing");

    syncUpdatePass("installing", [UPDATING_LINE]);
    expect(depth.updatePass).toBe(true);
    expect(stateLabel("installing", depth.updatePass)).toBe("updating");
    // Mid-pass the server is down: start is the control, and it is the state
    // that says so, not the label.
    expect(powerControls("installing")).toBe("start");
  });

  it("clears the label when the server leaves installing, line still in the ring", () => {
    syncUpdatePass("installing", [UPDATING_LINE]);
    expect(depth.updatePass).toBe(true);

    // The pass ends. `installing` and `running` are both "live" streams, so the
    // socket is never re-targeted and the opening line is still in the buffer
    // beside the new container's output — this is #311's exact shape.
    syncUpdatePass("running", [UPDATING_LINE, CONTAINER_LINE]);

    expect(depth.updatePass).toBe(false);
    expect(stateLabel("running", depth.updatePass)).toBe("running");
    expect(powerControls("running")).toBe("stop");
  });

  it("does not re-latch when the pass's line arrives after the running push", () => {
    // The race the issue suspected: the state push lands first and the line
    // follows (fresh, or replayed from the retained buffer).
    syncUpdatePass("running", []);
    expect(depth.updatePass).toBe(false);

    syncUpdatePass("running", [UPDATING_LINE]);
    expect(depth.updatePass).toBe(false);
    syncUpdatePass("running", [UPDATING_LINE, CONTAINER_LINE]);
    expect(depth.updatePass).toBe(false);

    expect(stateLabel("running", depth.updatePass)).toBe("running");
    expect(powerControls("running")).toBe("stop");
  });

  it("clears on every state that is not installing", () => {
    const states: Server["state"][] = [
      "starting",
      "running",
      "stopping",
      "offline",
      "crashed",
      "install_failed",
    ];
    for (const st of states) {
      syncUpdatePass("installing", [UPDATING_LINE]);
      expect(depth.updatePass).toBe(true);
      syncUpdatePass(st, [UPDATING_LINE]);
      expect(depth.updatePass, `${st} must clear the latch`).toBe(false);
      expect(stateLabel(st, depth.updatePass)).toBe(st.replace("_", " "));
    }
  });

  it("never lets `updating` stand in for a state other than installing", () => {
    // Even with the latch wedged on by hand, the label follows the state.
    depth.updatePass = true;
    expect(stateLabel("running", true)).toBe("running");
    expect(stateLabel("crashed", true)).toBe("crashed");
    expect(stateLabel(undefined, true)).toBe("");
  });
});

describe("syncDepthFromFleet", () => {
  it("clears the latch with the state push that carries the new state", () => {
    depth.open = true;
    depth.serverId = "srv-1";
    depth.server = server("installing");
    // A live socket already targeted at this server, holding the pass's line.
    stream.set("srv-1", "live");
    stream.lines.push({ seq: 1, ts: 0, stream: "stdout", ...UPDATING_LINE, hidden: 0 });
    syncUpdatePass("installing", stream.lines);
    expect(depth.updatePass).toBe(true);

    // The 10s poll brings the finished pass back as `running`. The stream is
    // not re-targeted (both states are "live"), so the line survives — and the
    // latch must still go.
    fleet.servers = [server("running")];
    syncDepthFromFleet();

    expect(depth.server?.state).toBe("running");
    expect(stream.lines.some((l) => l.text.startsWith(UPDATE_PASS_LINE))).toBe(true);
    expect(depth.updatePass).toBe(false);
    expect(powerControls(depth.server?.state)).toBe("stop");
  });

  it("clears the latch when the drill-in closes", () => {
    depth.updatePass = true;
    surface();
    expect(depth.updatePass).toBe(false);
  });
});

// The INSTALL LOG chip and the console viewport it swaps (#314). Both buffers
// are 500-line rings, so the case that broke is the one where nothing about the
// render *size* changes across the swap.
describe("consoleRepin", () => {
  const SRV = "srv-1";
  /** The console pane's identity for one server, one stream generation, one
   *  buffer — exactly what Depth.svelte composes. */
  const key = (buffer: "live" | "install", gen = 1, srv = SRV) => [srv, gen, buffer].join("|");
  const view = (buffer: "live" | "install", lastSeq: number, gen = 1, status = "open") => ({
    key: key(buffer, gen),
    content: lastSeq + "|" + status,
  });

  it("re-pins on a buffer swap even when both are at the ring cap", () => {
    // The exact shape of the bug: a full live buffer, a full retained install
    // log, and an effect that could only see the count.
    expect(consoleRepin(view("live", 899), view("install", 499))).toBe("force");
    expect(consoleRepin(view("install", 499), view("live", 899))).toBe("force");
    // ...and it makes no difference whether the two happen to match.
    expect(consoleRepin(view("live", 499), view("install", 499))).toBe("force");
    expect(consoleRepin(null, view("live", -1))).toBe("force");
  });

  it("keeps following the tail once the ring is full", () => {
    // The live buffer pushes then splices back to its cap on every append, so
    // past 500 lines the count never changes again. A view keyed on the count
    // would go quiet here and a pinned console would stop following the tail
    // for the rest of the session.
    expect(consoleRepin(view("live", 900), view("live", 901))).toBe("if-pinned");
    expect(consoleRepin(view("live", 41), view("live", 42))).toBe("if-pinned");
  });

  it("re-pins when the stream is re-targeted to the same kind of buffer", () => {
    // crashed replay → start → a new container's live log. Same server, same
    // "live" buffer, and seqs that keep climbing because they are handed out at
    // receipt — only the stream's own generation says the document changed.
    expect(consoleRepin(view("live", 120, 1), view("live", 121, 2))).toBe("force");
  });

  it("re-pins when the drill-in moves to another server", () => {
    const a = { key: key("live", 1, "srv-1"), content: "10|open" };
    const b = { key: key("live", 1, "srv-2"), content: "10|open" };
    expect(consoleRepin(a, b)).toBe("force");
  });

  it("notices a banner that shares the scroll box with the lines", () => {
    // "stream lost — reconnecting" renders inside the scroller, so it changes
    // the height with no line change at all.
    expect(consoleRepin(view("live", 901, 1, "open"), view("live", 901, 1, "retrying"))).toBe(
      "if-pinned",
    );
  });

  it("does nothing when nothing changed", () => {
    expect(consoleRepin(view("live", 901), view("live", 901))).toBe("no");
  });
});

describe("the stream's generation", () => {
  it("advances whenever the buffer stops being a continuation of itself", () => {
    // What the console's document key is built on. Line seqs cannot carry this:
    // they are handed out at receipt and never reset, so a replay of the same
    // scrollback arrives looking like fresh output.
    stream.set("srv-1", "live");
    const first = stream.generation;
    stream.set("srv-1", "replay"); // crashed: the tail is replayed and ends
    expect(stream.generation).toBeGreaterThan(first);
    const second = stream.generation;
    stream.set("srv-2", "live"); // and drilling elsewhere
    expect(stream.generation).toBeGreaterThan(second);
    // A no-op re-target changes nothing — the buffer really is the same one.
    stream.set("srv-2", "live");
    expect(stream.generation).toBe(second + 1);
  });
});

describe("canShowInstallLog", () => {
  it("offers the chip on a server whose console is a container log", () => {
    // running / starting / offline / crashed: the pane is tailing a container,
    // so the retained install log is the one thing it cannot reach.
    expect(
      canShowInstallLog({ installing: false, hasRetained: true, consoleCarriesInstall: true }),
    ).toBe(true);
    expect(
      canShowInstallLog({ installing: false, hasRetained: true, consoleCarriesInstall: false }),
    ).toBe(true);
  });

  it("stays away for the whole of a running install", () => {
    // The stream gate serves the very buffer the chip snapshots, and during a
    // pass the socket is live or reconnecting throughout — so this is the only
    // reachable shape there, and a chip would swap a live tail for an older
    // copy of itself.
    expect(
      canShowInstallLog({ installing: true, hasRetained: true, consoleCarriesInstall: true }),
    ).toBe(false);
  });

  it("offers the retained copy after a failed install the socket never delivered", () => {
    // install_failed replays and ends; a socket blocked or proxied away leaves
    // the pane empty while the REST read still holds the lines.
    expect(
      canShowInstallLog({ installing: true, hasRetained: true, consoleCarriesInstall: false }),
    ).toBe(true);
  });

  it("never offers an empty log", () => {
    expect(
      canShowInstallLog({ installing: false, hasRetained: false, consoleCarriesInstall: false }),
    ).toBe(false);
    expect(
      canShowInstallLog({ installing: true, hasRetained: false, consoleCarriesInstall: false }),
    ).toBe(false);
  });
});

describe("the fleet card", () => {
  it("takes its status suffix from the state, never from the latch", () => {
    depth.updatePass = true;
    expect(serverMeta(server("installing"))).toContain("· installing");
    expect(serverMeta(server("running"))).not.toContain("·  ");
    expect(serverMeta(server("running"))).not.toContain("updating");
    expect(serverMeta(server("install_failed"))).toContain("· install failed");
  });
});
