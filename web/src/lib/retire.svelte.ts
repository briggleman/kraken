// Retiring an untracked container from the node band (#354).
//
// The container-drift badge used to be a report with nothing behind it: it
// could name `kraken_f403…` and say nobody was tracking it, and the only way
// to act on that was `docker rm` on the host — after which the Agent's own
// persisted spec was still there, waiting to be adopted again. Retire is the
// action the badge was missing: the Panel asks the node to stop and remove the
// container and forget its spec; the world and config stay where they are and
// the backups are kept.
//
// It destroys nothing, so the confirmation is a plain one rather than the typed
// gate: the dialog names the container and says what stays.

import { api, errMsg } from "@/api/client";
import { refreshFleet } from "./fleet.svelte";
import { CD_CONTAINER_BODY, openConfirm } from "./state.svelte";
import { shortContainerLabel, type DriftItem } from "./views.svelte";

/** A retire the Panel refused: the reason, and the ids it was about. */
export interface RetireError {
  ids: string[];
  msg: string;
}

export const retire = $state({
  /** Server ids with a retire in flight, so a chip cannot be clicked twice. */
  busy: {} as Record<string, boolean>,
  /** The last retire failure per node, shown on its band until the next try —
   *  or until none of the containers it was about is on the band any more. */
  errors: {} as Record<string, RetireError>,
});

/** Opens the confirmation for retiring one untracked container on a node. The
 *  title carries the short name (a full `kraken_<uuid>` overflows the card);
 *  the body carries the full server id, so nothing is lost. */
export function openRetire(nodeId: string, item: DriftItem, returnTo: HTMLElement | null) {
  openConfirm(shortContainerLabel(item.label), returnTo, {
    noun: "container",
    verb: "retire",
    typed: false,
    body: `${CD_CONTAINER_BODY} server id ${item.server_id}.`,
    go: () => retireContainers(nodeId, [item.server_id]),
  });
}

/** Opens one confirmation for every untracked container a node reports — the
 *  badge's action once there are too many to name inline. */
export function openRetireAll(nodeId: string, items: DriftItem[], returnTo: HTMLElement | null) {
  openConfirm(`${items.length} untracked containers`, returnTo, {
    noun: "containers",
    verb: "retire",
    typed: false,
    body: CD_CONTAINER_BODY,
    go: () =>
      retireContainers(
        nodeId,
        items.map((i) => i.server_id),
      ),
  });
}

/**
 * Retires each container in turn and refreshes the fleet once. A failure stops
 * the run and lands on the node's band with the Panel's reason (409: the panel
 * tracks it after all, or a removal is already pending; 503: the node is away),
 * because a retire that failed silently would leave the badge looking as if
 * the click had done nothing.
 */
export async function retireContainers(nodeId: string, serverIds: string[]): Promise<void> {
  delete retire.errors[nodeId];
  try {
    for (const id of serverIds) {
      if (retire.busy[id]) continue;
      retire.busy[id] = true;
      try {
        await api.retireNodeContainer(nodeId, id);
      } finally {
        delete retire.busy[id];
      }
    }
  } catch (e) {
    retire.errors[nodeId] = {
      ids: serverIds,
      msg: errMsg(e) || "the panel refused without a reason — check the audit log",
    };
  }
  await refreshFleet();
}

/**
 * Drops a node's retire failure once none of the containers it was about is
 * still on the band — an orphan that went away by other means (removed on the
 * host, adopted back by a row) leaves no reason to keep showing why a retire of
 * it failed.
 */
export function pruneRetireError(nodeId: string, liveIds: string[]): void {
  const err = retire.errors[nodeId];
  if (!err) return;
  const live = new Set(liveIds);
  if (!err.ids.some((id) => live.has(id))) delete retire.errors[nodeId];
}
