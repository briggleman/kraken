// Retiring an untracked container from the node band (#354).
//
// The container-drift badge used to be a report with nothing behind it: it
// could name `kraken_f403…` and say nobody was tracking it, and the only way
// to act on that was `docker rm` on the host — after which the Agent's own
// persisted spec was still there, waiting to be adopted again. Retire is the
// action the badge was missing: the Panel asks the node to stop and remove the
// container and forget its spec, and the world, config and backups stay exactly
// where they are.
//
// It destroys nothing, so the confirmation is a plain one rather than the typed
// gate: the dialog names the container and says what stays.

import { api, errMsg } from "@/api/client";
import { refreshFleet } from "./fleet.svelte";
import { CD_CONTAINER_BODY, openConfirm } from "./state.svelte";
import type { DriftItem } from "./views.svelte";

export const retire = $state({
  /** Server ids with a retire in flight, so a chip cannot be clicked twice. */
  busy: {} as Record<string, boolean>,
  /** The last retire failure per node, shown on its band until the next try. */
  errors: {} as Record<string, string>,
});

/** Opens the confirmation for retiring one untracked container on a node. */
export function openRetire(nodeId: string, item: DriftItem, returnTo: HTMLElement | null) {
  openConfirm(item.label, returnTo, {
    noun: "container",
    verb: "retire",
    typed: false,
    body: CD_CONTAINER_BODY,
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
    go: () => retireContainers(
      nodeId,
      items.map((i) => i.server_id),
    ),
  });
}

/**
 * Retires each container in turn and refreshes the fleet once. A failure stops
 * the run and lands on the node's band with the Panel's reason (409: the panel
 * tracks it after all; 503: the node is away), because a retire that failed
 * silently would leave the badge looking as if the click had done nothing.
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
    retire.errors[nodeId] =
      errMsg(e) || "the panel refused without a reason — check the audit log";
  }
  await refreshFleet();
}
