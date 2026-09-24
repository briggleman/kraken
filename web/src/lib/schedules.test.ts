// The schedule row's last-run line. Every "skipped" outcome of a scheduled
// restart lands in last_error, so the row has to carry it or the skip is
// invisible.

import { describe, expect, it } from "vitest";
import { lastRun } from "./schedules";

describe("lastRun", () => {
  it("says a schedule that has never fired has not run", () => {
    expect(lastRun({})).toEqual({ when: "not run yet", error: null });
  });

  it("names when a clean run happened, with no error", () => {
    const r = lastRun({ last_run_at: "2026-09-24T04:00:00Z" });
    expect(r.when).toMatch(/^last run /);
    expect(r.error).toBeNull();
  });

  it("carries the reason a run did nothing", () => {
    const reason = "server is offline, so the scheduled restart was skipped — a restart would start a server someone had stopped";
    const r = lastRun({ last_run_at: "2026-09-24T04:00:00Z", last_error: reason });
    expect(r.error).toBe(reason);
  });

  it("treats a blank error as none", () => {
    expect(lastRun({ last_run_at: "2026-09-24T04:00:00Z", last_error: "  " }).error).toBeNull();
  });

  it("does not print a date it cannot parse", () => {
    expect(lastRun({ last_run_at: "not a date" }).when).toBe("not run yet");
  });
});
