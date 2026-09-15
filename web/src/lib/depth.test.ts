// The drill-in's `updating` label (#311). The pass that re-runs a spec's
// install script before a start (#307) has no state of its own — it reuses
// `installing` and is named by its opening console line — so these tests pin
// the one rule that keeps that honest: the store state decides, the log line
// only ever refines it.

import { beforeEach, describe, expect, it } from "vitest";
import {
  UPDATE_PASS_LINE,
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

describe("the fleet card", () => {
  it("takes its status suffix from the state, never from the latch", () => {
    depth.updatePass = true;
    expect(serverMeta(server("installing"))).toContain("· installing");
    expect(serverMeta(server("running"))).not.toContain("·  ");
    expect(serverMeta(server("running"))).not.toContain("updating");
    expect(serverMeta(server("install_failed"))).toContain("· install failed");
  });
});
