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
// GET /servers/{id}/backups — lazily, once per row, two at a time, again when
// that node comes back from offline or 30s after a read that failed for a
// passing reason; never per poll.

import { untrack } from "svelte";
import { api, ApiError, errMsg } from "@/api/client";
import type { Backup, Node, ReviveInput, Server } from "@/api/types";
import { hasPerm, onSessionChange } from "./auth.svelte";
import { fleet, fleetIssued, refreshFleet, serversReadIssued } from "./fleet.svelte";
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
  /** Not read, and the words say why (no permission, node away or gone).
   *  `retryAt` is set on a read that failed for a reason that may pass — the
   *  row is read again on the first sync after it. */
  | { kind: "unread"; label: string; title: string; retryAt?: number };

export const retired = $state({
  /** Per server id: the archives reading, and the node status it was taken
   *  under (so a node coming back online is read again, once). */
  keep: {} as Record<string, { reading: KeepReading; nodeStatus: string }>,
  /** Per server id: a delete for good in flight. */
  busy: {} as Record<string, boolean>,
  /** Per server id: a revive in flight, from the sheet's click until the fleet
   *  read after it has landed — until then the row still reads retired, and a
   *  second revive would only come back 409. Its archives are not read in that
   *  window either (#380): a poll landing between the 202 and that fleet read
   *  still shows the row retired, and its reading would be thrown away. */
  reviving: {} as Record<string, boolean>,
  /** The last delete's answer (the Panel's note, or its refusal), spoken in
   *  the group's head until the next action — or until a fleet poll started
   *  after it shows the retired set changed under it (another operator's
   *  retire, revive or delete), when what it answered is no longer the picture
   *  on screen. */
  note: "" as string,
  noteFailed: false,
  /** The retired set the note is judged against, as retiredKey() writes it:
   *  the set the first fleet read from a poll started after the note brought
   *  back (#380), or null until that read has landed. Taken from that read
   *  rather than worked out from the fleet on screen when the note is spoken,
   *  which can be a poll behind (a second delete, another operator's retire). */
  noteFor: null as string | null,
});

/** How long a failed (non-404) read waits before the row is read again. */
export const KEEP_RETRY_MS = 30_000;
/** How many rows' archives are read at once. Each read is an Agent round trip
 *  with a 20s timeout; a fleet with a dozen retired rows would otherwise fire
 *  a dozen at first paint. */
export const KEEP_IN_FLIGHT = 2;
/** How long the browser waits on one read before cancelling it, giving its
 *  slot back and treating it as failed (#380). The fetch itself has no
 *  timeout, and a read that never settles would otherwise hold its slot (and a
 *  connection) for the whole session. Above the Panel's own 20s Agent limit,
 *  so a slow answer that does come still lands, and a Panel that gives up
 *  first is heard in its own words. */
export const KEEP_READ_TIMEOUT_MS = 25_000;

// Bumped when the session changes: a read still out from the last operator's
// session must not land in the next one's.
let keepGen = 0;
const queue: string[] = [];
const queued = new Set<string>();
let inFlight = 0;

// The head note's generation (#380): the issue number of the first fleet poll
// started after the answer it speaks arrived. A poll started before that —
// one already out when the delete committed — can still carry the deleted
// row, and must not be read as the set changing under the note.
let noteGen = 0;

// Delete-for-good answers in the order the deletes were sent. Two deletes can
// be out at once, and an older one answering last must not undo what a newer
// one said: `noteSeq` is the delete whose answer is the note on screen.
let purgeSeq = 0;
let noteSeq = 0;

/** Everything the group holds for a session. Run on login and logout (and by
 *  the tests between cases). */
export function resetRetired() {
  keepGen++;
  retired.keep = {};
  retired.busy = {};
  retired.reviving = {};
  retired.note = "";
  retired.noteFailed = false;
  retired.noteFor = null;
  noteGen = 0;
  noteSeq = 0;
  // The slots go with the session: a read still out from the last one gives
  // nothing back when it settles (pump's done() checks the generation), so
  // without this its slot would stay taken in the next.
  queue.length = 0;
  queued.clear();
  inFlight = 0;
}
onSessionChange(resetRetired);

/** The retired set as one comparable string. */
export function retiredKey(servers: readonly Server[]): string {
  return servers
    .filter((s) => s.state === "retired")
    .map((s) => s.id)
    .sort()
    .join(",");
}

