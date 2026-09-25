// Drill-in data: one server's live detail — stream, settings, files, backups,
// schedules, DNS — fetched on open and kept fresh while the overlay is up.

import { api, errMsg } from "@/api/client";
import type {
  Backup,
  FileEntry,
  FileListing,
  InstallAttempt,
  InstallLog,
  Node,
  PowerActionName,
  RestoreProgress,
  ScheduledTask,
  Server,
  ServerDnsState,
  ServerSettings,
  SftpStatus,
} from "@/api/types";
import type { ScheduleInput } from "@/api/client";
import { untrack } from "svelte";
import { ServerStream, type StreamMode } from "./stream.svelte";
import { fleet, refreshFleet } from "./fleet.svelte";
import { fmtWhen } from "./fmt";

export interface Origin {
  ox: string;
  oy: string;
}

export interface RestoreWatch {
  serverId: string;
  backupId: string;
  /** When the watched restore began, by the SERVER's clock (its
   *  restore.started_at). An outcome only counts if it finished after this —
   *  which is what keeps an earlier restore's result, or a fleet read issued
   *  before the POST and landing after the 202, from reading as this one's end.
   *  Server time on both sides, so a skewed client clock cannot matter. */
  since: string;
}

export interface RestoreNote {
  kind: "done" | "failed";
  /** The archive, by the name the ledger shows it under. */
  name: string;
  /** The archive's created_ms, so the note names it the way its row did
   *  (`aug 21 03:00 · nightly`); 0 when the archive is no longer listed. */
  when: number;
  /** The agent's reason, for a failed restore. */
  reason: string;
}

export const depth = $state({
  open: false,
  origin: { ox: "50%", oy: "50%" } as Origin,
  serverId: null as string | null,
  server: null as Server | null,
  backups: [] as Backup[],
  // The node's off-node mirror destination for display (e.g. "sftp nas.local"),
  // or "" when replication is off. Node config, not per-archive.
  backupMirror: "",
  schedules: [] as ScheduledTask[],
  dns: null as ServerDnsState | null,
  settings: null as ServerSettings | null,
  files: null as FileListing | null,
  filesDir: ".",
  // The logical path of the entry whose download token is being minted, so its
  // pill can say so; null otherwise. The transfer itself belongs to the
  // browser, so this only ever spans the mint — see filesDownload.
  downloading: null as string | null,
  // The retained output of this server's last install, and whether the console
  // pane is showing it instead of the container's log. An install that "worked"
  // is the case this exists for: the console has nothing to tail for a server
  // that never started, and the install is the only account of why (#280).
  installLog: null as InstallLog | null,
  installLogOpen: false,
  // Which *instance* of that retained log is in hand. The console keys its
  // scroll position on the identity of the document it is showing, and a
  // snapshot has no identity of its own to offer: its lines are keyed by index,
  // so a re-read of the same install is indistinguishable from the one it
  // replaced (#320). Bumped by setInstallLog, which is the only writer — and
  // never reset, including on open: the console's key carries the server id
  // beside it, so the count only has to keep moving, not to start anywhere.
  installLogSeq: 0,
  // Whether this server's current `installing` is the pre-start update pass
  // (#307) rather than a first install. See syncUpdatePass — the state is the
  // fact, this is only a finer name for one of its causes.
  updatePass: false,
  creatingBackup: false,
  // The archive whose restore REQUEST is in flight — only the POST, which now
  // answers at once (#361). The restore itself is the server's `restoring`
  // state and its `restore` block, which outlive this by minutes.
  restoringBackup: null as string | null,
  // The restore this drill-in is waiting on the end of, so the ledger can say
  // how it ended: set when a restore starts here or the server is found
  // mid-restore, cleared when the outcome note is made.
  restoreWatch: null as RestoreWatch | null,
  // How the watched restore ended — the one piece of feedback a restore that
  // landed ever gave, since the button used to just go back to "restore".
  restoreNote: null as RestoreNote | null,
  // The retire block's one choice (#360): take a final backup before the world
  // goes. On by default — revive needs something to restore — and back on
  // every time the drill-in opens, so one server's "no" is never another's.
  retireFinalBackup: true,
  powerBusy: false,
  error: null as string | null,
  sftp: null as SftpStatus | null,
  sftpOpen: false,
  sftpBusy: false,
  sftpError: null as string | null,
  // The plaintext password, held only for as long as the dialog is on screen.
  // The Panel stores a hash and returns this exactly once, so it is deliberately
  // not part of `sftp` — nothing that survives a close may carry it.
  sftpPassword: null as string | null,
});

export const stream = new ServerStream();

let lastFocus: HTMLElement | null = null;
let backupPoll: ReturnType<typeof setInterval> | undefined;

// --- the server-state generation (#368) ---------------------------------------
// `depth.server` has several writers, and they do not answer in the order they
// were asked. The detail refresh is the slow one: its getServer read is taken
// when the refresh begins, but it is only applied once all eight reads settle —
// and one of them, the file listing, reaches the node. A power action taken in
// between refreshes the fleet and puts the newer state on screen first; the
// refresh then landed and put the older one back (the chip read `running`, then
// dropped to `offline` until the next poll, and the stream was re-targeted to
// the stale mode with it).
//
// So every write of `depth.server` goes through setDepthServer, which bumps this
// count, and a refresh applies its server read only if the count has not moved
// since the reads went out. Anything that wrote in between is newer by
// construction. Module state rather than `depth` state: nothing renders it.
//
// A write that shows nothing new does not bump it. The fleet poll re-delivers
// the row on every tick whether or not it changed, and a count moved by a row
// identical to the one on screen would throw away the refresh's read in favour
// of no newer fact at all.
let serverGen = 0;

/** Whether two reads of a server agree on everything the drill-in derives from
 *  the row: its state (chip, controls, stream mode, `updating`) and its restore
 *  (the ledger's meter and outcome note). */
