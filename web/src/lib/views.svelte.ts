// View models: fleet API objects mapped into the shapes the pane renders.
//
// Node band instruments are real — see lib/telemetry.svelte.ts. What remains
// synthetic here is the SERVER CARD cpu/mem tracks: real per-server stats exist
// only on the drill-in WebSocket, one server at a time, so the fleet view has
// no feed to draw from yet (phase 2 of the node & fleet telemetry issue).
// Tracks are cached per server so the walks keep their history across polls.

import type { Server, Spec } from "@/api/types";
import { fleet, specOf, nodeOf } from "./fleet.svelte";
import { seedHistory, type WalkSpec } from "./walk";

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

/** The dead-note under a stopped card — real facts only. */
export function deadNote(server: Server): string {
  if (server.state === "install_failed")
    return "install failed · " + (server.last_error || "see reinstall");
  if (server.state === "crashed") return "crashed · logs held until next start";
  if (server.state === "installing") return "installing — first start follows";
  return "stopped · world saved on shutdown";
}