function speak(note: string, failed: boolean) {
  if (note) noteGen = fleetIssued() + 1;
  retired.note = note;
  retired.noteFailed = failed;
  retired.noteFor = null;
}

/** Clear the head's note: the next action is the operator's own. */
export function clearRetiredNote() {
  speak("", false);
}

/**
 * Fold a fleet read into the group. `issuedAt` is the issue number of the poll
 * the read came from (serversReadIssued()).
 *
 * The head's note is judged only on reads from polls started after it: one
 * started before can still hold the row the delete took away. The first such
 * read sets the retired set the note stands for; the note goes when a later
 * one's set differs from it (another operator's retire, revive or delete),
 * when what it answered is no longer the picture on screen.
 *
 * Readings of rows that are no longer retired go (a revive in another tab must
 * not leave its old "4 backups kept" waiting for the next retire), and the
 * rows whose reading is missing or stale are queued to be read.
 */
export function syncRetired(servers: readonly Server[], issuedAt: number, now: number = Date.now()) {
  if (retired.note && issuedAt >= noteGen) {
    const key = retiredKey(servers);
    if (retired.noteFor === null) retired.noteFor = key;
    else if (key !== retired.noteFor) clearRetiredNote();
  }
  const live = new Set(servers.filter((s) => s.state === "retired").map((s) => s.id));
  for (const id of Object.keys(retired.keep)) if (!live.has(id)) delete retired.keep[id];
  for (const s of retiredServers(servers)) if (keepStale(s, now)) queueKeep(s);
}

/** The retired list's fleet-sync effect (RetiredList mounts exactly this):
 *  every fleet read is folded in, with the issue number of the poll it came
 *  from. Tracks the servers and the nodes (a node coming back makes its rows
 *  stale); the sync itself is untracked, so a reading landing does not re-run
 *  it. Named here so a test can mount the effect the list does. */
export function followRetired() {
  const servers = fleet.servers;
  void fleet.nodes;
  untrack(() => syncRetired(servers, serversReadIssued()));
}

/** Queue a row's read; at most KEEP_IN_FLIGHT run at once. */
export function queueKeep(server: Server) {
  if (queued.has(server.id) || retired.reviving[server.id]) return;
  queued.add(server.id);
  retired.keep[server.id] = { reading: { kind: "loading" }, nodeStatus: retiredNode(server)?.status ?? "gone" };
  queue.push(server.id);
  pump();
}

function pump() {
  while (inFlight < KEEP_IN_FLIGHT && queue.length > 0) {
    const id = queue.shift()!;
    const gen = keepGen;
    inFlight++;
    const row = fleet.servers.find((s) => s.id === id);
    const done = () => {
      // A read from a session since reset has no slot to give back.
      if (gen !== keepGen) return;
      inFlight--;
      queued.delete(id);
      pump();
    };
    if (!row || row.state !== "retired") {
      done();
      continue;
    }
    if (retired.reviving[id]) {
      // Queued before its revive was sent: the reading would be thrown away.
      // Dropped rather than left "loading", so a revive that is refused leaves
      // the row to be read on the next sync.
      delete retired.keep[id];
      done();
      continue;
    }
    void loadKeep(row).finally(done);
  }
}

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
 *  removal is already said on the node band's removals line. Only "skipped"
 *  reaches a retired row: a final backup that FAILED abandons the retire
 *  (retire.go), so the server is never retired with that note. */
