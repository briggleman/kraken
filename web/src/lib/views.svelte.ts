// View models: fleet API objects mapped into the shapes the pane renders.
//
// Node band instruments are real — see lib/telemetry.svelte.ts. What remains
// synthetic here is the SERVER CARD cpu/mem tracks: real per-server stats exist
// only on the drill-in WebSocket, one server at a time, so the fleet view has
// no feed to draw from yet (phase 2 of the node & fleet telemetry issue).
// Tracks are cached per server so the walks keep their history across polls.

import type { Node, Server, Spec } from "@/api/types";
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
export function agentDrift(node: Node): { from: string; to: string } | undefined {
  if (!node.agent_version) return undefined;
  const shown = { from: node.agent_version, to: fleet.panelVersion };
  const want = fleet.panelAgentSha[`${node.os}/${node.arch}`];
  if (node.agent_sha && want)
    return node.agent_sha.toLowerCase() === want.toLowerCase() ? undefined : shown;
  return node.agent_version !== fleet.panelVersion ? shown : undefined;
}

/** The dead-note under a stopped card — real facts only. */
export function deadNote(server: Server): string {
  if (server.state === "install_failed")
    return "install failed · " + (server.last_error || "see reinstall");
  if (server.state === "crashed") return "crashed · logs held until next start";
  if (server.state === "installing") return "installing — first start follows";
  return "stopped · world saved on shutdown";
}