function sameServerView(a: Server | null, b: Server | null): boolean {
  if (a === b) return true;
  if (!a || !b) return false;
  return (
    a.id === b.id &&
    a.state === b.state &&
    // A retire's phase and outcome (#360): the retiring notice names the phase,
    // and an abandoned retire speaks through retire_note.
    JSON.stringify(a.retire ?? null) === JSON.stringify(b.retire ?? null) &&
    (a.retire_note ?? "") === (b.retire_note ?? "") &&
    (a.last_error ?? "") === (b.last_error ?? "") &&
    JSON.stringify(a.restore ?? null) === JSON.stringify(b.restore ?? null) &&
    JSON.stringify(a.restore_result ?? null) === JSON.stringify(b.restore_result ?? null)
  );
}

/** Put a server read on screen. The one writer of `depth.server`, so no write
 *  can skip the generation; each caller still re-derives the stream mode and
 *  the `updating` latch from the state it wrote. */
function setDepthServer(s: Server | null) {
  if (!sameServerView(depth.server, s)) serverGen++;
  depth.server = s;
}

export function streamModeFor(state: Server["state"] | undefined): StreamMode {
  if (!state) return "off";
  if (state === "starting" || state === "running" || state === "stopping") return "live";
  // offline / crashed: the container is stopped but Docker still holds its
  // logs until the next start, so the stream replays the tail and ends.
  if (state === "offline" || state === "crashed") return "replay";
  // installing: no container exists yet, but the Panel buffers the installer's
  // output and serves it over this same socket. Live, because the socket must
  // survive an agent blip during what can be a 20-minute download.
  if (state === "installing") return "live";
  // install_failed: the buffered attempt is replayed and the stream ends —
  // there is nothing further coming until a reinstall.
  return "replay";
}

// --- the `updating` label ---------------------------------------------------
// `installing` covers two different events: the first install of a new server,
// and the update pass that re-runs the install script before a start (#307).
// The state alone cannot tell them apart, and "installing" over a server that
// has been running for a month reads as a wipe — so the drill-in watches for
// the pass's own opening line and says "updating" instead.

/** The line the Panel writes to the install buffer when a start or restart
 *  re-runs the install script (handlers_server.go, updateThenStart). */
export const UPDATE_PASS_LINE = "[panel] updating ";

/** Fold a server state and the console's lines into the `updating` latch.
 *
 *  **The store state is authoritative.** A server that is not `installing` is
 *  not mid-pass, whatever the console holds, so every state that is not
 *  `installing` clears the latch — and the log line may only *set* it while the
 *  state still says `installing`.
 *
 *  That asymmetry is the fix for #311. The pass's opening line does not leave
 *  the console when the pass ends: `installing`, `starting` and `running` all
 *  map to a "live" stream, so `stream.set` never re-targets the socket and
 *  never clears the ring — the line sits there beside the new container's
 *  output. A latch that only ever *set* off that line therefore survived into
 *  `running`, and the header read "updating" over a server that was up. (The
 *  failure path did not show it: `install_failed` is a "replay" stream, so the
 *  re-target wiped the ring and took the line with it.)
 *
 *  It still latches rather than deriving straight off the line, because the
 *  ring evicts from the front and a long pass scrolls its own opening line out
 *  of the buffer. */
export function syncUpdatePass(
  state: Server["state"] | undefined,
  lines: readonly { text: string }[],
) {
  if (state !== "installing") {
    depth.updatePass = false;
    return;
  }
  if (lines.some((l) => l.text.startsWith(UPDATE_PASS_LINE))) depth.updatePass = true;
}

/** The drill-in's state word. `updating` is only a finer name for `installing`
 *  — it can never stand in for any other state (#311). */
export function stateLabel(state: Server["state"] | undefined, updating: boolean): string {
  if (!state) return "";
  if (state === "installing" && updating) return "updating";
  return state.replace("_", " ");
}

/** Which pair of power controls a state offers: "stop" is stop + restart, and
 *  "start" is start (+ update, where the state allows it). Keyed off the store
 *  state and nothing else — never the `updating` label. */
export function powerControls(state: Server["state"] | undefined): "stop" | "start" | "none" {
  // A retired server is on no node, and a retiring one belongs to its job:
  // there is nothing to power (#360).
  if (state === "retired" || state === "retiring") return "none";
  return state === "running" || state === "starting" || state === "stopping" ? "stop" : "start";
}

// --- the console pane's two buffers -----------------------------------------
// The console renders either the container's live log or the retained install
// log, and the INSTALL LOG chip swaps between them. Both are rings capped at
// 500 lines, which is what made the swap look broken (#314): the autoscroll
// effect was keyed on the rendered line *count* alone, so toggling between two
// full buffers changed nothing it could see and the viewport stayed exactly
// where the old buffer had been left. The log had swapped; the pane had not
// moved. Keying on a count is doubly wrong once a ring is full: the live buffer
// pushes and splices back to its cap on every append, so past 500 lines the
// count never changes again and a pinned console would stop following the tail
// altogether. These helpers carry that reasoning as logic rather than as an
// effect's dependency list.

export interface ConsoleView {
  /** What makes this a *different document*: which server, which buffer, and
   *  which instance of that buffer. See consoleViewKey — the two buffers do not
   *  answer that last question the same way. */
  key: string;
  /** What makes it a different *rendering of the same document*: the newest
   *  line's own id, plus anything else sharing the scroll box with it (the
   *  reconnect and install-log banners are inside it and change its height). */
  content: string;
}

/** Which document the console is showing, as a comparable string.
 *
 *  The two buffers answer "which instance of this?" differently, and keying
 *  both on the live stream was wrong in both directions (#320).
 *
 *  The live console *is* the stream, so a re-target or a reconnect is a new
 *  document: the lines are dropped and replayed from the start, and line seqs
 *  cannot say so because they are handed out at receipt and never reset.
 *
 *  The retained install log is a static snapshot fetched over REST, and the
 *  stream's generation says nothing about it — the socket stays targeted at the
 *  server whatever the chip is showing, so a background reconnect would re-key
 *  a finished install the operator is reading back through and yank it to the
 *  tail. Its instance is the fetch that produced it, which is also the only
 *  thing that can distinguish a re-read from the snapshot it replaced: those
 *  lines are keyed by index, so two reads of the same install render
 *  identically down to the last line's id. */
