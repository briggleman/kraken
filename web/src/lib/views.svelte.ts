// View models: fleet API objects mapped into the shapes the pane renders.
//
// Node band instruments are real — see lib/telemetry.svelte.ts. What remains
// synthetic here is the SERVER CARD cpu/mem tracks: real per-server stats exist
// only on the drill-in WebSocket, one server at a time, so the fleet view has
// no feed to draw from yet (phase 2 of the node & fleet telemetry issue).
// Tracks are cached per server so the walks keep their history across polls.

import type { Node, PendingRemoval, Server, Spec } from "@/api/types";
import { fleet, specOf, nodeOf } from "./fleet.svelte";
import { seedHistory, type WalkSpec } from "./walk";
import { fmtGb } from "./fmt";

export interface Track {
  walk: WalkSpec;
  history: number[];
}

function track(spec: WalkSpec, length: number): Track {
  return { walk: { ...spec }, history: seedHistory(spec, length) };
}

const CARD_TRACK = 72;

const cardTracks = new Map<string, { cpu: Track; mem: Track }>();

export function cardTracksFor(server: Server): { cpu: Track; mem: Track } {
  let t = cardTracks.get(server.id);
  if (!t) {
    t = {
      cpu: track({ v: 15 + Math.random() * 20, lo: 5, hi: 60, step: 5 }, CARD_TRACK),
      mem: track({ v: 40 + Math.random() * 15, lo: 30, hi: 70, step: 4 }, CARD_TRACK),
    };
    cardTracks.set(server.id, t);
  }
  return t;
}

export function allSyntheticTracks(): Track[] {
  const out: Track[] = [];
  // card walks only advance for servers that are actually running
  for (const s of fleet.servers) {
    if (s.state !== "running") continue;
    const t = cardTracks.get(s.id);
    if (t) out.push(t.cpu, t.mem);
  }
  return out;
}

// ---------------------------------------------------------------------------

/** running/starting carry the light (a live thing); everything else is dark. */
export function chipKind(state: Server["state"]): "run" | "stop" {
  return state === "running" || state === "starting" ? "run" : "stop";
}

export function gamePort(server: Server): number | undefined {
  const ports = server.ports ?? {};
  return ports["game"] ?? Object.values(ports)[0];
}

export function serverMeta(server: Server): string {
  const spec = specOf(server);
  const port = gamePort(server);
  const bits = [spec?.slug ?? server.spec_id, spec ? `v${spec.version}` : "", port ? `:${port}` : ""]
    .filter(Boolean)
    .join(" · ");
  return server.state === "running" ? bits : `${bits} · ${server.state.replace("_", " ")}`;
}

export function serverArt(server: Server): string | undefined {
  return specOf(server)?.banner_url || undefined;
}

export function rackOf(server: Server): { node: string; host: string } {
  const node = nodeOf(server);
  const idx = node ? fleet.nodes.indexOf(node) : -1;
  return {
    node: idx >= 0 ? `node ${String(idx + 1).padStart(2, "0")}` : "node —",
    host: node?.name ?? server.node_id,
  };
}

export function playersLabel(server: Server): { num: string; max: string; pct: number } {
  if (server.state !== "running" || !server.players_known) {
    return { num: "—", max: "", pct: 0 };
  }
  const max = server.max_players ?? 0;
  const players = server.players ?? 0;
  return {
    num: String(players),
    max: max ? String(max) : "?",
    pct: max ? Math.round((players / max) * 100) : 0,
  };
}

/**
 * The node band's memory readout: memory the scheduler has committed to servers
 * against the node's total. This is the number that decides whether the next
 * server fits, and unlike host usage it is known even when the agent is not
 * reachable.
 */
export function nodeMemLabel(node: Node): { used: string; total: string } {
  return {
    used: node.total_memory_mb ? fmtGb(node.allocated_memory_mb) : "",
    total: node.total_memory_mb ? Math.round(node.total_memory_mb / 1024) + "G" : "",
  };
}

