// View models: fleet API objects mapped into the shapes the pane renders,
// with synthetic instrument tracks attached where the backend has no
// telemetry feed yet (node cpu/net/temp/disk and card cpu/mem — see the
// "node & fleet telemetry" backlog issue). Tracks are cached per entity so
// the walks keep their history across the 10s fleet polls.

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

const NODE_TRACK = 48;
const CARD_TRACK = 72;

export interface NodeInstruments {
  cpu: Track;
  /** REAL: allocated/total memory ratio, sampled each tick — flat is truth. */
  alloc: Track;
  net: { base: number; span: number; ref: number; rate: number };
  temp: { floor: number; div: number; deg: number };
  diskSeed: number;
  packets: string[];
}

const PACKET_SETS: string[][] = [
  [
    "--s:4; --d:3.1s; --dl:-0.5s; top:30%",
    "--s:6; --d:4.4s; --dl:-2.4s; top:42%",
    "--s:2.5; --d:2.2s; --dl:-1.4s; top:24%",
    "--s:3; --d:2.7s; --dl:-0.2s; top:36%",
    "rev|--s:3; --d:3.6s; --dl:-1.8s; top:62%",
    "rev|--s:2; --d:2.5s; --dl:-0.7s; top:72%",
    "rev|--s:4.5; --d:4.9s; --dl:-3s; top:68%",
  ],
  [
    "--s:3; --d:3.4s; --dl:-0.9s; top:28%",
    "--s:5; --d:4.8s; --dl:-2.1s; top:44%",
    "--s:2; --d:2.6s; --dl:-1.1s; top:34%",
    "rev|--s:2.5; --d:3.9s; --dl:-2.6s; top:64%",
    "rev|--s:3.5; --d:4.3s; --dl:-0.4s; top:70%",
  ],
];

const nodeInstruments = new Map<string, NodeInstruments>();
const cardTracks = new Map<string, { cpu: Track; mem: Track }>();

export function instrumentsFor(node: Node, index: number): NodeInstruments {
  let inst = nodeInstruments.get(node.id);
  if (!inst) {
    const ratio = node.total_memory_mb
      ? (node.allocated_memory_mb / node.total_memory_mb) * 100
      : 0;
    inst = {
      cpu: track({ v: 20 + Math.random() * 20, lo: 8, hi: 88, step: 8 }, NODE_TRACK),
      alloc: { walk: { v: ratio, lo: 0, hi: 100, step: 0 }, history: Array(NODE_TRACK).fill(ratio) },
      net: { base: 3 + Math.random() * 8, span: 5, ref: 10, rate: 8 },
      temp: { floor: 40 + Math.floor(Math.random() * 5), div: 9, deg: 44 },
      diskSeed: 30 + Math.floor(Math.random() * 12),
      packets: PACKET_SETS[index % PACKET_SETS.length],
    };
    inst.net.ref = inst.net.base + inst.net.span / 2;
    nodeInstruments.set(node.id, inst);
  }
  return inst;
}

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
  for (const inst of nodeInstruments.values()) out.push(inst.cpu);
  // card walks only advance for servers that are actually running
  for (const s of fleet.servers) {
    if (s.state !== "running") continue;
    const t = cardTracks.get(s.id);
    if (t) out.push(t.cpu, t.mem);
  }
  return out;
}

export function allNodeInstruments(): NodeInstruments[] {
  return [...nodeInstruments.values()];
}

/** Sample the real allocation ratio into each node's memory track. */
export function sampleAllocTracks() {
  for (const node of fleet.nodes) {
    const inst = nodeInstruments.get(node.id);
    if (!inst) continue;
    const ratio = node.total_memory_mb
      ? (node.allocated_memory_mb / node.total_memory_mb) * 100
      : 0;
    inst.alloc.history.push(ratio);
    inst.alloc.history.shift();
  }
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

export function nodeMemLabel(node: Node): { used: string; total: string } {
  return { used: fmtGb(node.allocated_memory_mb), total: Math.round(node.total_memory_mb / 1024) + "G" };
}

/** The dead-note under a stopped card — real facts only. */
export function deadNote(server: Server): string {
  if (server.state === "install_failed")
    return "install failed · " + (server.last_error || "see reinstall");
  if (server.state === "crashed") return "crashed · logs held until next start";
  if (server.state === "installing") return "installing — first start follows";
  return "stopped · world saved on shutdown";
}