export function consoleViewKey(v: {
  serverId: string | null;
  showingInstall: boolean;
  installLogSeq: number;
  generation: number;
}): string {
  const buffer = v.showingInstall ? ["install", v.installLogSeq] : ["live", v.generation];
  return [v.serverId ?? "", ...buffer].join("|");
}

/** What the console viewport owes the next render.
 *
 *  - `force` — a different document is on screen. It opens at its tail whatever
 *    the operator had scrolled the previous one to; no offset on the old log
 *    means anything on the new one.
 *  - `restore` — the same document, coming back from a tab round-trip. The pane
 *    was display:none'd, which drops the offset, so the viewport is put back
 *    where the operator left it rather than re-decided from scratch.
 *  - `if-pinned` — the same document, rendered differently. Honour the pin:
 *    someone reading back through a noisy install must not be dragged to the
 *    bottom by the next line.
 *  - `no` — nothing changed; do not touch scrollTop, and do not pay for the
 *    forced layout that reading scrollHeight costs (#279).
 *
 *  `returning` says the console tab has just come back up. It never overrides a
 *  changed document — a log that was swapped out while the operator was in
 *  settings still opens at its tail — and it is what keeps a deliberate scroll
 *  back from being discarded by the round trip (#320). */
export function consoleRepin(
  prev: ConsoleView | null,
  next: ConsoleView,
  returning = false,
): "no" | "if-pinned" | "force" | "restore" {
  if (!prev || prev.key !== next.key) return "force";
  if (returning) return "restore";
  return next.content === prev.content ? "no" : "if-pinned";
}

/** Where a console coming back from a hidden tab should sit. The pane was
 *  display:none'd, which drops the offset, so something has to be written: the
 *  tail for a console that was following it, and otherwise the offset the
 *  operator left — a raw pixel offset, which a ring that rolled during the
 *  absence will have refilled with other lines. */
export function restoreTop(v: { pinned: boolean; lastTop: number; scrollHeight: number }): number {
  return v.pinned ? v.scrollHeight : v.lastTop;
}

/** What the console pane says when it has no lines at all. Four different
 *  facts, and the difference matters: a dark server has nothing to tail, an
 *  offline server with no container at all is installed and waiting for its
 *  first start (the state a reinstall or update pass leaves, which an empty
 *  `docker ps -a` must not pass off as a deletion), an install whose output
 *  never arrived has it on the chip above, and a Panel that restarted since
 *  the attempt genuinely kept none of it.
 *
 *  "No container" is said only from real Agent data (#385): the server's node
 *  must have reported every managed container (containers_reported) and listed
 *  none, in any state, for this server's id. A stopped container, a node whose
 *  agent is too old to report stopped ones, or a node that is offline (its list
 *  is from its last contact) all leave the plain dark note. */
export function emptyConsoleNote(opts: {
  installing: boolean;
  hasRetained: boolean;
  server?: Pick<Server, "id" | "state">;
  node?: Node;
}): string {
  if (!opts.installing) {
    const { server, node } = opts;
    if (
      server?.state === "offline" &&
      node?.containers_reported === true &&
      node.status !== "offline" &&
      !(node.managed_containers ?? []).some((c) => c.server_id === server.id)
    ) {
      return "installed · no container until start";
    }
    return "no output — server is dark";
  }
  return opts.hasRetained
    ? "nothing came over the console — the retained install log is on the chip above"
    : "no install output kept — the panel restarted since this attempt";
}

/** The one-line header the install console carries above the current output
 *  when the attempt before it is kept (#381):
 *  `previous attempt · started 08:02 · failed 08:06`. The verdict is read from
 *  the lines, because the Panel writes every failure — install_failed, a pass
 *  refused before it touched the tree, a start after an update that did not
 *  come up — on the `error` stream, and nothing else writes there. An attempt
 *  with no finish time was superseded before it reached a verdict. */
export function previousAttemptHeader(prev: InstallAttempt): string {
  const at = (ms: number | undefined) => (ms ? fmtWhen(ms).replace("today ", "") : "—");
  const failed = prev.lines.some((l) => l.stream === "error");
  const end = prev.finished_ms
    ? `${failed ? "failed" : "finished"} ${at(prev.finished_ms)}`
    : "did not finish";
  return `previous attempt · started ${at(prev.started_ms)} · ${end}`;
}

/** Whether the INSTALL LOG chip has anything to offer.
 *
 *  It needs a retained log to show, and it must not be offered while the
 *  console pane is *already* that log: in `installing` and `install_failed` the
 *  Panel's stream gate serves the very in-memory install buffer this chip
 *  snapshots (serveInstallStream tails it, the REST read snapshots it, one
 *  entry either way), so the chip would swap a live tail for an older copy of
 *  itself — which is the "the toggle does nothing" complaint again.
 *
 *  So the gate is on the console actually carrying that tail, not on the state.
 *  While an install is *running* that is always true — the socket is live or
 *  reconnecting throughout, and a stream that has not delivered yet is still
 *  working on it — so the chip stays away for the whole pass and no amount of
 *  socket trouble brings it back mid-install. It is after a *failed* install
 *  that the two can genuinely differ: that stream is a replay which ends, and a
 *  socket that never delivered (blocked, proxied away) leaves the pane empty
 *  while the REST read still holds the lines. There the chip appears, and it is
 *  the only way to them. */
export function canShowInstallLog(opts: {
  installing: boolean;
  hasRetained: boolean;
  consoleCarriesInstall: boolean;
}): boolean {
  if (!opts.hasRetained) return false;
  return !(opts.installing && opts.consoleCarriesInstall);
}