/**
 * The two versions to show when the Panel has outrun a node's agent, or undefined
 * when it hasn't. Mirrors the Panel's own agentNeedsUpdate (handlers_node.go):
 * artifact identity decides when both sides report a SHA, because a panel-only
 * release leaves the agent binary byte-identical and flagging the whole fleet for
 * it teaches operators to ignore the flag. The version strings are only ever the
 * DISPLAY; they are the test solely as a fallback for agents predating agent_sha.
 *
 * A node that has never been contacted has nothing to compare and is never flagged.
 */
export function agentDrift(node: Node): AgentDrift | undefined {
  if (!node.agent_version) return undefined;
  const shown: AgentDrift = {
    from: node.agent_version,
    to: fleet.panelVersion,
    direction: versionCompare(node.agent_version, fleet.panelVersion) > 0 ? "ahead" : "behind",
  };
  const want = fleet.panelAgentSha[`${node.os}/${node.arch}`];
  if (node.agent_sha && want)
    return node.agent_sha.toLowerCase() === want.toLowerCase() ? undefined : shown;
  return node.agent_version !== fleet.panelVersion ? shown : undefined;
}

export interface AgentDrift {
  from: string;
  to: string;
  /** "behind" — the usual case, the panel is newer. "ahead" — the agent is newer,
   *  which happens after a panel rollback and means the action DOWNGRADES the
   *  agent. The skew test is deliberately direction-blind ("bring the node to the
   *  panel's version"), so the direction exists only to keep the label honest:
   *  a chip that says "update" while it would downgrade is lying. */
  direction: "behind" | "ahead";
}

/**
 * Compares two release-please semver strings, tolerating a leading "v" and
 * non-numeric suffixes. Returns >0 when a is newer.
 *
 * Deliberately not a full semver implementation: these are our own tags, and the
 * only decision riding on it is which word the chip uses. Anything it cannot
 * parse compares as equal, which falls through to "behind" — the safe default,
 * since that is what every normal fleet is.
 */
function versionCompare(a: string, b: string): number {
  const parts = (v: string) =>
    v.replace(/^v/, "").split(".").map((n) => parseInt(n, 10));
  const [pa, pb] = [parts(a), parts(b)];
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const x = pa[i] ?? 0;
    const y = pb[i] ?? 0;
    if (Number.isNaN(x) || Number.isNaN(y)) return 0;
    if (x !== y) return x - y;
  }
  return 0;
}

/** One container the drift badge is talking about. `label` is the agent's own
 *  container name for an untracked one (what an operator types into `docker`)
 *  and the panel's server name for a missing one, where no container exists to
 *  be named. */
export interface DriftItem {
  server_id: string;
  label: string;
}

/**
 * Containers the panel has lost track of on a node, or undefined when the two
 * accounts agree. The comparison is deliberately between opposite directions:
 * the agent reports its own kraken.managed containers (adopted on reconcile),
 * and the tracked figure is what the panel placed here. Everywhere else the
 * panel reasons from its own rows outward, so this is the one question asked the
 * other way — and the only way an untracked container is seen.
 *
 * Reachable state, not a hypothetical: before #354 deleting a server while its
 * node was unreachable dropped the row and left the container running, and a
 * node that carried one then still carries it. A delete now leaves a pending
 * removal instead (pendingRemovalsNote), and an untracked container that
 * predates that is retired from the badge (retirable). A deficit is reported
 * too — containers stopped behind the panel's back is the same class of
 * divergence.
 *
 * An agent from 0.54.0 on names the containers (node.managed_containers), and
 * then the answer is computed from identities rather than from two totals:
 * untracked is a reported container whose server id matches no row on this node
 * in any state, missing is a `running` row with no container behind it. Matching
 * on any state is what keeps an install pass quiet — the one-shot install
 * container carries the same managed label and the same server id, so it belongs
 * to a row the panel knows about even while that row is `installing`, and an
 * `installing` row is never itself missing because it was never claimed running.
 * An older agent sends no list, and the count comparison stands in unchanged.
 *
 * Skipped for a node that has never been contacted (no reported count to trust)
 * and for one that is offline, where a stale count would invent a discrepancy.
 */
