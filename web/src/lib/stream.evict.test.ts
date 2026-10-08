// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ServerStream } from "./stream.svelte";

/** Captures the socket the stream opens so a test can push frames into it. */
let sockets: FakeWebSocket[] = [];
class FakeWebSocket {
  static readonly OPEN = 1;
  readyState = 0;
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  constructor() {
    sockets.push(this);
  }
  close() {}
  send() {}
  /** Deliver one console frame as the Panel would write it. */
  say(stream: string, text: string) {
    this.onmessage?.({ data: JSON.stringify({ type: "console", ts: 1, stream, text }) });
  }
}

beforeEach(() => {
  sockets = [];
  (globalThis as unknown as { WebSocket: unknown }).WebSocket = FakeWebSocket;
});
afterEach(() => {
  sockets = [];
});

describe("the console ring under a chatty installer", () => {
  it("evicts installer output before Kraken's own step lines (#392)", () => {
    const s = new ServerStream();
    s.set("sv-1", "live");
    const ws = sockets[0];
    expect(ws).toBeDefined();

    ws.say("system", "[panel] stopping sv-1 before the update");
    ws.say("system", "[panel] stop took 5ms");
    ws.say("system", "[kraken] image check took 329ms");
    for (let i = 0; i < 2000; i++) ws.say("install", ` Update state (0x5) validating, progress: ${i}`);
    ws.say("error", "[panel] install failed: state is 0x6");

    expect(s.lines.length).toBe(500);
    expect(s.lines.slice(0, 3).map((l) => l.text)).toEqual([
      "[panel] stopping sv-1 before the update",
      "[panel] stop took 5ms",
      "[kraken] image check took 329ms",
    ]);
    expect(s.lines.at(-1)?.stream).toBe("error");
    // The newest installer lines are the ones kept, in order.
    expect(s.lines[3].text).toBe(" Update state (0x5) validating, progress: 1504");
    expect(s.lines.at(-2)?.text).toBe(" Update state (0x5) validating, progress: 1999");
    s.set("", "off");
  });

  it("strips ANSI escapes from what it renders, Steam's colour resets included", () => {
    const s = new ServerStream();
    s.set("sv-3", "live");
    const ws = sockets[0];
    ws.say("install", "\u001b[0m Update state (0x5) verifying install, progress: 36.08");
    ws.say("install", "Loading Steam API...\u001b[0mOK");
    ws.say("stdout", "\u001b[1;32m[Server]\u001b[0m world saved");
    expect(s.lines.map((l) => l.text)).toEqual([
      " Update state (0x5) verifying install, progress: 36.08",
      "Loading Steam API...OK",
      "[Server] world saved",
    ]);
    s.set("", "off");
  });

  it("still caps the ring when every line is Kraken's own", () => {
    const s = new ServerStream();
    s.set("sv-2", "live");
    const ws = sockets[0];
    for (let i = 0; i < 505; i++) ws.say("system", `[panel] step ${i}`);
    expect(s.lines.length).toBe(500);
    expect(s.lines[0].text).toBe("[panel] step 5");
    s.set("", "off");
  });
});