export function openDepth(id: string, x: number, y: number, returnTo?: HTMLElement | null) {
  lastFocus = returnTo ?? null;
  depth.origin = {
    ox: (x / innerWidth) * 100 + "%",
    oy: (y / innerHeight) * 100 + "%",
  };
  depth.serverId = id;
  // Through the generation too: a refresh still in flight from an earlier open
  // of this same server must not land over this one's.
  setDepthServer(fleet.servers.find((s) => s.id === id) ?? null);
  depth.backups = [];
  depth.backupMirror = "";
  depth.schedules = [];
  depth.dns = null;
  depth.settings = null;
  depth.files = null;
  depth.filesDir = ".";
  // A read left over from an earlier open of this same server (its refresh
  // waits on the file listing) holds a log from before this open; it must not
  // land over this one's.
  setInstallLog(null);
  retireInstallLogReads();
  depth.installLogOpen = false;
  // A fresh server, and a fresh socket: nothing is latched until this server's
  // own state and console say so.
  depth.updatePass = false;
  depth.restoreWatch = null;
  depth.restoreNote = null;
  depth.retireFinalBackup = true;
  depth.error = null;
  depth.sftp = null;
  depth.sftpOpen = false;
  depth.sftpBusy = false;
  depth.sftpError = null;
  depth.sftpPassword = null;
  depth.open = true;
  stream.set(id, streamModeFor(depth.server?.state));
  syncUpdatePass(depth.server?.state, stream.lines);
  void refreshDetail();
  pushRoute(id);
}

export function surface() {
  depth.open = false;
  depth.updatePass = false;
  stream.set("", "off");
  stopBackupPoll();
  if (lastFocus && document.contains(lastFocus)) lastFocus.focus();
  pushRoute(null);
}

// --- deep links -----------------------------------------------------------
// The panel has no router by design (sheets navigate), but a server's drill-in
// is a real destination people bookmark and paste — the old React panel had
// /servers/{id} routes, and its stale bookmarks were landing on the fleet grid
// with no explanation (#223). The URL contract stays deliberately small:
// /servers/{id} is the ONLY routable path; every sheet remains unrouted.

const serverPathRE = /^\/servers\/([0-9a-fA-F-]+)\/?$/;

// pushRoute reflects an open/close into the address bar. It no-ops when the
// bar already matches, which is what keeps a popstate-driven open/close from
// pushing a duplicate entry back onto the history it was driven by.
function pushRoute(id: string | null) {
  const want = id ? `/servers/${id}` : "/";
  if (location.pathname !== want) history.pushState(null, "", want);
}

// applyRoute opens or closes the drill-in to match the address bar. An id the
// fleet doesn't know (a stale bookmark, a deleted server) is rewritten to "/"
// rather than left in the bar to fail again on the next reload.
function applyRoute() {
  const m = serverPathRE.exec(location.pathname);
  if (!m) {
    if (depth.open) surface();
    return;
  }
  const sv = fleet.servers.find((s) => s.id === m[1]);
  // A retired server has no drill-in (#360): no node, no console, no files.
  // Back/forward or a bookmark to one lands on the fleet instead.
  if (!sv || sv.state === "retired") {
    history.replaceState(null, "", "/");
    if (depth.open) surface();
    return;
  }
  if (depth.open && depth.serverId === sv.id) return;
  // No originating click to grow the sheet out of — open from center frame.
  openDepth(sv.id, innerWidth / 2, innerHeight / 2, null);
}

let routed = false;

// bootDeepLinks wires back/forward navigation and resolves the path the app
// was loaded on. Called from the authed boot path — the fleet must be loaded
// before an id can be resolved, and one extra refresh beats racing the poll.
export async function bootDeepLinks() {
  if (routed) return;
  routed = true;
  window.addEventListener("popstate", applyRoute);
  if (serverPathRE.test(location.pathname)) {
    await refreshFleet();
    applyRoute();
  }
}

async function refreshDetail() {
  const id = depth.serverId;
  if (!id) return;
  // The notice as it stood when the reads went out. The reads are slow (the
  // file listing reaches the node) and a power refusal is fast (no node
  // contact), so an action taken while they are in flight can put up its own
  // notice first — and that one must survive this refresh finishing.
  const noticeAtStart = depth.error;
  // Likewise the server state: the getServer read below is only as new as this
  // moment, and any write of depth.server made while the other reads are still
  // out is newer than it (#368).
  const genAtStart = serverGen;
  const logRead = ++installLogReads;
  const results = await Promise.allSettled([
    api.getServer(id),
    api.listBackups(id),
    api.listSchedules(id),
    api.getServerDns(id),
    api.getServerSettings(id),
    api.listFiles(id, "."),
    api.getServerSftp(id),
    api.getInstallLog(id),
  ]);
  if (depth.serverId !== id) return; // drilled elsewhere meanwhile
  const [srv, bk, sch, dns, settings, files, sftp, installLog] = results;
  if (bk.status === "fulfilled") {
    depth.backups = bk.value.backups ?? [];
    depth.backupMirror = bk.value.mirror ?? "";
  }
  // After the backups, so a restore found in flight can name its archive. And
  // only over the state it started from: a power action, a reinstall, a restore
  // or a fleet push that landed meanwhile holds a newer read than this one, and
  // its stream mode and label with it. The other seven reads are about things
  // no such action writes, so they apply regardless.
  if (srv.status === "fulfilled" && serverGen === genAtStart) {
    setDepthServer(srv.value);
    stream.set(id, streamModeFor(srv.value.state));
    syncUpdatePass(srv.value.state, stream.lines);
    syncRestore(srv.value);
  }
  if (sch.status === "fulfilled") depth.schedules = sch.value.schedules ?? [];
  if (dns.status === "fulfilled") depth.dns = dns.value;
  if (settings.status === "fulfilled") depth.settings = settings.value;
  if (files.status === "fulfilled") depth.files = files.value;
  // SFTP keeps its own error line: it is one optional affordance in the tab
  // strip, and a node that cannot answer for it must not blank the drill-in.
  depth.sftp = sftp.status === "fulfilled" ? sftp.value : null;
  depth.sftpError =
    sftp.status === "rejected" ? String(sftp.reason?.message ?? sftp.reason) : null;
  // Same reasoning for the install log: it is a look back at a finished phase,
  // and failing to read it must not take the drill-in's error line hostage. A
  // failed read writes nothing (see claimInstallLog).
  if (installLog.status === "fulfilled" && claimInstallLog(logRead)) {
    setInstallLog(installLog.value);
  }
  // The two optional reads are excluded from the drill-in's error line.
  const firstErr = results.slice(0, -2).find((r) => r.status === "rejected") as
    | PromiseRejectedResult
    | undefined;
  // Only this refresh's own verdict is written, and only over the notice it
  // started with: anything set since belongs to a later action.
  if (depth.error === noticeAtStart) {
    depth.error = firstErr ? String(firstErr.reason?.message ?? firstErr.reason) : null;
  }
}

