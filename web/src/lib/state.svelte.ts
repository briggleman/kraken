// Central app state for Pass A of the web-svelte migration: the mock's
// behavior model, translated from design/mockups/spog-abyssal-ops.html's
// script block into Svelte 5 runes. Every seeded value, walk bound, and
// cadence is verbatim from the mock. Pass B replaces the simulated data
// sources with web/src/api/client.ts, keeping this module's shape.

import { stepWalk, seedHistory, pushSample, type WalkSpec } from "./walk";

export const reducedMotion =
  typeof matchMedia !== "undefined" && matchMedia("(prefers-reduced-motion: reduce)").matches;

// ---------------------------------------------------------------------------
// instruments

export interface Track {
  walk: WalkSpec;
  history: number[];
}

function track(spec: WalkSpec, length: number): Track {
  return { walk: { ...spec }, history: seedHistory(spec, length) };
}

export interface NodeSim {
  name: string;
  meta1: string;
  meta2: string;
  aria: string;
  cpu: Track;
  mem: Track;
  memGb: number;
  disk: string; // static readout, e.g. "3.1" — chart is a spark
  diskUnit: string;
  diskSeed: number;
  net: { base: number; span: number; ref: number; rate: number };
  temp: { floor: number; div: number; deg: number };
  packets: string[]; // verbatim inline styles from the mock
}

export interface RosterEntry {
  name: string;
  time: string;
}

export interface ConsoleLine {
  t: string;
  html: string; // trusted app constant — good/warn spans, never user input
}

export interface DepthData {
  state: "running" | "stopped";
  up: string;
  players: string;
  port: string;
  ver: string;
  cpuV: number | null;
  memU: number | null;
  memM: number;
  tickV: number | null;
  worldG: string;
  roster: RosterEntry[];
  lines: ConsoleLine[];
}

export interface ServerSim {
  name: string;
  rackNode: string; // "node 01"
  rackHost: string; // "behemoth"
  meta: string;
  state: "running" | "stopped";
  art: string;
  playersNum: string;
  playersMax: string;
  capPct: number;
  deadNote?: string;
  cpu?: Track; // running cards only
  mem?: Track;
  memGb?: number;
  depth?: DepthData; // servers with a drill-in record (mock: three of six)
}

const NODE_TRACK = 48;
const CARD_TRACK = 72;

function lines(rows: string[]): ConsoleLine[] {
  return rows.map((html, i) => ({ t: `23:4${i}:0${i * 2}`, html }));
}

