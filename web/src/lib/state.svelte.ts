// App shell state: sheet navigation, the typed-confirm dialog, the first-run
// wizard, and the synthetic instrument ticker. Server/fleet data is real
// (see fleet.svelte.ts, depth.svelte.ts, and telemetry.svelte.ts for the node
// bands); the walks that remain here cover only the server-card readouts the
// backend has no fleet-wide feed for yet.

import { stepWalk, pushSample } from "./walk";
import { allSyntheticTracks } from "./views.svelte";
import { retireCurrentServer, surface } from "./depth.svelte";
import { fleet, refreshFleet } from "./fleet.svelte";
import { api, errMsg } from "@/api/client";

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
  | "reviveForm"
  | "auditLog"
  | "apiDocs"
  | "setup";

export const ui = $state({
  clock: new Date().toLocaleTimeString("en-US", { hour12: false }),

  // sheets — one of each; open set + per-sheet origin
  open: {} as Partial<Record<SheetId, Origin>>,

  // z-order of the sheets, bottom → top. Every sheet element sits at the same
  // CSS z-index, so DOM order — fixed at authoring time — used to decide which
  // of two open sheets was visible: a spec editor opened FROM the new-server
  // sheet rendered underneath it. Opening a sheet moves it to the top of this
  // stack and sheetZ() turns the position into an inline z-index. Closed
  // sheets keep their slot (they are visibility:hidden, and holding the slot
  // keeps the z stable through the closing wipe) — opening dedupes, so the
  // stack never grows past the number of sheet ids.
  stack: [] as SheetId[],

  // typed-confirmation dialog
  confirm: null as null | {
    name: string;
    noun: string;
    body: string | null;
    // The consequence, when the opener owns it (a file, a folder). Without it
    // the dialog falls through to the noun branches in confirmGo.
    go?: () => Promise<void> | void;
    // What the confirm button does ("delete" unless the opener says otherwise),
    // and whether the word must be typed first. Typing is for what cannot be
    // undone; an opener whose consequence destroys nothing (retiring an
    // untracked container) passes typed: false and gets a plain confirm.
    verb: string;
    typed: boolean;
  },

  // Why the last spec delete was refused (a server still uses the spec, say),
  // for the spec editor to show; it stays open when the delete does not happen.
  specError: null as string | null,

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
  // which retired server the reviveForm sheet is bringing back (#360)
  reviveServerId: null as string | null,
});

const sheetReturn = new Map<SheetId, HTMLElement | null>();

export function openSheet(id: SheetId, x: number, y: number, returnTo?: HTMLElement | null) {
  ui.open[id] = originOf(x, y);
  const at = ui.stack.indexOf(id);
  if (at >= 0) ui.stack.splice(at, 1);
  ui.stack.push(id);
  sheetReturn.set(id, returnTo ?? null);
}

// SHEET_Z matches the .sheet z-index in house.css. The stack is capped at one
// entry per sheet id, so the topmost sheet stays below the typed-confirm
// dialog and the sftp overlay (z 50+).
const SHEET_Z = 35;

export function sheetZ(id: SheetId): number {
  const at = ui.stack.indexOf(id);
  return at < 0 ? SHEET_Z : SHEET_Z + at;
}

export function closeSheet(id: SheetId) {
  delete ui.open[id];
  const back = sheetReturn.get(id);
  if (back && document.contains(back)) back.focus();
}

// ---------------------------------------------------------------------------
// typed-confirmation dialog: the noun and warning come from whoever opened it

// Backups are named because they are NOT taken: they are kept (on the node, a
// share or a mirror — wherever the target puts them), and a
// warning that claimed otherwise (it did, until #354) is how a surviving archive
// came to look like another server's. The button retires the server now
// (#360), and the retire takes a final backup before the world goes — which
// the warning says first, because it is what makes the rest recoverable.
//
// Two sentences, because the mock writes two: the drill-in's danger note says
// what retiring does ("retiring stops this server…"), and the typed confirmation
// repeats it and adds where the server goes ("…from the retired list"). Each
// follows the retire block's `take a final backup first` toggle, because the
// no-backup retire must not claim a backup it will not take — and neither
// says "it cannot be undone" (the Cannot-Be-Undone Rule): a retire keeps every
// backup and can be revived.
export const CD_SERVER_BODY =
  "this stops the server, takes a final backup, then removes its world and config from the node. " +
  "its backups are kept, and it can be revived later from the retired list.";
export const CD_SERVER_BODY_NO_BACKUP =
  "this stops the server, then removes its world and config from the node — no final backup is taken, " +
  "so anything since its last backup is lost. its backups are kept, and it can be revived later from the retired list.";