/** The body of App's fleet-sync effect: re-sync the drill-in when the fleet
 *  poll delivers, and only then.
 *
 *  The untrack matters. syncDepthFromFleet reads depth.open, depth.serverId,
 *  depth.server and stream.lines, and an effect that called it bare re-ran on
 *  a change to any of them (#368). Every open (depth.open flips) pushed the
 *  fleet row back over the drill-in straight away, which also moved the
 *  generation before the refresh's reads returned, so the refresh's own server
 *  read was never applied. Every other write of depth.server was reverted to
 *  the fleet row on the next flush: the restore poll's and the restore POST's
 *  reads were undone, and the meter moved at fleet-poll speed. Named here so a
 *  test can mount the exact effect App does. */
export function followFleet() {
  void fleet.servers;
  untrack(syncDepthFromFleet);
}

/** The fleet poll keeps the drilled server's state in sync (chip, controls,
 *  stream mode) between detail refreshes. */
export function syncDepthFromFleet() {
  if (!depth.open || !depth.serverId) return;
  const s = fleet.servers.find((x) => x.id === depth.serverId);
  // Retired while open (#360; inherited — the mock draws no such moment): a
  // retired server has no drill-in (no node, no console, no files), so the
  // sheet surfaces to the fleet, where the row now sits in the retired group.
  if (s?.state === "retired") {
    surface();
    return;
  }
  if (s) {
    const was = depth.server?.state;
    setDepthServer(s);
    stream.set(s.id, streamModeFor(s.state));
    // Folded in here, synchronously with the push that carries the new state,
    // rather than left to a component effect to notice afterwards: the state is
    // what decides the label, so the label must not be able to outlive it.
    syncUpdatePass(s.state, stream.lines);
    syncRestore(s);
    // An install that just ended leaves a retained log the open drill-in has
    // never read — and this is exactly the moment it matters, because the
    // console's socket is about to switch to a container that may not start.
    // An install that just began is the other moment: the attempt it replaced
    // is now the log's `previous` (#381). The snapshot in hand predates the
    // button press, so its `previous` names the attempt before the wrong one —
    // it is dropped at once (the header goes with it) and only the re-read puts
    // one back. A re-read that fails leaves it dropped: no header beats a
    // header naming the wrong attempt. Every read already out predates the
    // press too, so none of them may land after the drop either. Likewise when
    // an install ends: a read already out may hold a mid-install snapshot, cut
    // off before the failure lines, and must not land over the verdict.
    if (was !== s.state && s.state === "installing") {
      setInstallLog(null);
      retireInstallLogReads();
      void refreshInstallLog();
    } else if (was !== s.state && (was === "installing" || was === "install_failed")) {
      retireInstallLogReads();
      void refreshInstallLog();
    }
  }
}

/** Assign the retained install log and name the instance. Every writer of
 *  `depth.installLog` goes through here: the console's view key is the only
 *  thing standing between a re-read and a viewport that jumps, and the snapshot
 *  itself has nothing in it that a re-read would change (#320). */
function setInstallLog(log: InstallLog | null) {
  depth.installLog = log;
  depth.installLogSeq++;
}

/** Install-log reads started so far; each read is numbered by it. */
let installLogReads = 0;
/** The high-water mark: no read numbered at or below it may apply. It moves up
 *  when a read's answer is applied, and when the held log is deliberately
 *  reset (see retireInstallLogReads). */
let installLogApplied = 0;

/** Whether a read that SUCCEEDED may apply its answer, and if so, record that
 *  it did. Only successful reads ask: a failed one claims nothing, so a newer
 *  read that fails leaves an older one still in flight free to land (#387). A
 *  success applies unless a newer read has already succeeded, or it was
 *  started before the last reset. */
function claimInstallLog(read: number): boolean {
  if (read <= installLogApplied) return false;
  installLogApplied = read;
  return true;
}

/** Rule out every install-log read already out. Called wherever the held log
 *  is deliberately reset — a (re)open, an install that begins, an install that
 *  ends — because each of those reads holds the log as it was before that
 *  moment: a pre-press log whose `previous` names the wrong attempt (#381), or
 *  a mid-install snapshot cut off before the verdict. */
function retireInstallLogReads() {
  installLogApplied = installLogReads;
}

/** Re-read the retained install log (after an install ends, or on demand). */
export async function refreshInstallLog() {
  const id = depth.serverId;
  if (!id) return;
  const mine = ++installLogReads;
  try {
    const log = await api.getInstallLog(id);
    if (depth.serverId === id && claimInstallLog(mine)) setInstallLog(log);
  } catch {
    // Optional surface: a failed read leaves the last value rather than
    // claiming the log is gone.
  }
}

// --- power -----------------------------------------------------------------

export async function power(action: PowerActionName) {
  if (!depth.serverId || depth.powerBusy) return;
  depth.powerBusy = true;
  // A new attempt starts clean, as a reinstall does. Otherwise a refusal outlives
  // its own fix: fill in the required setting it named, start, and the server
  // runs under a banner still saying it cannot.
  depth.error = null;
  try {
    await api.powerServer(depth.serverId, action);
    await refreshFleet();
    syncDepthFromFleet();
  } catch (e) {
    depth.error = errMsg(e);
  } finally {
    depth.powerBusy = false;
  }
}

/** Run the install script once, now. Two jobs, one endpoint: the retry a
 *  failed install needs (provisioning is the one state the power controls
 *  cannot recover from), and the "update" act on a stopped server whose start
 *  does not update it — a pinned build, or a spec that opted out. */
export async function reinstall() {
  if (!depth.serverId || depth.powerBusy) return;
  depth.powerBusy = true;
  depth.error = null;
  try {
    await api.reinstallServer(depth.serverId);
    await refreshFleet();
    syncDepthFromFleet();
  } catch (e) {
    depth.error = errMsg(e);
  } finally {
    depth.powerBusy = false;
  }
}