export const sim = $state({
  clock: "23:47:12",
  ping: "2s",

  nodes: [
    {
      name: "BEHEMOTH",
      meta1: "node 01 · 16c/32t · up 27d 06h",
      meta2: "10.0.0.4 · debian 12",
      aria: "Node BEHEMOTH vitals",
      cpu: track({ v: 34, lo: 12, hi: 88, step: 8 }, NODE_TRACK),
      mem: track({ v: 33, lo: 24, hi: 62, step: 6 }, NODE_TRACK),
      memGb: 64,
      disk: "3.1",
      diskUnit: "/8T",
      diskSeed: 39,
      net: { base: 10, span: 6, ref: 12.4, rate: 12.4 },
      temp: { floor: 44, div: 9, deg: 47 },
      packets: [
        "--s:4; --d:3.1s; --dl:-0.5s; top:30%",
        "--s:6; --d:4.4s; --dl:-2.4s; top:42%",
        "--s:2.5; --d:2.2s; --dl:-1.4s; top:24%",
        "--s:3; --d:2.7s; --dl:-0.2s; top:36%",
        "rev|--s:3; --d:3.6s; --dl:-1.8s; top:62%",
        "rev|--s:2; --d:2.5s; --dl:-0.7s; top:72%",
        "rev|--s:4.5; --d:4.9s; --dl:-3s; top:68%",
      ],
    },
    {
      name: "TITAN",
      meta1: "node 02 · 8c/16t · up 6d 11h",
      meta2: "10.0.0.7 · debian 12",
      aria: "Node TITAN vitals",
      cpu: track({ v: 19, lo: 6, hi: 52, step: 7 }, NODE_TRACK),
      mem: track({ v: 44, lo: 30, hi: 68, step: 5 }, NODE_TRACK),
      memGb: 32,
      disk: "1.2",
      diskUnit: "/4T",
      diskSeed: 30,
      net: { base: 3, span: 4, ref: 5.2, rate: 4.3 },
      temp: { floor: 39, div: 8, deg: 41 },
      packets: [
        "--s:3; --d:3.4s; --dl:-0.9s; top:28%",
        "--s:5; --d:4.8s; --dl:-2.1s; top:44%",
        "--s:2; --d:2.6s; --dl:-1.1s; top:34%",
        "rev|--s:2.5; --d:3.9s; --dl:-2.6s; top:64%",
        "rev|--s:3.5; --d:4.3s; --dl:-0.4s; top:70%",
      ],
    },
  ] as NodeSim[],

  servers: [
    {
      name: "Palworld",
      rackNode: "node 01",
      rackHost: "behemoth",
      meta: "feybreak-01 · v0.6.1 · :8211 · up 3d 14h",
      state: "running",
      art: "https://cdn.cloudflare.steamstatic.com/steam/apps/1623730/library_hero.jpg",
      playersNum: "7",
      playersMax: "32",
      capPct: 22,
      cpu: track({ v: 22, lo: 12, hi: 45, step: 5 }, CARD_TRACK),
      mem: track({ v: 50, lo: 40, hi: 62, step: 4 }, CARD_TRACK),
      memGb: 16,
      depth: {
        state: "running",
        up: "3d 14h",
        players: "7/32",
        port: "8211",
        ver: "v0.6.1",
        cpuV: 22,
        memU: 14.2,
        memM: 16,
        tickV: 31,
        worldG: "4.8",
        roster: [
          { name: "Kestrel", time: "6m" },
          { name: "MossVeil", time: "1h 12m" },
          { name: "Brakkus", time: "2h 03m" },
          { name: "softserve", time: "2h 40m" },
          { name: "Anchorite", time: "3h 15m" },
          { name: "Pellet", time: "4h 27m" },
          { name: "DGRhino", time: "5h 58m" },
        ],
        lines: lines([
          "[Session] world feybreak-01 autosaved (2.41s)",
          '<span class="good">[Join] Kestrel connected — 7 online</span>',
          "[Pal] wild raid event scheduled at Bamboo Grove",
          '<span class="warn">[Perf] tick 48ms (budget 50ms) — watching</span>',
          "[Net] keepalive ok — 7 clients",
        ]),
      },
    },
    {
      name: "Enshrouded",
      rackNode: "node 01",
      rackHost: "behemoth",
      meta: "embervale · v0.8.3 · :15636 · up 11h 02m",
      state: "running",
      art: "https://cdn.cloudflare.steamstatic.com/steam/apps/1203620/library_hero.jpg",
      playersNum: "3",
      playersMax: "16",
      capPct: 19,
      cpu: track({ v: 9, lo: 5, hi: 22, step: 4 }, CARD_TRACK),
      mem: track({ v: 37, lo: 30, hi: 44, step: 3 }, CARD_TRACK),
      memGb: 12,
      depth: {
        state: "running",
        up: "11h 02m",
        players: "3/16",
        port: "15636",
        ver: "v0.8.3",
        cpuV: 9,
        memU: 6.8,
        memM: 12,
        tickV: 24,
        worldG: "2.1",
        roster: [
          { name: "Hollowfen", time: "24m" },
          { name: "Bramble", time: "1h 05m" },
          { name: "Vess", time: "2h 18m" },
        ],
        lines: lines([
          "[Session] embervale autosaved (1.12s)",
          '<span class="good">[Join] Vess connected — 3 online</span>',
          "[Shroud] storm cycle beginning in Revelwood",
          "[Net] keepalive ok — 3 clients",
        ]),
      },
    },
    {
      name: "Dragonwilds",
      rackNode: "node 01",
      rackHost: "behemoth",
      meta: "runescape · v1.2.0 · :43594 · stopped 22:58",
      state: "stopped",
      art: "https://cdn.cloudflare.steamstatic.com/steam/apps/3061810/library_hero.jpg",
      playersNum: "—",
      playersMax: "24",
      capPct: 0,
      deadNote: "last session 6d 01h · world saved · no errors on shutdown",
      depth: {
        state: "stopped",
        up: "—",
        players: "0/24",
        port: "43594",
        ver: "v1.2.0",
        cpuV: null,
        memU: null,
        memM: 24,
        tickV: null,
        worldG: "3.3",
        roster: [],
        lines: lines([
          "[Daemon] server stopped by owner at 22:58",
          '<span class="good">[Save] world rs-dragonwilds flushed clean</span>',
          "[Daemon] awaiting start command",
        ]),
      },
    },
    {
      name: "Valheim",
      rackNode: "node 02",
      rackHost: "titan",
      meta: "yggdrasil · v0.220.5 · :2456 · up 9d 03h",
      state: "running",
      art: "https://cdn.cloudflare.steamstatic.com/steam/apps/892970/library_hero.jpg",
      playersNum: "4",
      playersMax: "10",
      capPct: 40,
      cpu: track({ v: 14, lo: 8, hi: 31, step: 4 }, CARD_TRACK),
      mem: track({ v: 46, lo: 40, hi: 58, step: 3 }, CARD_TRACK),
      memGb: 8,
    },
    {
      name: "Factorio",
      rackNode: "node 02",
      rackHost: "titan",
      meta: "megabase · v2.0.28 · :34197 · up 21d 17h",
      state: "running",
      art: "https://cdn.cloudflare.steamstatic.com/steam/apps/427520/library_hero.jpg",
      playersNum: "2",
      playersMax: "8",
      capPct: 25,
      cpu: track({ v: 31, lo: 18, hi: 52, step: 6 }, CARD_TRACK),
      mem: track({ v: 69, lo: 58, hi: 80, step: 4 }, CARD_TRACK),
      memGb: 6,
    },
    {
      name: "V Rising",
      rackNode: "node 02",
      rackHost: "titan",
      meta: "vardoran · v1.1.4 · :9876 · stopped 19:41",
      state: "stopped",
      art: "https://cdn.cloudflare.steamstatic.com/steam/apps/1604030/library_hero.jpg",
      playersNum: "—",
      playersMax: "10",
      capPct: 0,
      deadNote: "last session 2d 08h · world saved · stopped for a spec update",
    },
  ] as ServerSim[],

  events: [
    { t: "23:41", who: "palworld", text: " — kestrel joined · 7 online", warn: false },
    { t: "23:37", who: "", text: "behemoth — disk i/o spike settled", warn: true },
    { t: "22:58", who: "dragonwilds", text: " — stopped by owner", warn: false },
    { t: "22:14", who: "enshrouded", text: " — backup completed · 1.2G", warn: false },
  ],

  // the drill-in's side blocks (one demo set, as in the mock)
  backups: [
    { kind: "scheduled", label: "tonight 03:00" },
    { kind: "done", label: "aug 21 03:00 · 4.8G", restoring: false, leaving: false },
    { kind: "done", label: "aug 20 03:00 · 4.7G", restoring: false, leaving: false },
  ] as BackupRow[],
  backupLive: null as null | { name: string; prog: number },
  backupSeq: 1,

  schedules: [
    { name: "nightly backup", detail: "backup", cron: "0 3 * * *", paused: false, leaving: false },
    { name: "nightly restart", detail: "restart", cron: "0 4 * * *", paused: false, leaving: false },
    { name: "autosave sweep", detail: "command: save-all", cron: "0 * * * *", paused: true, leaving: false },
  ] as ScheduleRow[],
});

