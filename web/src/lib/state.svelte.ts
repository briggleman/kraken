// App shell state: sheet navigation, the typed-confirm dialog, the first-run
// wizard, and the synthetic instrument ticker. Server/fleet data is real
// (see fleet.svelte.ts, depth.svelte.ts, and telemetry.svelte.ts for the node
// bands); the walks that remain here cover only the server-card readouts the
// backend has no fleet-wide feed for yet.

import { stepWalk, pushSample } from "./walk";
import { allSyntheticTracks } from "./views.svelte";
import { deleteCurrentServer, surface } from "./depth.svelte";
import { fleet, refreshFleet } from "./fleet.svelte";
import { api } from "@/api/client";

export const reducedMotion =
  typeof matchMedia !== "undefined" && matchMedia("(prefers-reduced-motion: reduce)").matches;

export const TICK_MS = 2600;

// ---------------------------------------------------------------------------
// overlay state (sheets, confirm, first-run overlays)

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
  clock: new Date().toLocaleTimeString("en-US", { hour12: false }),

  // sheets — one of each; open set + per-sheet origin
  open: {} as Partial<Record<SheetId, Origin>>,

  // typed-confirmation dialog
  confirm: null as null | { name: string; noun: string; body: string | null },

  // first-run overlays (the login itself is auth-driven; these are the
  // wizard's restart choreography)
  loginOpen: false,
  loginSub: null as string | null,
  rotateOpen: false,
  dbRestartOpen: false,

  // which node / spec the nodeCfg, specEdit, and nsForm sheets are about
  nodeCfgId: null as string | null,
  specEditId: null as string | null,
  nsFormNodeId: null as string | null,
});

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

// ---------------------------------------------------------------------------
// typed-confirmation dialog: the noun and warning come from whoever opened it

export const CD_SERVER_BODY =
  "this removes the world, backups and config for this server. it cannot be undone.";

// Every word of this is true of a node and false of a server, which is the point:
// DESIGN.md requires the warning to describe the noun that opened the dialog.
// Removing a node is panel-side bookkeeping — the host is left exactly as it is.
export const CD_NODE_BODY =
  "the panel forgets this node. the agent keeps running on the host and its containers keep " +
  "running — nothing there is stopped or deleted. servers placed here lose their node, and " +
  "re-adding it needs a fresh enrollment token because this one's identity is orphaned. " +
  "it cannot be undone.";

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

export async function confirmGo() {
  const c = ui.confirm;
  if (!c) return;
  if (c.noun === "spec") {
    // a spec is a recipe, not a running thing: it leaves the list and the
    // editor closes; servers built from it keep running
    const spec = fleet.specs.find((sp) => sp.name.toLowerCase() === c.name.toLowerCase());
    ui.confirm = null;
    closeSheet("specEdit");
    if (spec) {
      try {
        await api.deleteSpec(spec.id);
        await refreshFleet();
      } catch {
        /* the audit log records the refusal; the list simply keeps the row */
      }
    }
    return;
  }
  if (c.noun === "node") {
    // Panel-side only: the agent and its containers are left running on the
    // host. Uses ui.nodeCfgId rather than matching on name — the sheet already
    // holds the id, and two nodes may legitimately share a display name.
    const id = ui.nodeCfgId;
    ui.confirm = null;
    closeSheet("nodeCfg");
    if (id) {
      try {
        await api.deleteNode(id);
        await refreshFleet();
      } catch {
        /* the audit log records the refusal; the fleet simply keeps the band */
      }
    }
    return;
  }
  const ok = await deleteCurrentServer();
  ui.confirm = null;
  if (ok) surface();
}

// ---------------------------------------------------------------------------
// first run: the forced rotation, then the four-step wizard

export const LG_SUB = "one console for every server on every node. sign in to reach it.";

export const wz = $state({
  at: 1,
  done: [false, false, false, false], // steps 1..4
  dbMode: "in-memory" as "in-memory" | "postgres",
  dbRes: null as null | { cls: "ok" | "bad"; text: string },
  resumeSetup: false,
});

export function wzMarkDone(n: number) {
  wz.done[n - 1] = true;
}

// a step you have not reached is not a place you can jump to
export function wzReachable(n: number): boolean {
  return wz.done[n - 1] || n <= wz.at + 1;
}

export function wzGo(n: number) {
  // moving forward finishes the step you are leaving; moving back does not
  if (n > wz.at) wzMarkDone(wz.at);
  wz.at = n;
  const body = document.querySelector<HTMLElement>("#setup .sheet-body");
  if (body) body.scrollTop = 0;
}

export function openSetup() {
  openSheet("setup", innerWidth / 2, innerHeight / 2, null);
  wz.at = 1;
}

// step 1 → the panel restarts onto postgres. ORDER MATTERS: the interstitial
// sits above the login (z 55 over 50), so it closes LAST and one full wipe
// later — the login opens underneath it, hidden, and the interstitial then
// wipes away to reveal a screen that is already there.
export function dbConnectRestart() {
  ui.dbRestartOpen = true;
  setTimeout(() => {
    delete ui.open.setup;
    wz.resumeSetup = true;
    ui.loginSub = "the panel is back on postgres. log in again and setup continues.";
    ui.loginOpen = true;
    setTimeout(() => (ui.dbRestartOpen = false), 700);
  }, TICK_MS);
}

// the login is the only door into any of this
export function loginGo() {
  ui.loginOpen = false;
  if (wz.resumeSetup) {
    // back from the restart: step 1 is finished and the store it was about changed
    wz.resumeSetup = false;
    ui.loginSub = null;
    wz.dbMode = "postgres";
    wz.dbRes = { cls: "ok", text: "on postgres — migrations applied." };
    wzMarkDone(1);
    openSetup();
  }
}

export function rotateDone() {
  ui.rotateOpen = false;
  // step 2 is finished by this screen, not by the wizard — so the wizard
  // opens on step 1 with Secure already struck through
  wzMarkDone(2);
  openSetup();
}

// ---------------------------------------------------------------------------
// the synthetic instrument ticker — walks for the server-card readouts, which
// have no fleet-wide feed yet. Node bands are driven by real telemetry and are
// deliberately not ticked here.

function tickInstruments() {
  for (const t of allSyntheticTracks()) {
    pushSample(t.history, stepWalk(t.walk));
  }
}

let started = false;

export function startSim() {
  if (started) return;
  started = true;
  setInterval(() => {
    ui.clock = new Date().toLocaleTimeString("en-US", { hour12: false });
  }, 1000);
  if (reducedMotion) return;
  setInterval(tickInstruments, TICK_MS);
}
