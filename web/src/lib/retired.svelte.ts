// The retired group at the foot of the fleet (#360; DESIGN.md, Retired Row).
//
// A retired server is one the operator took off its node on purpose: stopped,
// its world and config removed, its row, config, schedules (switched off) and
// backups kept. It is not a card — a card is a live thing with vitals — but a
// ledger row in the specs sheet's voice: name, game, where it was, when it
// went, what it kept. Two controls per row: revive (the revive sheet) and
// delete for good (the one irreversible act, behind the typed gate).
//
// What it kept is the one reading the fleet poll does not carry: the archives
// live on the node the server left, so they are read with
// GET /servers/{id}/backups — lazily, once per row, again after a revive or a
// delete, and again when that node comes back from offline; never per poll.

import { api, ApiError, errMsg } from "@/api/client";
import type { Backup, Node, Server } from "@/api/types";
import { hasPerm } from "./auth.svelte";
import { fleet, refreshFleet } from "./fleet.svelte";
import { fmtAgo, fmtDay, fmtSize } from "./fmt";
import { CD_PURGE_BODY, openConfirm, openSheet, ui } from "./state.svelte";

/** The retired servers, most recently retired first. The grid's complement:
 *  gridServers() is every server but these. */
export function retiredServers(servers: readonly Server[]): Server[] {
  return servers
    .filter((s) => s.state === "retired")
    .sort((a, b) => Date.parse(b.retired_at ?? "") - Date.parse(a.retired_at ?? "") || a.name.localeCompare(b.name));
}

/** The empty grid's line. A fleet whose every server is retired is not a
 *  fleet with "no servers yet": the rows are right below, and revive is the
 *  way back for any of them. */
export function rosterEmptyNote(servers: readonly Server[]): string {
  return servers.some((s) => s.state === "retired")
    ? "every server is retired — revive one below, or deploy a new one from a node band's New Server"
    : "no servers yet — deploy one from a node band's New Server";
}

/** What a row knows of its archives. */
export type KeepReading =
  | { kind: "loading" }
  | { kind: "ok"; count: number; bytes: number }
  /** Not read, and the words say why (no permission, node away or gone). */
  | { kind: "unread"; label: string; title?: string };

export const retired = $state({
  /** Per server id: the archives reading, and the node status it was taken
   *  under (so a node coming back online is read again, once). */
  keep: {} as Record<string, { reading: KeepReading; nodeStatus: string }>,
  /** Per server id: a delete for good in flight. */
  busy: {} as Record<string, boolean>,
  /** The last delete's answer (the Panel's note, or its refusal), spoken in
   *  the group's head until the next action. */
  note: "" as string,
  noteFailed: false,
});

/** The node a retired server left, if the fleet still has it. */
export function retiredNode(server: Server): Node | undefined {
  return fleet.nodes.find((n) => n.id === server.retired_from_node_id);
}

/** The slug line under the name: `runescape dragonwilds · was on behemoth`,
 *  or `… · its node is gone` when the node was deleted since. */
export function retiredSlug(server: Server, specName: string | undefined, node: Node | undefined): string {
  const game = (specName ?? server.spec_id).toLowerCase();
  return node ? `${game} · was on ${node.name}` : `${game} · its node is gone`;
}

/** `retired sep 21 · 3d ago`. */
export function retiredWhen(server: Server, now: number = Date.now()): string {
  const at = Date.parse(server.retired_at ?? "");
  if (!Number.isFinite(at)) return "retired";
  return `retired ${fmtDay(at)} · ${fmtAgo(at, now)}`;
}

/** The one coloured thing a row may carry: the retire could not take its
 *  final backup. retire_note is "; "-joined facts; only the final-backup one
 *  is said on the row (in Caution, the whole note in its title) — a queued
 *  removal is already said on the node band's removals line. */
export function retiredNote(server: Server): { word: string; title: string } | null {
  const note = server.retire_note ?? "";
  for (const part of note.split("; ")) {
    if (part.startsWith("final backup skipped")) return { word: "final backup skipped", title: note };
    if (part.startsWith("final backup failed")) return { word: "final backup failed", title: note };
  }
  return null;
}

/** The archives a reading counts: only ready ones — a failed attempt captured
 *  nothing, and a pending one is not an archive yet. */
export function keepOf(backups: readonly Backup[]): { count: number; bytes: number } {
  const ready = backups.filter((b) => b.state === "ready");
  return { count: ready.length, bytes: ready.reduce((n, b) => n + (b.size || 0), 0) };
}

/** `4 backups · 9.6G kept`, `no backups kept`, or why it could not be read. */
export function keepLabel(r: KeepReading | undefined): string {
  if (!r || r.kind === "loading") return "backups …";
  if (r.kind === "unread") return r.label;
  if (r.count === 0) return "no backups kept";
  return `${r.count} backup${r.count === 1 ? "" : "s"} · ${fmtSize(r.bytes)} kept`;
}

/** Whether a row's reading should be (re)taken now: never read, or read while
 *  its node was away and the node is back. */
export function keepStale(server: Server): boolean {
  const had = retired.keep[server.id];
  if (!had) return true;
  const status = retiredNode(server)?.status ?? "gone";
  return had.nodeStatus !== status && status !== "offline" && had.reading.kind === "unread";
}

/** Read a row's archives from the node it left, or say why not. */
export async function loadKeep(server: Server): Promise<void> {
  const node = retiredNode(server);
  const nodeStatus = node?.status ?? "gone";
  const set = (reading: KeepReading) => (retired.keep[server.id] = { reading, nodeStatus });
  if (!node) {
    set({ kind: "unread", label: "its archives went with its node" });
    return;
  }
  if (!hasPerm("backup.manage")) {
    set({ kind: "unread", label: `backups on ${node.name}` });
    return;
  }
  if (node.status === "offline") {
    set({ kind: "unread", label: `backups unreadable while ${node.name} is offline` });
    return;
  }
  set({ kind: "loading" });
  try {
    const r = await api.listBackups(server.id);
    set({ kind: "ok", ...keepOf(r.backups ?? []) });
  } catch (e) {
    const gone = e instanceof ApiError && e.status === 404;
    set({
      kind: "unread",
      label: gone ? "its archives went with its node" : `backups on ${node.name}`,
      title: errMsg(e),
    });
  }
}

/** Revive: the revive sheet, seeded from this row. */
export function openRevive(server: Server, e: MouseEvent & { currentTarget: HTMLElement }) {
  retired.note = "";
  ui.reviveServerId = server.id;
  openSheet("reviveForm", e.clientX, e.clientY, e.currentTarget);
}

/** Delete for good: the typed gate, then the permanent DELETE. */
export function openPurge(server: Server, returnTo: HTMLElement | null) {
  retired.note = "";
  openConfirm(server.name, returnTo, {
    noun: "server",
    verb: "delete",
    typed: true,
    body: CD_PURGE_BODY,
    go: () => purge(server.id),
  });
}

/** Deletes a retired server for good. The Panel's answer is spoken in the
 *  group's head until the next action: a note when archives on a shared target
 *  were kept or the node is owed the delete, its refusal when it said no. */
export async function purge(id: string): Promise<void> {
  if (retired.busy[id]) return;
  retired.busy[id] = true;
  retired.note = "";
  retired.noteFailed = false;
  try {
    const res = await api.deleteServer(id);
    retired.note = res?.note ?? "";
    delete retired.keep[id];
  } catch (e) {
    retired.note = errMsg(e) || "the panel refused without a reason — check the audit log";
    retired.noteFailed = true;
  } finally {
    delete retired.busy[id];
  }
  await refreshFleet().catch(() => {});
}
