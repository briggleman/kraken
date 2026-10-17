// The invite helpers: which paths are invite links, what the chip says, and
// when the create form may offer an invite.

import { describe, expect, it } from "vitest";
import { inviteChip, inviteLeft, inviteModeAvailable, inviteTokenFromPath } from "./invite";

const TOKEN = "Q2x9vJq0sN4rT7wZc1kFh8uLm3bXaE5dGpYoRiVtK6M";

describe("inviteTokenFromPath", () => {
  it("reads the token out of an emailed link's path", () => {
    expect(inviteTokenFromPath(`/invite/${TOKEN}`)).toBe(TOKEN);
    expect(inviteTokenFromPath(`/invite/${TOKEN}/`)).toBe(TOKEN);
  });
  it("is not fooled by other paths", () => {
    for (const p of ["/", "/servers/abc", "/invite", "/invite/", "/invite/short", `/invite/${TOKEN}/x`, `/invite/${TOKEN}?x=1`, "/invite/has space here 123456"]) {
      expect(inviteTokenFromPath(p)).toBeNull();
    }
  });
});

describe("inviteLeft", () => {
  const now = Date.parse("2026-10-17T12:00:00Z");
  it("rounds to hours when there is more than an hour and a half", () => {
    expect(inviteLeft("2026-10-18T12:00:00Z", now)).toBe("24h left");
    expect(inviteLeft("2026-10-17T13:40:00Z", now)).toBe("2h left");
  });
  it("counts minutes under ninety", () => {
    expect(inviteLeft("2026-10-17T12:45:00Z", now)).toBe("45m left");
    expect(inviteLeft("2026-10-17T12:00:30Z", now)).toBe("under a minute left");
  });
  it("says expired for the past and for garbage", () => {
    expect(inviteLeft("2026-10-17T11:59:00Z", now)).toBe("expired");
    expect(inviteLeft("not a date", now)).toBe("expired");
  });
});

describe("inviteChip", () => {
  const now = Date.parse("2026-10-17T12:00:00Z");
  it("is absent for an account with no open invite", () => {
    expect(inviteChip(undefined, now)).toBeNull();
    expect(inviteChip(null, now)).toBeNull();
  });
  it("lights a live invite and names what is left", () => {
    expect(inviteChip({ state: "sent", sent_to: "a@b.c", expires_at: "2026-10-18T11:00:00Z" }, now)).toEqual({ text: "invited · 23h left", cls: "ok" });
  });
  it("reads expired and failed in the caution colour", () => {
    expect(inviteChip({ state: "expired", sent_to: "a@b.c", expires_at: "2026-10-16T12:00:00Z" }, now)).toEqual({ text: "invite expired", cls: "bad" });
    expect(inviteChip({ state: "failed", sent_to: "a@b.c", expires_at: "2026-10-18T12:00:00Z" }, now)).toEqual({ text: "invite failed", cls: "bad" });
    // The clock can pass the expiry between the list being fetched and drawn.
    expect(inviteChip({ state: "sent", sent_to: "a@b.c", expires_at: "2026-10-17T11:00:00Z" }, now)).toEqual({ text: "invite expired", cls: "bad" });
  });
});

describe("inviteModeAvailable", () => {
  const ready = { smtp_configured: true, public_url: "https://kraken.example.com" };
  it("needs a relay, a public address and an email address", () => {
    expect(inviteModeAvailable(ready, "captain@example.org")).toBe(true);
    expect(inviteModeAvailable({ ...ready, smtp_configured: false }, "captain@example.org")).toBe(false);
    expect(inviteModeAvailable({ ...ready, public_url: "" }, "captain@example.org")).toBe(false);
    expect(inviteModeAvailable(ready, "")).toBe(false);
    expect(inviteModeAvailable(ready, "not-an-address")).toBe(false);
  });
  it("offers nothing when the settings did not load", () => {
    expect(inviteModeAvailable(null, "captain@example.org")).toBe(false);
    expect(inviteModeAvailable(undefined, "captain@example.org")).toBe(false);
  });
});
