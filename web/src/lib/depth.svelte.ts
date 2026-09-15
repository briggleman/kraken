// Drill-in data: one server's live detail — stream, settings, files, backups,
// schedules, DNS — fetched on open and kept fresh while the overlay is up.

import { api } from "@/api/client";
import type {
  Backup,
  FileEntry,
  FileListing,
  InstallLog,
  PowerActionName,
  ScheduledTask,
  Server,
  ServerDnsState,
  ServerSettings,
  SftpStatus,
} from "@/api/types";
import type { ScheduleInput } from "@/api/client";
import { ServerStream, type StreamMode } from "./stream.svelte";
import { fleet, refreshFleet } from "./fleet.svelte";

export interface Origin {
  ox: string;
  oy: string;
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
  // The logical path of the entry being fetched for download, so its pill can
  // say so; null between downloads. One at a time — see filesDownload.
  downloading: null as string | null,
  // The retained output of this server's last install, and whether the console
  // pane is showing it instead of the container's log. An install that "worked"
  // is the case this exists for: the console has nothing to tail for a server
  // that never started, and the install is the only account of why (#280).
  installLog: null as InstallLog | null,
  installLogOpen: false,
  // Whether this server's current `installing` is the pre-start update pass
  // (#307) rather than a first install. See syncUpdatePass — the state is the
  // fact, this is only a finer name for one of its causes.
  updatePass: false,
  creatingBackup: false,
  restoringBackup: null as string | null,
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
export function powerControls(state: Server["state"] | undefined): "stop" | "start" {
  return state === "running" || state === "starting" || state === "stopping" ? "stop" : "start";
}

export function openDepth(id: string, x: number, y: number, returnTo?: HTMLElement | null) {
  lastFocus = returnTo ?? null;
  depth.origin = {
    ox: (x / innerWidth) * 100 + "%",
    oy: (y / innerHeight) * 100 + "%",
  };
  depth.serverId = id;
  depth.server = fleet.servers.find((s) => s.id === id) ?? null;
  depth.backups = [];
  depth.backupMirror = "";
  depth.schedules = [];
  depth.dns = null;
  depth.settings = null;
  depth.files = null;
  depth.filesDir = ".";
  depth.installLog = null;
  depth.installLogOpen = false;
  // A fresh server, and a fresh socket: nothing is latched until this server's
  // own state and console say so.
  depth.updatePass = false;
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
  if (!sv) {
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
  if (srv.status === "fulfilled") {
    depth.server = srv.value;
    stream.set(id, streamModeFor(srv.value.state));
    syncUpdatePass(srv.value.state, stream.lines);
  }
  if (bk.status === "fulfilled") {
    depth.backups = bk.value.backups ?? [];
    depth.backupMirror = bk.value.mirror ?? "";
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
  // and failing to read it must not take the drill-in's error line hostage.
  depth.installLog = installLog.status === "fulfilled" ? installLog.value : null;
  // The two optional reads are excluded from the drill-in's error line.
  const firstErr = results.slice(0, -2).find((r) => r.status === "rejected") as
    | PromiseRejectedResult
    | undefined;
  depth.error = firstErr ? String(firstErr.reason?.message ?? firstErr.reason) : null;
}

/** The fleet poll keeps the drilled server's state in sync (chip, controls,
 *  stream mode) between detail refreshes. */
export function syncDepthFromFleet() {
  if (!depth.open || !depth.serverId) return;
  const s = fleet.servers.find((x) => x.id === depth.serverId);
  if (s) {
    const was = depth.server?.state;
    depth.server = s;
    stream.set(s.id, streamModeFor(s.state));
    // Folded in here, synchronously with the push that carries the new state,
    // rather than left to a component effect to notice afterwards: the state is
    // what decides the label, so the label must not be able to outlive it.
    syncUpdatePass(s.state, stream.lines);
    // An install that just ended leaves a retained log the open drill-in has
    // never read — and this is exactly the moment it matters, because the
    // console's socket is about to switch to a container that may not start.
    if (was !== s.state && (was === "installing" || was === "install_failed")) {
      void refreshInstallLog();
    }
  }
}

/** Re-read the retained install log (after an install ends, or on demand). */
export async function refreshInstallLog() {
  const id = depth.serverId;
  if (!id) return;
  try {
    const log = await api.getInstallLog(id);
    if (depth.serverId === id) depth.installLog = log;
  } catch {
    // Optional surface: a failed read leaves the last value rather than
    // claiming the log is gone.
  }
}

// --- power -----------------------------------------------------------------

export async function power(action: PowerActionName) {
  if (!depth.serverId || depth.powerBusy) return;
  depth.powerBusy = true;
  try {
    await api.powerServer(depth.serverId, action);
    await refreshFleet();
    syncDepthFromFleet();
  } catch (e) {
    depth.error = e instanceof Error ? e.message : String(e);
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
    depth.error = e instanceof Error ? e.message : String(e);
  } finally {
    depth.powerBusy = false;
  }
}

export async function deleteCurrentServer(): Promise<boolean> {
  if (!depth.serverId) return false;
  try {
    await api.deleteServer(depth.serverId);
    await refreshFleet();
    return true;
  } catch (e) {
    depth.error = e instanceof Error ? e.message : String(e);
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
    depth.error = e instanceof Error ? e.message : String(e);
  }
}

export async function filesUpload(files: File[]) {
  if (!depth.serverId || !files.length) return;
  try {
    await api.uploadFiles(depth.serverId, depth.filesDir, files);
    await filesGo(depth.filesDir);
  } catch (e) {
    depth.error = e instanceof Error ? e.message : String(e);
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
    depth.error = e instanceof Error ? e.message : String(e);
    ok = false;
  }
  await filesGo(depth.filesDir);
  return ok;
}

/** Hand one entry to the browser as a download: a file as its raw bytes, a
 *  folder as the zip the Panel builds for it. The session rides as a Bearer
 *  header, so this cannot be a plain link — the bytes are fetched into a Blob
 *  and offered through a throwaway anchor, which means the whole payload sits
 *  in browser memory first. Fine for configs and saves; a multi-GB install
 *  tree wants a streamed, tokenised URL instead (#304). One download at a time
 *  per pane: `depth.downloading` holds the path in flight so its pill can say
 *  so, and a refusal lands in the pane's notice like every other file op. */
export async function filesDownload(f: FileEntry) {
  if (!depth.serverId || depth.downloading) return;
  depth.downloading = f.path;
  try {
    const dl = f.is_dir
      ? await api.downloadZip(depth.serverId, [f.path], `${f.name}.zip`)
      : await api.downloadFile(depth.serverId, f.path);
    const url = URL.createObjectURL(dl.blob);
    const a = document.createElement("a");
    a.href = url;
    // A file keeps the name the Panel put in Content-Disposition. A folder's
    // zip is named by the Panel after the SERVER ("midgard-files.zip"), which
    // says nothing about which folder it holds — so it is saved as the folder.
    a.download = f.is_dir ? `${f.name}.zip` : dl.filename;
    a.rel = "noopener";
    document.body.appendChild(a);
    a.click();
    a.remove();
    // Revoke on the next tick: the click has to have started the save first.
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  } catch (e) {
    depth.error = e instanceof Error ? e.message : String(e);
  } finally {
    depth.downloading = null;
  }
}

// --- backups ---------------------------------------------------------------

function stopBackupPoll() {
  if (backupPoll !== undefined) {
    clearInterval(backupPoll);
    backupPoll = undefined;
  }
}

async function refreshBackups() {
  if (!depth.serverId) return;
  const r = await api.listBackups(depth.serverId);
  depth.backups = r.backups ?? [];
  if (!depth.backups.some((b) => b.state === "pending")) stopBackupPoll();
}

export async function backupCreate() {
  if (!depth.serverId || depth.creatingBackup) return;
  depth.creatingBackup = true;
  try {
    const name = "manual-" + new Date().toISOString().slice(0, 16).replace(/[T:]/g, "-");
    await api.createBackup(depth.serverId, name);
    await refreshBackups();
    // archives run asynchronously — poll while one is pending
    stopBackupPoll();
    backupPoll = setInterval(() => void refreshBackups().catch(() => {}), 2000);
  } catch (e) {
    depth.error = e instanceof Error ? e.message : String(e);
  } finally {
    depth.creatingBackup = false;
  }
}

export async function backupRestore(b: Backup) {
  if (!depth.serverId || depth.restoringBackup) return;
  depth.restoringBackup = b.id;
  try {
    await api.restoreBackup(depth.serverId, b.id);
  } catch (e) {
    depth.error = e instanceof Error ? e.message : String(e);
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
    depth.error = e instanceof Error ? e.message : String(e);
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
    depth.error = e instanceof Error ? e.message : String(e);
  }
}

export async function scheduleDelete(t: ScheduledTask) {
  if (!depth.serverId) return;
  try {
    await api.deleteSchedule(depth.serverId, t.id);
    depth.schedules = depth.schedules.filter((x) => x.id !== t.id);
  } catch (e) {
    depth.error = e instanceof Error ? e.message : String(e);
  }
}

export async function scheduleAdd(input: ScheduleInput): Promise<boolean> {
  if (!depth.serverId) return false;
  try {
    const created = await api.createSchedule(depth.serverId, input);
    depth.schedules = [...depth.schedules, created];
    return true;
  } catch (e) {
    depth.error = e instanceof Error ? e.message : String(e);
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
      if (r.variables) depth.settings.variables = r.variables;
      if (r.pin_build !== undefined) depth.settings.pin_build = r.pin_build;
    }
    if (r.applied && r.hot_reload) return "saved — the game re-reads config live";
    return r.restart_needed ? "saved — applies on next restart" : "saved";
  } catch (e) {
    depth.error = e instanceof Error ? e.message : String(e);
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
    depth.error = e instanceof Error ? e.message : String(e);
  }
}

export async function dnsUnpublish() {
  if (!depth.serverId) return;
  try {
    await api.deleteServerDns(depth.serverId);
    depth.dns = await api.getServerDns(depth.serverId);
  } catch (e) {
    depth.error = e instanceof Error ? e.message : String(e);
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
    depth.sftpError = e instanceof Error ? e.message : String(e);
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
    depth.sftpError = e instanceof Error ? e.message : String(e);
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
    depth.error = e instanceof Error ? e.message : String(e);
  }
}
