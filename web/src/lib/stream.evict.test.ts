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
    s.flush();

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
    s.flush();
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
    s.flush();
    expect(s.lines.length).toBe(500);
    expect(s.lines[0].text).toBe("[panel] step 5");
    s.set("", "off");
  });

  it("evicts the same way whether lines arrive one flush at a time or in one batch", () => {
    const say = (ws: FakeWebSocket, i: number) =>
      i % 50 === 0 ? ws.say("system", `[panel] step ${i}`) : ws.say("install", `progress ${i}`);
    const oneByOne = new ServerStream();
    oneByOne.set("sv-a", "live");
    for (let i = 0; i < 1200; i++) {
      say(sockets[0], i);
      oneByOne.flush();
    }
    const batched = new ServerStream();
    batched.set("sv-b", "live");
    for (let i = 0; i < 1200; i++) say(sockets[1], i);
    batched.flush();
    expect(batched.lines.map((l) => l.text)).toEqual(oneByOne.lines.map((l) => l.text));
    oneByOne.set("", "off");
    batched.set("", "off");
  });
});

describe("console lines are applied once per frame, not once per message", () => {
  it("holds a burst of frames until the next flush, then applies them together", () => {
    const s = new ServerStream();
    s.set("sv-4", "live");
    const ws = sockets[0];
    for (let i = 0; i < 200; i++) ws.say("stdout", `replayed ${i}`);
    // A connect's replay arrives as ~200 separate messages; none of them
    // touches the reactive buffer on its own.
    expect(s.lines.length).toBe(0);
    s.flush();
    expect(s.lines.length).toBe(200);
    expect(s.lines[0].text).toBe("replayed 0");
    expect(s.lines.at(-1)?.text).toBe("replayed 199");
    s.set("", "off");
  });

  it("applies the queue on the next animation frame without being asked", async () => {
    const s = new ServerStream();
    s.set("sv-5", "live");
    sockets[0].say("stdout", "one");
    sockets[0].say("stdout", "two");
    await new Promise((r) => requestAnimationFrame(() => r(undefined)));
    expect(s.lines.map((l) => l.text)).toEqual(["one", "two"]);
    s.set("", "off");
  });

  it("drops queued lines from the old buffer when it re-targets", () => {
    const s = new ServerStream();
    s.set("sv-6", "live");
    sockets[0].say("stdout", "from sv-6");
    s.set("sv-7", "live");
    s.flush();
    expect(s.lines).toEqual([]);
    s.set("", "off");
  });

  it("drops queued lines when a reconnect opens and replays the tail again", () => {
    const s = new ServerStream();
    s.set("sv-8", "live");
    const ws = sockets[0];
    ws.say("stdout", "before the reconnect");
    ws.onopen?.();
    s.flush();
    expect(s.lines).toEqual([]);
    s.set("", "off");
  });
});