export function retiredNote(server: Server): { word: string; title: string } | null {
  const note = server.retire_note ?? "";
  for (const part of note.split("; ")) {
    if (part.startsWith("final backup skipped: ")) return { word: "final backup skipped", title: note };
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

/** Whether a row's reading should be (re)taken now: never read; read while
 *  its node was away and the node is back; or a read that failed for a
 *  passing reason whose wait is over. A row being read is never stale. */
export function keepStale(server: Server, now: number = Date.now()): boolean {
  if (queued.has(server.id)) return false;
  const had = retired.keep[server.id];
  if (!had) return true;
  if (had.reading.kind !== "unread") return false;
  if (had.reading.retryAt !== undefined && now >= had.reading.retryAt) return true;
  const status = retiredNode(server)?.status ?? "gone";
  return had.nodeStatus !== status && status !== "offline";
}

const GONE = "its node is gone, and its archives went with it";

/** Read a row's archives from the node it left, or say why not. The words
 *  for a row that cannot be read say where the archives are and why they are
 *  not counted, and ride the title too, since the reading is clipped. */
export async function loadKeep(server: Server, now: () => number = Date.now): Promise<void> {
  const node = retiredNode(server);
  const nodeStatus = node?.status ?? "gone";
  const gen = keepGen;
  const set = (reading: KeepReading) => {
    // A reading lands only on a row that is still retired, in the session
    // that asked for it: a revive or a logout while the read was out makes
    // it an answer about something no longer on screen.
    if (gen !== keepGen) return;
    if (fleet.servers.find((s) => s.id === server.id)?.state !== "retired") return;
    retired.keep[server.id] = { reading, nodeStatus };
  };
  const unread = (label: string, title = label, retryAt?: number) => set({ kind: "unread", label, title, retryAt });
  if (!node) return unread(GONE);
  const where = `backups on ${node.name}`;
  if (!hasPerm("backup.manage")) return unread(`${where} · no permission to read them`);
  if (node.status === "offline") return unread(`${where} · unreadable while ${node.name} is offline`);
  set({ kind: "loading" });
  // Given up on after KEEP_READ_TIMEOUT_MS, and cancelled then too: freeing
  // the slot alone would leave the request holding a connection, and a stuck
  // node would soon hold all of the browser's to the Panel.
  const abort = new AbortController();
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    const r = await Promise.race([
      api.listBackups(server.id, abort.signal),
      new Promise<never>((_, reject) => {
        timer = setTimeout(() => {
          reject(new Error(`the read of its backups timed out after ${KEEP_READ_TIMEOUT_MS / 1000}s`));
          abort.abort();
        }, KEEP_READ_TIMEOUT_MS);
      }),
    ]);
    set({ kind: "ok", ...keepOf(r.backups ?? []) });
  } catch (e) {
    // 404 is the Panel saying the node went (with the archives); anything
    // else — a timeout, an agent error — may pass, so it is read again later.
    if (e instanceof ApiError && e.status === 404) return unread(`${where} · ${GONE}`, errMsg(e));
    unread(`${where} · could not be read`, errMsg(e), now() + KEEP_RETRY_MS);
  } finally {
    clearTimeout(timer);
  }
}

/** Revive: the revive sheet, seeded from this row. */
export function openRevive(server: Server, e: MouseEvent & { currentTarget: HTMLElement }) {
  clearRetiredNote();
  ui.reviveServerId = server.id;
  openSheet("reviveForm", e.clientX, e.clientY, e.currentTarget);
}

/**
 * Revives a retired server with the sheet's body. The row is held (its revive
 * button disabled) from the click until the fleet read after the answer has
 * landed, because until then it still reads retired and a second revive would
 * come back 409 server_not_retired. Returns the refusal's words, or "".
 */
export async function reviveRetired(id: string, body: ReviveInput): Promise<string> {
  if (retired.reviving[id]) return "";
  retired.reviving[id] = true;
  clearRetiredNote();
  try {
    await api.reviveServer(id, body);
    delete retired.keep[id];
    await refreshFleet().catch(() => {});
    return "";
  } catch (e) {
    return errMsg(e) || "the panel refused without a reason — check the audit log";
  } finally {
    delete retired.reviving[id];
  }
}

/** Delete for good: the typed gate, then the permanent DELETE. */
export function openPurge(server: Server, returnTo: HTMLElement | null) {
  clearRetiredNote();
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
  clearRetiredNote();
  // An answer that arrives after a logout (or a logout and a new login) is the
  // last session's: it is not spoken in this one, and it touches none of this
  // one's state (a new delete of the same row may be out by then).
  const gen = keepGen;
  const seq = ++purgeSeq;
  // An answer from an older delete than the one whose answer is on screen is
  // not spoken; only an answer with words takes the head.
  const answer = (note: string, failed: boolean) => {
    if (gen !== keepGen || seq < noteSeq) return;
    if (note) noteSeq = seq;
    speak(note, failed);
  };
  try {
    const res = await api.deleteServer(id);
    // The set the note stands for is taken from the first fleet read started
    // after this answer (syncRetired), so the read that takes the row away
    // does not also take the answer away.
    answer(res?.note ?? "", false);
    if (gen === keepGen) delete retired.keep[id];
  } catch (e) {
    answer(errMsg(e) || "the panel refused without a reason — check the audit log", true);
  } finally {
    if (gen === keepGen) delete retired.busy[id];
  }
  await refreshFleet().catch(() => {});
}
