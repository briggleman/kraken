// The revive sheet's decisions (#360; DESIGN.md, Revive Sheet), kept pure so
// they can be pinned without mounting the sheet.
//
// The sheet is the deploy sheet with everything the server already knows filled
// in: its old node first, its old ports where they are still free, its old
// memory, and one choice the deploy sheet never had — which backup to restore
// before the first start. What it posts is only what the operator changed: the
// Panel's defaults ARE the old node, the old memory and the old ports, so
// sending them back would pin a revive to a copy of the row this sheet read.

import type { Backup, Node, PlatformKind, ReviveInput, Server, Spec } from "@/api/types";
import { fmtSize, fmtWhen } from "./fmt";

/** Whether a node can run a platform — the scheduler's SupportedKinds rule. */
export function nodeFits(node: Node, kind: PlatformKind): boolean {
  if (kind === "windows-native") return node.os === "windows";
  if (kind === "linux-native") return node.os === "linux";
  return node.os === "linux" && node.wine_enabled; // linux-wine
}

/** The nodes the picker offers: those that can run the server's platform and
 *  take new placements (online — the scheduler skips partial, offline and
 *  locked ones), its old node first. The old node is offered whatever its
 *  status, so the sheet can say why it will not do rather than silently
 *  picking another; the pre-flight check then names the status. So is the
 *  node already chosen (`keepId`): one that goes offline while the sheet is
 *  open stays in the picker, named with its status, rather than vanishing
 *  into "no node can run its platform". */
export function reviveCandidates(server: Server, nodes: readonly Node[], keepId = ""): Node[] {
  const fit = nodes.filter((n) => nodeFits(n, server.kind));
  const old = fit.find((n) => n.id === server.retired_from_node_id);
  const rest = fit.filter((n) => n.id !== old?.id && (n.status === "online" || n.id === keepId));
  return old ? [old, ...rest] : rest;
}

/** A node's option text: its name, and its status when it is not taking work
 *  (a locked node says "locked" — the operator's word, never "cordoned"). */
export function nodeOptionLabel(node: Node): string {
  if (node.status === "online") return node.name;
  return `${node.name} — ${node.status === "cordoned" ? "locked" : node.status}`;
}

/** The ports a revive asks for again, in the spec's order when it has one. */
export function retiredPortList(server: Server): number[] {
  return Object.values(server.retired_ports ?? {}).filter((p) => p > 0);
}

/** Whether every old port is free on a node: inside its pool and not
 *  allocated — the pool's own IsFree. With none to reuse it is not a claim
 *  worth a badge, so it reads false. */
export function portsFree(ports: readonly number[], node: Node | undefined): boolean {
  if (!node || ports.length === 0) return false;
  const ranges = node.ports?.ranges ?? [];
  const taken = new Set(node.ports?.allocated ?? []);
  return ports.every((p) => !taken.has(p) && ranges.some((r) => p >= r.start && p <= r.end));
}

/** One option of the restore select. */
export interface BackupOption {
  id: string;
  label: string;
  bytes: number;
}

/** The archives a revive can restore — ready ones only, newest first, the
 *  first marked latest: `sep 21 03:00 · final-before-retire · 2.4G — latest`.
 *  (`none — a fresh world` is the sheet's own last option.) */
export function backupOptions(backups: readonly Backup[]): BackupOption[] {
  return backups
    .filter((b) => b.state === "ready")
    .sort((a, b) => b.created_ms - a.created_ms)
    .map((b, i) => ({
      id: b.id,
      bytes: b.size || 0,
      label: `${fmtWhen(b.created_ms)} · ${b.name}${b.size ? " · " + fmtSize(b.size) : ""}${i === 0 ? " — latest" : ""}`,
    }));
}

/** A size split for the cost strip's `2.4<em>G</em>` shape. */
export function sizeParts(bytes: number): { num: string; unit: string } {
  const s = fmtSize(bytes);
  return { num: s.slice(0, -1), unit: s.slice(-1) };
}