/** The typed confirmation's warning for a retire, as the toggle stands. */
export function retireConfirmBody(finalBackup: boolean): string {
  return finalBackup ? CD_SERVER_BODY : CD_SERVER_BODY_NO_BACKUP;
}

/** The retire block's danger note, as the toggle stands. */
export function retireNote(finalBackup: boolean): string {
  return finalBackup
    ? "retiring stops this server, takes a final backup, then removes its world and config from the node. " +
        "its backups are kept, and it can be revived later from any of them."
    : "retiring stops this server, then removes its world and config from the node without a final backup. " +
        "its existing backups are kept, and it can be revived later from one of them or as a fresh world.";
}

// Deleting a retired server for good (#360): the one lifecycle act that is
// irreversible, and so the one place "cannot be undone" is spent (the
// Cannot-Be-Undone Rule). Undesigned copy, inherited from the Panel's DELETE:
// the row, its schedules and its own archives go; archives on a shared target
// are the target's and are kept, and the Panel says so in its answer.
export const CD_PURGE_BODY =
  "this deletes the retired server for good: its row, its schedules and its own archives go. " +
  "archives on a shared backup target are kept. it cannot be undone.";

// Retiring an untracked container: the one confirmation that destroys nothing,
// which is why it is not typed. The container goes; everything it was using
// stays exactly where it is.
export const CD_CONTAINER_BODY =
  "the node stops and removes this container and forgets it, so its watchdog never brings it " +
  "back. the panel has no server for it. its world and config stay on the node untouched, and " +
  "its backups are kept.";

// Every word of this is true of a node and false of a server, which is the point:
// DESIGN.md requires the warning to describe the noun that opened the dialog.
// Removing a node is panel-side bookkeeping — the host is left exactly as it is.
export const CD_NODE_BODY =
  "the panel forgets this node. the agent keeps running on the host and its containers keep " +
  "running — nothing there is stopped or deleted. servers placed here lose their node, and " +
  "re-adding it needs a fresh enrollment token because this one's identity is orphaned. " +
  "it cannot be undone.";

// A file or folder in the Files tab. The agent removes it from the host data
// dir with no trash, so the warning says exactly that and nothing about worlds
// or backups — those are what a *server* delete takes.
export const CD_FILE_BODY =
  "this removes the file from the server's data on the node. there is no trash. it cannot be undone.";
export const CD_FOLDER_BODY =
  "this removes the folder and everything inside it from the server's data on the node. " +
  "there is no trash. it cannot be undone.";

let confirmReturn: HTMLElement | null = null;

export function openConfirm(
  name: string,
  returnTo: HTMLElement | null,
  opts?: {
    noun?: string;
    body?: string;
    go?: () => Promise<void> | void;
    verb?: string;
    typed?: boolean;
  },
) {
  confirmReturn = returnTo;
  ui.confirm = {
    name,
    noun: opts?.noun || "server",
    body: opts?.body || CD_SERVER_BODY,
    go: opts?.go,
    verb: opts?.verb || "delete",
    typed: opts?.typed ?? true,
  };
}

/** The word a typed confirmation asks for: its verb. */
export function confirmWord(c: { verb?: string } | null | undefined): string {
  return (c?.verb || "delete").toLowerCase();
}

export function closeConfirm() {
  ui.confirm = null;
  if (confirmReturn && document.contains(confirmReturn)) confirmReturn.focus();
}

export async function confirmGo() {
  const c = ui.confirm;
  if (!c) return;
  if (c.go) {
    // The opener owns the consequence; the dialog only gates it. It closes
    // before the call so a slow agent leaves the pane, not the veil, as the
    // thing reporting back — and focus goes home only if home still exists
    // (a deleted row's button does not).
    ui.confirm = null;
    await c.go();
    if (confirmReturn && document.contains(confirmReturn)) confirmReturn.focus();
    return;
  }
  if (c.noun === "spec") {
    // a spec is a recipe, not a running thing: it leaves the list and the
    // editor closes; servers built from it keep running
    const spec = fleet.specs.find((sp) => sp.name.toLowerCase() === c.name.toLowerCase());
    ui.confirm = null;
    ui.specError = null;
    if (!spec) {
      closeSheet("specEdit");
      return;
    }
    try {
      await api.deleteSpec(spec.id);
      closeSheet("specEdit");
      await refreshFleet();
    } catch (e) {
      // The refusal is the operator's next step ("2 servers use this spec (1
      // retired) — revive or delete them for good first"): the editor stays
      // open and says it.
      ui.specError = errMsg(e);
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
  const ok = await retireCurrentServer();
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
