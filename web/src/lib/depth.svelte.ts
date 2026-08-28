// Drill-in data: one server's live detail — stream, settings, files, backups,
// schedules, DNS — fetched on open and kept fresh while the overlay is up.

import { api, ApiError } from "@/api/client";
import type {
  Backup,
  FileListing,
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
  schedules: [] as ScheduledTask[],
  dns: null as ServerDnsState | null,
  settings: null as ServerSettings | null,
  files: null as FileListing | null,
  filesDir: ".",
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

export function openDepth(id: string, x: number, y: number, returnTo?: HTMLElement | null) {
  lastFocus = returnTo ?? null;
  depth.origin = {
    ox: (x / innerWidth) * 100 + "%",
    oy: (y / innerHeight) * 100 + "%",
  };
  depth.serverId = id;
  depth.server = fleet.servers.find((s) => s.id === id) ?? null;
  depth.backups = [];
  depth.schedules = [];
  depth.dns = null;
  depth.settings = null;
  depth.files = null;
  depth.filesDir = ".";
  depth.error = null;
  depth.sftp = null;
  depth.sftpOpen = false;
  depth.sftpBusy = false;
  depth.sftpError = null;
  depth.sftpPassword = null;
  depth.open = true;
  stream.set(id, streamModeFor(depth.server?.state));
  void refreshDetail();
}

export function surface() {
  depth.open = false;
  stream.set("", "off");
  stopBackupPoll();
  if (lastFocus && document.contains(lastFocus)) lastFocus.focus();
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
  ]);
  if (depth.serverId !== id) return; // drilled elsewhere meanwhile
  const [srv, bk, sch, dns, settings, files, sftp] = results;
  if (srv.status === "fulfilled") {
    depth.server = srv.value;
    stream.set(id, streamModeFor(srv.value.state));
  }
  if (bk.status === "fulfilled") depth.backups = bk.value.backups ?? [];
  if (sch.status === "fulfilled") depth.schedules = sch.value.schedules ?? [];
  if (dns.status === "fulfilled") depth.dns = dns.value;
  if (settings.status === "fulfilled") depth.settings = settings.value;
  if (files.status === "fulfilled") depth.files = files.value;
  // SFTP keeps its own error line: it is one optional affordance in the tab
  // strip, and a node that cannot answer for it must not blank the drill-in.
  depth.sftp = sftp.status === "fulfilled" ? sftp.value : null;
  depth.sftpError =
    sftp.status === "rejected" ? String(sftp.reason?.message ?? sftp.reason) : null;
  const firstErr = results.slice(0, -1).find((r) => r.status === "rejected") as
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
    depth.server = s;
    stream.set(s.id, streamModeFor(s.state));
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

/** A failed install is a dead end without the retry: provisioning is the one
 *  state the power controls cannot recover from. */
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
): Promise<string | null> {
  if (!depth.serverId) return null;
  try {
    const r = await api.updateServerSettings(depth.serverId, values, variables);
    if (depth.settings) {
      depth.settings.values = r.values;
      if (r.variables) depth.settings.variables = r.variables;
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
  if (!depth.serverId || !name) return;
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
    if (e instanceof ApiError && depth.dns) {
      // flip back — the gateway refused or isn't configured
      depth.dns = { ...depth.dns };
    }
    depth.error = e instanceof Error ? e.message : String(e);
  }
}