/** Why the sheet cannot revive this server at all, or "". Each is a refusal
 *  the Panel would answer with — said before the click instead of after it. */
export function reviveBlock(
  server: Server,
  spec: Spec | undefined,
  nodes: readonly Node[],
  chosen: Node | undefined,
): string {
  const owedOn = nodes.find((n) => (n.pending_removals ?? []).some((p) => p.server_id === server.id));
  if (owedOn)
    return `its old containers and world are still being removed from ${owedOn.name} — revive it once the node confirms (the removals line on its band)`;
  if (!spec) return "the game spec it was built from no longer exists, so it cannot be revived";
  if (!spec.platforms.some((p) => p.kind === server.kind))
    return `the game spec no longer offers the ${server.kind} platform it ran on, so it cannot be revived`;
  if (!chosen) return `no node can run its ${server.kind} platform`;
  if (chosen.status !== "online")
    return `${chosen.name} is ${chosen.status === "cordoned" ? "locked" : chosen.status} and takes no new servers — pick another node`;
  return "";
}

/** What the sheet chose, as the sheet holds it. */
export interface ReviveChoice {
  nodeId: string;
  memoryMb: number;
  restoreId: string; // "" = none — a fresh world
  start: boolean;
  steamGuard: string;
}

/**
 * The memory the sheet starts from, and where it came from (the help line
 * under the field). It is what the Panel would give the server with no
 * memory_mb in the body:
 *
 * - its old figure, when it stored one and the spec's minimum still allows it
 *   ("what it had before");
 * - the spec's allocation (recommended, else minimum; Resources.AllocMemoryMB)
 *   when it stored none ("the spec's allocation");
 * - the spec's allocation when the spec's minimum has been raised past its old
 *   figure since it was retired ("… its minimum was raised since it was
 *   retired" — said only then, #380). Seeding the old figure would open the
 *   sheet on a refusal the operator did not cause.
 */
export function reviveMemorySeed(server: Server, spec: Spec | undefined): { mb: number; source: string } {
  const min = spec?.resources.min_memory_mb ?? 0;
  const had = server.memory_mb > 0 ? server.memory_mb : 0;
  if (had > 0 && had >= min) return { mb: had, source: "what it had before" };
  const mb = spec?.resources.recommended_memory_mb || min || server.memory_mb;
  if (had === 0) return { mb, source: "the spec's allocation" };
  return { mb, source: "the spec's allocation — its minimum was raised since it was retired" };
}

/** The restore the sheet posts. Only an archive on the node it lands on can
 *  be restored (the archives stay where they were taken), so away from its
 *  old node it is always none. On it, the latest ready archive is the default
 *  until the operator picks — and their pick, "none" included, stands: a list
 *  arriving late, or a trip to another node and back, does not overwrite it. */
export function effectiveRestore(
  onOldNode: boolean,
  touched: boolean,
  pick: string,
  options: readonly BackupOption[],
): string {
  if (!onOldNode) return "";
  if (touched && (pick === "" || options.some((o) => o.id === pick))) return pick;
  return options[0]?.id ?? "";
}

/** The body the sheet posts: only what differs from the Panel's own defaults
 *  (the old node, the seeded memory, no restore, no start). */
export function reviveBody(server: Server, c: ReviveChoice, seedMb: number = server.memory_mb): ReviveInput {
  const body: ReviveInput = {};
  if (c.nodeId && c.nodeId !== server.retired_from_node_id) body.node_id = c.nodeId;
  if (c.memoryMb > 0 && c.memoryMb !== seedMb) body.memory_mb = c.memoryMb;
  if (c.restoreId) body.restore_backup_id = c.restoreId;
  if (c.start) body.start = true;
  if (c.steamGuard.trim()) body.steam_guard_code = c.steamGuard.trim();
  return body;
}