export function containerDrift(
  node: Node,
):
  | { running: number; delta: number; word: "untracked" | "missing"; items: DriftItem[] }
  | undefined {
  if (node.status === "offline" || !node.agent_version) return undefined;
  const reported = node.running_servers ?? 0;
  const rows = fleet.servers.filter((sv) => sv.node_id === node.id);
  const tracked = rows.filter((sv) => sv.state === "running");

  const named = node.managed_containers;
  if (named) {
    // A container whose server was deleted but whose removal the node still
    // owes is not untracked: the panel knows exactly what it is and is already
    // removing it (pendingRemovalsNote names it). Counting it as untracked
    // would offer a retire whose "data untouched" the next replay undoes.
    const known = new Set([
      ...rows.map((sv) => sv.id),
      ...(node.pending_removals ?? []).map((p) => p.server_id),
    ]);
    const onNode = new Set(named.map((c) => c.server_id));
    const untracked = named
      .filter((c) => !known.has(c.server_id))
      .map((c) => ({ server_id: c.server_id, label: c.container_name || c.server_id }));
    // Reported first: a container running outside the panel's books is holding
    // memory and ports the scheduler believes are free, which outranks a row
    // whose container has gone. Both are rare and both together rarer still.
    if (untracked.length > 0) {
      return { running: reported, delta: untracked.length, word: "untracked", items: untracked };
    }
    const missing = tracked
      .filter((sv) => !onNode.has(sv.id))
      .map((sv) => ({ server_id: sv.id, label: sv.name }));
    if (missing.length > 0) {
      return { running: reported, delta: missing.length, word: "missing", items: missing };
    }
    return undefined;
  }

  const delta = reported - tracked.length;
  if (delta === 0) return undefined;
  return {
    running: reported,
    delta: Math.abs(delta),
    word: delta > 0 ? "untracked" : "missing",
    items: [],
  };
}

/** How many untracked containers the badge names inline before it keeps only
 *  the count (the rest ride the title). */
export const DRIFT_INLINE_MAX = 3;

/**
 * A container name as the band prints it inline. A Panel-made name is
 * `kraken_` plus a 36-character server id, and printed whole it is the widest
 * thing in the id cell — wide enough to push the band's instruments into each
 * other, because that column sizes to its content. The first eight characters
 * of the id are what an operator matches against `docker ps` anyway; the full
 * name stays in the line's title and in every control's label.
 */
export function shortContainerLabel(label: string): string {
  const m = /^kraken_([0-9a-f]{8})-[0-9a-f-]{27}(_install)?$/i.exec(label);
  if (m) return `kraken_${m[1]}…${m[2] ?? ""}`;
  return label.length > 24 ? label.slice(0, 23) + "…" : label;
}

/** The title a name printed on the drift line carries: the whole name and
 *  the server id it belongs to (`kraken_f4030778-… (f4030778-…)`), so it can
 *  be matched to a row or to `docker ps` without guessing which half is which.
 *  An item with no id, or one whose label already is its id, says it once. */
export function containerTitle(item: DriftItem): string {
  return item.server_id && item.server_id !== item.label ? `${item.label} (${item.server_id})` : item.label;
}

/** What a retire chip says to assistive tech: which container, where, and
 *  what the act does — the chip itself reads only `retire`. */
export function retireChipLabel(item: DriftItem, nodeName: string): string {
  return (
    `Retire the untracked container ${shortContainerLabel(item.label)} on ${nodeName}` +
    " — it is stopped and removed, its data untouched"
  );
}