/** The delete button on a live server retires it (#360): a final backup, then
 *  the world goes and the row stays, retired and out of the grid, its backups
 *  kept. The Panel answers once the retire has started; the fleet poll picks
 *  up the rest. The final backup follows the retire block's toggle. */
export async function retireCurrentServer(): Promise<boolean> {
  if (!depth.serverId) return false;
  try {
    await api.retireServer(depth.serverId, depth.retireFinalBackup);
    await refreshFleet();
    return true;
  } catch (e) {
    depth.error = errMsg(e);
    return false;
  }
}

// --- files -----------------------------------------------------------------

export async function filesGo(dir: string) {
  if (!depth.serverId) return;
  depth.filesDir = dir;
  try {
    depth.files = await api.listFiles(depth.serverId, dir);
  } catch (e) {
    depth.error = errMsg(e);
  }
}

export async function filesUpload(files: File[]) {
  if (!depth.serverId || !files.length) return;
  try {
    await api.uploadFiles(depth.serverId, depth.filesDir, files);
    await filesGo(depth.filesDir);
  } catch (e) {
    depth.error = errMsg(e);
  }
}

/** Remove one entry — a file, or a folder and everything under it — then
 *  re-list the directory so the row leaves without a reload. `p` is the
 *  listing's own logical path (`/data/...`): the agent maps it onto the node's
 *  real root, `C:\data` included, so a host path must never be sent (#287).
 *  Resolves true when the agent confirmed the delete; a refusal lands in
 *  `depth.error`, the pane's notice. The listing is re-read either way — the
 *  usual refusal is a row that went stale (removed over SFTP, or by another
 *  session), and the honest response to that is the listing as it now is. */
export async function filesDelete(p: string): Promise<boolean> {
  if (!depth.serverId) return false;
  let ok = true;
  try {
    await api.deleteFiles(depth.serverId, [p]);
  } catch (e) {
    depth.error = errMsg(e);
    ok = false;
  }
  await filesGo(depth.filesDir);
  return ok;
}

/** Offer `href` to the browser as a save, through a throwaway anchor. */
function saveThroughAnchor(href: string, name: string) {
  const a = document.createElement("a");
  a.href = href;
  a.download = name;
  a.rel = "noopener";
  document.body.appendChild(a);
  a.click();
  a.remove();
}

/** Hand one entry to the browser as a download: a file as its raw bytes, a
 *  folder as the zip the Panel builds for it.
 *
 *  The session rides as a Bearer header, so the obvious `<a download href>`
 *  would arrive unauthenticated. The Panel's answer is a one-time, 60-second
 *  download token bound to this server, this exact path and the asking user
 *  (#304): mint one, point the anchor at the URL it returns, and the browser
 *  streams to disk with its own progress UI. Nothing here ever holds the
 *  bytes, so a multi-GB install tree is a download rather than a crashed tab.
 *
 *  There is no Blob fallback. The Panel embeds this bundle (`//go:embed`), so
 *  a UI that knows the mint route is being served by a Panel that has it —
 *  the version skew a fallback would guard against cannot happen. Every mint
 *  failure is therefore a real refusal (no permission, server gone, node
 *  unreachable) and belongs in the pane's notice, not in a second attempt.
 *
 *  `depth.downloading` holds the path while the MINT is in flight — that is
 *  all it can span, since the transfer itself belongs to the browser — so the
 *  pill can say so and a double click cannot mint twice. */
export async function filesDownload(f: FileEntry) {
  if (!depth.serverId || depth.downloading) return;
  const id = depth.serverId;
  depth.downloading = f.path;
  try {
    const minted = await api.mintDownloadToken(id, f.is_dir ? { paths: [f.path] } : { path: f.path });
    // The Panel names the save through Content-Disposition, which wins for a
    // same-origin navigation; this is the belt for anything that ignores it.
    saveThroughAnchor(minted.url, f.is_dir ? `${f.name}.zip` : f.name);
  } catch (e) {
    depth.error = errMsg(e);
  } finally {
    depth.downloading = null;
  }
}

// --- backups ---------------------------------------------------------------

// The ledger's one poll, at one cadence (2s): it runs while an archive is
// being created or a restore is in flight, and re-reads only what is moving —
// the list for a pending archive, the server for a restore's meter.

const LEDGER_POLL_MS = 2000;

function stopBackupPoll() {
  if (backupPoll !== undefined) {
    clearInterval(backupPoll);
    backupPoll = undefined;
  }
}

function backupPending(): boolean {
  return depth.backups.some((b) => b.state === "pending");
}

function ledgerNeedsPoll(): boolean {
  return backupPending() || depth.restoreWatch !== null || depth.server?.state === "restoring";
}

function startLedgerPoll() {
  if (depth.open && backupPoll === undefined) {
    backupPoll = setInterval(() => void ledgerTick().catch(() => {}), LEDGER_POLL_MS);
  }
}

// Every await in here can return after the drill-in has closed (or moved to
// another server); anything it did then would reopen the console socket and
// keep polling a sheet nobody is looking at, so each one re-checks.
function stillOn(id: string): boolean {
  return depth.open && depth.serverId === id;
}

async function ledgerTick() {
  const id = depth.serverId;
  if (!id || !depth.open) return stopBackupPoll();
  if (backupPending()) await refreshBackups();
  if (!stillOn(id)) return stopBackupPoll();
  if (depth.restoreWatch !== null || depth.server?.state === "restoring") {
    const s = await api.getServer(id);
    if (!stillOn(id)) return stopBackupPoll();
    setDepthServer(s);
    stream.set(id, streamModeFor(s.state));
    syncRestore(s);
  }
  if (!ledgerNeedsPoll()) stopBackupPoll();
}

async function refreshBackups() {
  const id = depth.serverId;
  if (!id) return;
  const r = await api.listBackups(id);
  if (depth.serverId !== id) return;
  depth.backups = r.backups ?? [];
  if (!ledgerNeedsPoll()) stopBackupPoll();
}