export interface BackupRow {
  kind: "scheduled" | "done";
  label: string;
  restoring?: boolean;
  leaving?: boolean;
}

export interface ScheduleRow {
  name: string;
  detail: string;
  cron: string;
  paused: boolean;
  leaving: boolean;
}

export const SCH_NEXT: Record<string, string> = {
  "0 * * * *": "top of hour",
  "0 */6 * * *": "18:00",
  "0 4 * * *": "04:00",
  "0 4 * * 0": "sun 04:00",
  "0 3 * * *": "03:00",
};

export function scheduleNext(row: ScheduleRow): string {
  return row.paused ? "paused" : (SCH_NEXT[row.cron] ?? "—");
}

// ---------------------------------------------------------------------------
// overlay state (drill-in, sheets, confirm) — the mock's class toggles as runes

export interface Origin {
  ox: string;
  oy: string;
}

function originOf(x: number, y: number): Origin {
  return {
    ox: (x / innerWidth) * 100 + "%",
    oy: (y / innerHeight) * 100 + "%",
  };
}

export type SheetId =
  | "prefs"
  | "users"
  | "specs"
  | "specEdit"
  | "nodeAdd"
  | "nodeCfg"
  | "nsForm"
  | "auditLog"
  | "apiDocs"
  | "setup";