/**
 * The untracked containers the badge offers to retire, or [] when it offers
 * none. Only an untracked surplus has anything to retire — a missing container
 * is a row with nothing behind it — and only a named one: an agent that sends
 * only the count gives no server id to aim at. Permission is the caller's check.
 *
 * One entry per server: an orphan whose install container is still around
 * reports two containers under one server id, and one retire removes both. The
 * band keys its chips on this list, so a duplicate would not just repeat a chip
 * — Svelte refuses duplicate keys and the band would not render at all. A
 * container with no server id (a hand-made one carrying the managed label) is
 * keyed by its name for the dedupe, and is not offered: the endpoint addresses
 * a server id, and there is none to aim at.
 */
export function retirable(drift: ReturnType<typeof containerDrift>): DriftItem[] {
  if (!drift || drift.word !== "untracked") return [];
  const seen = new Set<string>();
  const out: DriftItem[] = [];
  for (const item of drift.items) {
    const key = item.server_id || item.label;
    if (seen.has(key)) continue;
    seen.add(key);
    if (item.server_id) out.push(item);
  }
  return out;
}

/**
 * The quiet line a node band carries while the node owes removals (DESIGN.md,
 * Removals Owed): servers retired or deleted in the panel whose removal has not
 * reached the node yet. The count is the reading — the plain value ink, no chip,
 * because the Panel is already retrying — and the title is the roll call, each
 * entry by the server's name (its id once the row is gone) with what goes and
 * how the last try went, because "2 pending" alone does not say whether the
 * node is simply away or refusing. There is no "retry in 40s": the Panel
 * retries whenever the node answers, and reports no schedule to print.
 */
export function pendingRemovalsNote(node: Node): { count: number; title: string } | undefined {
  const owed = node.pending_removals ?? [];
  if (owed.length === 0) return undefined;
  // A container the node still reports for an owed id is the removal not yet
  // done, not an orphan (containerDrift leaves it out of the untracked count);
  // it is said here instead, where the removal is.
  const running = new Set((node.managed_containers ?? []).map((c) => c.server_id));
  const lines = owed.map((p) => {
    // A retired row is still in the fleet and has a name; a deleted one is not.
    const name = fleet.servers.find((s) => s.id === p.server_id)?.name ?? p.server_id;
    const facts = [
      removalKind(p),
      // delete_backups is a permanent delete's (#360): its own archives go too.
      p.delete_backups ? "container, data and archives" : p.delete_data ? "container and data" : "container",
      p.attempts === 1 ? "1 attempt" : `${p.attempts} attempts`,
    ];
    if (running.has(p.server_id)) facts.push("container still running");
    if (p.last_error) facts.push(p.last_error);
    return `${name} (${facts.join(" · ")})`;
  });
  return {
    count: owed.length,
    title:
      "retired or deleted in the panel, not yet removed from this node — retried each time the node answers\n" +
      lines.join("\n"),
  };
}

/** What a pending removal is the tail of (#360), as the first fact on its
 *  line: a retire whose node did not answer (the row is still in the panel,
 *  retired), a permanent delete (its own archives go too), or a plain delete
 *  from before retiring existed. */
export function removalKind(p: PendingRemoval): "retired" | "deleted for good" | "deleted" {
  if (p.delete_backups) return "deleted for good";
  const row = fleet.servers.find((s) => s.id === p.server_id);
  if (row?.state === "retired") return "retired";
  return "deleted";
}

/** The dead-note under a stopped card — real facts only. */
export function deadNote(server: Server): string {
  if (server.state === "install_failed")
    return "install failed · " + (server.last_error || "see reinstall");
  if (server.state === "crashed") return "crashed · logs held until next start";
  if (server.state === "installing") return "installing — first start follows";
  if (server.state === "restoring") return "restoring a backup — start waits for it";
  if (server.state === "retiring") return "retiring — final backup, then its world leaves the node";
  return "stopped · world saved on shutdown";
}