export async function backupCreate() {
  if (!depth.serverId || depth.creatingBackup) return;
  depth.creatingBackup = true;
  // The restore's outcome note is spoken once and carries no dismiss (the mock
  // has none): the next backup or restore action is what replaces it.
  depth.restoreNote = null;
  try {
    const name = "manual-" + new Date().toISOString().slice(0, 16).replace(/[T:]/g, "-");
    await api.createBackup(depth.serverId, name);
    await refreshBackups();
    // archives run asynchronously — poll while one is pending
    startLedgerPoll();
  } catch (e) {
    depth.error = errMsg(e);
  } finally {
    depth.creatingBackup = false;
  }
}

// --- restore (#361) -----------------------------------------------------------

/** What the ledger's restore meter draws. `sized` is whether there is a real
 *  figure at all: an agent too old to report progress, or a target that cannot
 *  size the archive, sends bytes_total 0, and the meter then reads "unknown" —
 *  a breathing dot and the phase word — never a percentage it would have to
 *  invent. Unsized also means the fill sits at 0 %, per DESIGN.md's "pushing
 *  measures itself": no figure is no fill, never a full one. */
export interface RestoreMeterView {
  pct: number;
  sized: boolean;
  /** Whether the row prints a number: a sized restore while it is extracting.
   *  Only then does the reading sit in its own `.pct` box; every other moment
   *  of the restore — opening, applying, an unsized one throughout — shows the
   *  `.phase` word instead, never both (the row is a two-column grid, and a
   *  third reading beside them would wrap). */
  numeric: boolean;
  /** The phase word the `.phase` slot narrates when the row is not numeric. */
  phase: string;
  /** What the row says for assistive tech and its status line: the percentage
   *  when numeric, the phase word otherwise. */
  label: string;
}

const PHASE_WORDS: Record<string, string> = {
  opening: "opening",
  extracting: "extracting",
  applying: "applying",
  done: "done",
  restoring: "restoring",
};

export function restoreMeter(r: RestoreProgress | null | undefined): RestoreMeterView {
  const total = r?.bytes_total ?? 0;
  const sized = total > 0;
  const pct = sized ? Math.max(0, Math.min(100, Math.floor(((r?.bytes_done ?? 0) * 100) / total))) : 0;
  const phase = r?.phase ?? "opening";
  const word = PHASE_WORDS[phase] ?? phase;
  const numeric = sized && phase === "extracting";
  return { pct, sized, numeric, phase: word, label: numeric ? `${pct}%` : word };
}

/** Whether a restore is running or being asked for. Every restore button in
 *  the ledger is disabled while this holds — the Panel refuses a second one
 *  (409 restore_in_progress), so the control says so before the click. */
export function restoreActive(server: Server | null | undefined, requesting: boolean): boolean {
  return requesting || server?.state === "restoring" || server?.restore !== undefined;
}

/** How a watched restore ended, once the server says it has: the row is out
 *  of `restoring`, carries no job, and holds a `restore_result` that finished
 *  after the watch began. Read from restore_result and never from last_error —
 *  a restore does not write last_error, and an install_failed server's own
 *  reason there is not this restore's. */
export function restoreOutcome(
  watch: RestoreWatch | null,
  server: Server | null | undefined,
  backups: readonly Backup[],
): RestoreNote | null {
  if (!watch || !server || server.id !== watch.serverId) return null;
  if (server.state === "restoring" || server.restore !== undefined) return null;
  const res = server.restore_result;
  // `>=`, not `>`: the Panel writes nanoseconds and Date.parse keeps
  // milliseconds, so a restore that ends within the millisecond it began would
  // otherwise never settle. Equal cannot be an earlier restore's result — each
  // restore has its own start, and a new one clears the old result.
  if (!res || !(Date.parse(res.finished_at) >= Date.parse(watch.since))) return null;
  const id = res.backup_id || watch.backupId;
  const archive = backups.find((b) => b.id === id);
  const name = archive?.name ?? id;
  const when = archive?.created_ms ?? 0;
  if (!res.ok) return { kind: "failed", name, when, reason: res.error ?? "" };
  return { kind: "done", name, when, reason: "" };
}

/** The outcome note's sentence, as the ledger speaks it: the archive named the
 *  way its row was (`aug 21 03:00 · nightly`), and — for a landed restore on a
 *  stopped server — the one next step. `stopped` is whether the server is
 *  offline now: a restore that put the row back to crashed or install_failed
 *  is not one to start "when ready". */
export function restoreNoteText(note: RestoreNote, stopped: boolean): string {
  const which = note.when ? `${fmtWhen(note.when)} · ${note.name}` : note.name;
  if (note.kind === "failed") return `restore of ${which} failed${note.reason ? " — " + note.reason : ""}`;
  return `restored ${which}${stopped ? " — start the server when ready" : ""}`;
}

/** Fold a fresh server read into the restore watch: adopt a restore found in
 *  flight (the drill-in opened mid-restore, or another operator started it),
 *  and turn a watched one that has ended into the ledger's note. */
function syncRestore(s: Server) {
  if (depth.serverId !== s.id) return;
  if (depth.restoreWatch === null && (s.state === "restoring" || s.restore !== undefined)) {
    depth.restoreWatch = {
      serverId: s.id,
      backupId: s.restore?.backup_id ?? "",
      since: s.restore?.started_at ?? new Date().toISOString(),
    };
    depth.restoreNote = null;
    startLedgerPoll();
    return;
  }
  if (depth.restoreWatch !== null && !depth.restoreWatch.backupId && s.restore) {
    depth.restoreWatch = {
      ...depth.restoreWatch,
      backupId: s.restore.backup_id,
      since: s.restore.started_at,
    };
  }
  const note = restoreOutcome(depth.restoreWatch, s, depth.backups);
  if (note) {
    depth.restoreNote = note;
    depth.restoreWatch = null;
  }
}