export const ui = $state({
  // drill-in
  depthOpen: false,
  depthOrigin: { ox: "50%", oy: "50%" } as Origin,
  depthServer: null as ServerSim | null,

  // sheets — the mock allows one of each; open set + per-sheet origin
  open: {} as Partial<Record<SheetId, Origin>>,

  // confirm dialog
  confirm: null as null | { name: string; noun: string; body: string | null },

  // login / rotate / db-restart overlays
  loginOpen: false,
  loginSub: null as string | null,
  rotateOpen: false,
  dbRestartOpen: false,
});

let lastFocus: HTMLElement | null = null;
const sheetReturn = new Map<SheetId, HTMLElement | null>();

export function openSheet(id: SheetId, x: number, y: number, returnTo?: HTMLElement | null) {
  ui.open[id] = originOf(x, y);
  sheetReturn.set(id, returnTo ?? null);
}

export function closeSheet(id: SheetId) {
  delete ui.open[id];
  const back = sheetReturn.get(id);
  if (back && document.contains(back)) back.focus();
}

export function openDepth(server: ServerSim, x: number, y: number, returnTo?: HTMLElement | null) {
  if (!server.depth) return; // a card without a drill-in record is inert (mock parity)
  lastFocus = returnTo ?? null;
  ui.depthOrigin = originOf(x, y);
  ui.depthServer = server;
  ui.depthOpen = true;
}

export function surface() {
  ui.depthOpen = false;
  if (lastFocus) lastFocus.focus();
}

export function logDepthLine(html: string) {
  const d = ui.depthServer?.depth;
  if (!d) return;
  d.lines.push({
    t: new Date().toLocaleTimeString("en-US", { hour12: false }),
    html,
  });
}

// ---------------------------------------------------------------------------
// side-block actions (backups, schedules, confirm) — behavior from the mock

export function backupRestore(row: BackupRow) {
  if (row.restoring) return;
  row.restoring = true;
  logDepthLine("[Backup] restore started from " + row.label);
  setTimeout(() => {
    row.restoring = false;
    logDepthLine('<span class="good">[Backup] restore complete — world reloaded</span>');
  }, 2600);
}

export function backupDelete(row: BackupRow) {
  row.leaving = true;
  setTimeout(() => {
    sim.backups = sim.backups.filter((r) => r !== row);
  }, 250);
  logDepthLine('<span class="warn">[Backup] deleted ' + row.label + "</span>");
}

export function backupCreate() {
  if (sim.backupLive) return;
  const name = "manual-" + sim.backupSeq++;
  sim.backupLive = { name, prog: 0 };
  const timer = setInterval(() => {
    const live = sim.backupLive;
    if (!live) {
      clearInterval(timer);
      return;
    }
    live.prog = Math.min(100, live.prog + 4 + Math.random() * 6);
    if (live.prog >= 100) {
      clearInterval(timer);
      setTimeout(() => {
        sim.backups.unshift({
          kind: "done",
          label: "just now · " + name + " · 4.8G",
          restoring: false,
          leaving: false,
        });
        sim.backupLive = null;
      }, 400);
    }
  }, 220);
}

export function scheduleToggle(row: ScheduleRow) {
  row.paused = !row.paused;
  logDepthLine("[Schedule] " + (row.paused ? "paused " : "resumed ") + row.name);
}

export function scheduleDelete(row: ScheduleRow) {
  row.leaving = true;
  setTimeout(() => {
    sim.schedules = sim.schedules.filter((r) => r !== row);
  }, 250);
  logDepthLine('<span class="warn">[Schedule] deleted ' + row.name + "</span>");
}

