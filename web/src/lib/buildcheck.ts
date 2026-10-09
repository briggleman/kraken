// The Steam build check as the panel reads it (#392): the build a server's
// install tree holds against the build its branch ships now, said in four
// places — the drill-in's build line, the server card, the node band and the
// top bar — in two voices: plain when current, Caution Violet when a newer
// build is waiting (DESIGN.md, Build Check; The Old Build Turns Players Away
// Rule). Everything here is pure so each reading has its own test.
import type { Node, Server } from "@/api/types";
import { fmtAgo } from "./fmt";

/** What the drill-in's build line says, or null when it says nothing at all:
 *  a server with no build to check (not a Steam install, or `update_check:
 *  none`) and one the panel has never reported on carry no line. */
export interface BuildLine {
  /** The installed build id, or "—" when no check has read it yet. */
  build: string;
  /** The status after the id; "" when the last check could not compare. */
  status: string;
  /** True when the status takes Caution Violet: a newer build is waiting. */
  avail: boolean;
  /** Why the last check could not compare, for the line's title. */
  title?: string;
}

/** `updating` is the drill-in's latch for "this `installing` is the
 *  pre-start update pass", the one `stateLabel` reads. */
export function buildLine(server: Server | null | undefined, updating: boolean, now = Date.now()): BuildLine | null {
  const u = server?.update;
  if (!server || !u || u.status === "unsupported") return null;
  const build = u.installed_build || "—";
  switch (u.status) {
    case "current":
      return {
        build,
        status: u.checked_at ? `current · checked ${fmtAgo(Date.parse(u.checked_at), now)}` : "current",
        avail: false,
      };
    case "available": {
      const to = u.available_build ?? "";
      if (server.state === "installing" && updating) {
        return { build, status: `→ ${to} · updating now`, avail: true };
      }
      // What applies it: a start re-runs the pass on a stopped server, a
      // restart on a running one. Anything mid-flight says only that it waits.
      const act =
        server.state === "running" || server.state === "starting"
          ? " · restart to apply"
          : server.state === "offline" || server.state === "crashed"
            ? " · start to apply"
            : "";
      return { build, status: `update available · ${to}${act}`, avail: true };
    }
    default:
      // unknown: no third colour and no status word — an unknown build is not a
      // condition anyone can act on. The reason rides in the title.
      return { build, status: "", avail: false, title: u.error || undefined };
  }
}

/** Whether a server's card ends its meta line in "update available". */
export function cardUpdateAvailable(server: Server): boolean {
  return server.update?.status === "available" && server.state !== "retired" && server.state !== "retiring";
}

/** "dragonwilds-01 · 25630937 → 25805654" — one server in a roll call. */
function rollCallEntry(s: Server): string {
  return `${s.name} · ${s.update?.installed_build ?? "?"} → ${s.update?.available_build ?? "?"}`;
}

/** The node band's Updates Available condition: how many of the node's
 *  servers have a newer build waiting, with the roll call for the title, or
 *  null when none do. */
export function nodeUpdatesNote(node: Node, servers: readonly Server[]): { count: number; title: string } | null {
  const behind = servers.filter((s) => s.node_id === node.id && cardUpdateAvailable(s));
  if (behind.length === 0) return null;
  return { count: behind.length, title: behind.map(rollCallEntry).join(", ") };
}

/** The top bar's fleet-wide count, or null at zero. */
export function fleetUpdatesNote(servers: readonly Server[]): { label: string; title: string } | null {
  const behind = servers.filter(cardUpdateAvailable);
  if (behind.length === 0) return null;
  const n = behind.length;
  return {
    label: `${n} ${n === 1 ? "update" : "updates"} available`,
    title: behind.map((s) => `${s.name} has a newer Steam build waiting`).join("; "),
  };
}