export async function backupRestore(b: Backup) {
  if (!depth.serverId || restoreActive(depth.server, depth.restoringBackup !== null)) return;
  const id = depth.serverId;
  depth.restoringBackup = b.id;
  depth.restoreNote = null;
  try {
    const s = await api.restoreBackup(id, b.id);
    if (!depth.open || depth.serverId !== id) return;
    // The 202 already carries the server in `restoring`: take it now, so the
    // meter replaces the row on this frame instead of the next poll's.
    depth.restoreWatch = {
      serverId: id,
      backupId: b.id,
      since: s.restore?.started_at ?? new Date().toISOString(),
    };
    setDepthServer(s);
    stream.set(id, streamModeFor(s.state));
    syncRestore(s);
    startLedgerPoll();
    void refreshFleet().catch(() => {});
  } catch (e) {
    depth.error = errMsg(e);
  } finally {
    depth.restoringBackup = null;
  }
}

export async function backupDelete(b: Backup) {
  if (!depth.serverId) return;
  try {
    await api.deleteBackup(depth.serverId, b.id);
    depth.backups = depth.backups.filter((x) => x.id !== b.id);
  } catch (e) {
    depth.error = errMsg(e);
  }
}

// --- schedules ---------------------------------------------------------------

export async function scheduleToggle(t: ScheduledTask) {
  if (!depth.serverId) return;
  try {
    const updated = await api.updateSchedule(depth.serverId, t.id, {
      name: t.name,
      action: t.action,
      cron: t.cron,
      command: t.command,
      enabled: !t.enabled,
    });
    depth.schedules = depth.schedules.map((x) => (x.id === t.id ? updated : x));
  } catch (e) {
    depth.error = errMsg(e);
  }
}

export async function scheduleDelete(t: ScheduledTask) {
  if (!depth.serverId) return;
  try {
    await api.deleteSchedule(depth.serverId, t.id);
    depth.schedules = depth.schedules.filter((x) => x.id !== t.id);
  } catch (e) {
    depth.error = errMsg(e);
  }
}

export async function scheduleAdd(input: ScheduleInput): Promise<boolean> {
  if (!depth.serverId) return false;
  try {
    const created = await api.createSchedule(depth.serverId, input);
    depth.schedules = [...depth.schedules, created];
    return true;
  } catch (e) {
    depth.error = errMsg(e);
    return false;
  }
}

// --- settings ----------------------------------------------------------------

export async function settingsApply(
  values: Record<string, string>,
  variables: Record<string, string>,
  // The build pin, only when the operator touched it: an omitted field leaves
  // the server's pin alone, so an ordinary settings save cannot unpin it.
  pinBuild?: boolean,
): Promise<string | null> {
  if (!depth.serverId) return null;
  try {
    const r = await api.updateServerSettings(depth.serverId, values, variables, pinBuild);
    if (depth.settings) {
      depth.settings.values = r.values;
      depth.settings.from_spec = r.from_spec ?? [];
      if (r.variables) depth.settings.variables = r.variables;
      if (r.pin_build !== undefined) depth.settings.pin_build = r.pin_build;
    }
    if (r.applied && r.hot_reload) return "saved — the game re-reads config live";
    return r.restart_needed ? "saved — applies on next restart" : "saved";
  } catch (e) {
    depth.error = errMsg(e);
    return null;
  }
}

// --- dns / forwards -----------------------------------------------------------

export async function dnsPublish(name: string, service?: string) {
  if (!depth.serverId) return;
  if (!name) {
    // never a silent no-op: the publish flip resyncs off depth.dns, so the
    // caller sees the pill snap back and this message says why
    depth.error = "enter a hostname before publishing";
    return;
  }
  try {
    await api.setServerDns(depth.serverId, { name, service: service || undefined });
    depth.dns = await api.getServerDns(depth.serverId);
  } catch (e) {
    depth.error = errMsg(e);
  }
}

export async function dnsUnpublish() {
  if (!depth.serverId) return;
  try {
    await api.deleteServerDns(depth.serverId);
    depth.dns = await api.getServerDns(depth.serverId);
  } catch (e) {
    depth.error = errMsg(e);
  }
}

// --- sftp ------------------------------------------------------------------

export function sftpShow() {
  depth.sftpError = null;
  depth.sftpOpen = true;
}

export function sftpHide() {
  depth.sftpOpen = false;
  // The One Sighting Rule: closing ends the reveal. The plaintext is gone
  // rather than remembered, which is exactly what reopening does in production.
  depth.sftpPassword = null;
  depth.sftpError = null;
}

async function sftpRun(fn: (id: string) => Promise<SftpStatus>) {
  if (!depth.serverId || depth.sftpBusy) return;
  depth.sftpBusy = true;
  depth.sftpError = null;
  try {
    depth.sftp = await fn(depth.serverId);
  } catch (e) {
    depth.sftpError = errMsg(e);
  } finally {
    depth.sftpBusy = false;
  }
}

export async function sftpRotate() {
  if (!depth.serverId || depth.sftpBusy) return;
  depth.sftpBusy = true;
  depth.sftpError = null;
  try {
    const r = await api.resetServerSftpPassword(depth.serverId);
    depth.sftp = r.status;
    depth.sftpPassword = r.password;
  } catch (e) {
    depth.sftpError = errMsg(e);
  } finally {
    depth.sftpBusy = false;
  }
}

export async function sftpAddKey(key: string) {
  const k = key.trim();
  if (!k) return;
  const keys = [...(depth.sftp?.keys ?? []), k];
  await sftpRun((id) => api.setServerSftpKeys(id, keys));
}

export async function sftpRemoveKey(key: string) {
  const keys = (depth.sftp?.keys ?? []).filter((k) => k !== key);
  await sftpRun((id) => api.setServerSftpKeys(id, keys));
}

export async function sftpDisable() {
  await sftpRun((id) => api.disableServerSftp(id));
  if (!depth.sftpError) sftpHide();
}

export async function forwardSet(portName: string, open: boolean) {
  if (!depth.serverId) return;
  try {
    const r = await api.setServerForward(depth.serverId, portName, open);
    if (depth.dns) depth.dns.forwards = r.forwards;
  } catch (e) {
    // the caller resyncs its toggle from depth.dns after this returns, which is
    // what actually snaps a refused flip back (reassigning depth.dns here never
    // did — the memoized checked attribute saw the same value and skipped)
    depth.error = errMsg(e);
  }
}