export function scheduleAdd(row: Omit<ScheduleRow, "leaving">) {
  sim.schedules.push({ ...row, leaving: false });
  logDepthLine("[Schedule] created " + row.name + " (" + row.cron + ")");
}

// typed-confirmation dialog: the noun and warning come from whoever opened it
export const CD_SERVER_BODY =
  "this removes the world, backups and config for this server. it cannot be undone.";

let confirmReturn: HTMLElement | null = null;

export function openConfirm(
  name: string,
  returnTo: HTMLElement | null,
  opts?: { noun?: string; body?: string },
) {
  confirmReturn = returnTo;
  ui.confirm = {
    name,
    noun: opts?.noun || "server",
    body: opts?.body || CD_SERVER_BODY,
  };
}

export function closeConfirm() {
  ui.confirm = null;
  if (confirmReturn && document.contains(confirmReturn)) confirmReturn.focus();
}

export function confirmGo() {
  const c = ui.confirm;
  if (!c) return;
  if (c.noun === "spec") {
    // a spec is a recipe, not a running thing: it leaves the list and the
    // editor closes; servers built from it keep running
    specDelete(c.name);
    ui.confirm = null;
    closeSheet("specEdit");
    return;
  }
  logDepthLine(
    '<span class="warn">[Server] ' + c.name + " deleted — world, backups and config removed</span>",
  );
  const srv = sim.servers.find((s) => s.name === c.name);
  if (srv) {
    setTimeout(() => {
      sim.servers = sim.servers.filter((s) => s !== srv);
    }, 260);
  }
  ui.confirm = null;
  surface();
}

// placeholder until the specs sheet lands; confirmGo routes spec deletions here
export function specDelete(_name: string) {}

// ---------------------------------------------------------------------------
// the tick engine — cadences verbatim from the mock

export const TICK_MS = 2600;

function fmt1(n: number): string {
  return n.toFixed(1);
}

export function memLabel(t: Track, gb: number): string {
  return fmt1((t.walk.v / 100) * gb);
}

function tickInstruments() {
  for (const n of sim.nodes) {
    pushSample(n.cpu.history, stepWalk(n.cpu.walk));
    pushSample(n.mem.history, stepWalk(n.mem.walk));
    n.net.rate = n.net.base + Math.random() * n.net.span;
    n.temp.deg = Math.round(n.temp.floor + n.cpu.walk.v / n.temp.div);
  }
  for (const s of sim.servers) {
    if (!s.cpu || !s.mem) continue;
    pushSample(s.cpu.history, stepWalk(s.cpu.walk));
    pushSample(s.mem.history, stepWalk(s.mem.walk));
  }
}

function tickDepthVitals() {
  const d = ui.depthServer?.depth;
  if (!ui.depthOpen || !d || d.cpuV == null || d.memU == null || d.tickV == null) return;
  d.cpuV = Math.max(3, Math.min(95, d.cpuV + (Math.random() * 6 - 3)));
  d.memU = Math.max(0.5, Math.min(d.memM - 0.2, d.memU + (Math.random() * 0.4 - 0.2)));
  d.tickV = Math.max(18, Math.min(55, d.tickV + (Math.random() * 8 - 4)));
}

const DRIP_LINES = [
  "[Session] autosave complete",
  "[Net] keepalive ok",
  '<span class="good">[Perf] tick nominal</span>',
];
let dripIndex = 0;

function tickConsoleDrip() {
  const d = ui.depthServer?.depth;
  if (!ui.depthOpen || !d || d.state !== "running") return;
  logDepthLine(DRIP_LINES[dripIndex++ % DRIP_LINES.length]);
}

let started = false;

export function startSim() {
  if (started) return;
  started = true;
  setInterval(() => {
    sim.clock = new Date().toLocaleTimeString("en-US", { hour12: false });
  }, 1000);
  setInterval(() => {
    sim.ping = 1 + Math.floor(Math.random() * 5) + "s";
  }, 3000);
  if (reducedMotion) return;
  setInterval(tickInstruments, TICK_MS);
  setInterval(tickDepthVitals, 2200);
  setInterval(tickConsoleDrip, 4500);
}
