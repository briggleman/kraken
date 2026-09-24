// What a schedule row says about its last run. The Panel records a run that
// did nothing in `last_error` — a scheduled restart skipped on a stopped server,
// or refused for an empty required setting — and a row that showed only the
// next run would make every one of those invisible.

import type { ScheduledTask } from "@/api/types";
import { fmtWhen } from "./fmt";

export interface LastRun {
  /** "last run today 04:00", or "not run yet". */
  when: string;
  /** Why the last run did nothing; null when it went through (or never ran). */
  error: string | null;
}

export function lastRun(t: Pick<ScheduledTask, "last_run_at" | "last_error">): LastRun {
  const ms = t.last_run_at ? new Date(t.last_run_at).getTime() : NaN;
  const when = Number.isFinite(ms) ? "last run " + fmtWhen(ms) : "not run yet";
  const error = t.last_error?.trim() ? t.last_error.trim() : null;
  return { when, error };
}
