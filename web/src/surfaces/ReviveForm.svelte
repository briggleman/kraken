<script lang="ts">
  import { untrack } from "svelte";
  import { istyle } from "@/lib/istyle";
  import { ui, closeSheet, sheetZ } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";
  import { hasPerm } from "@/lib/auth.svelte";
  import { fleet, refreshFleet, specOf } from "@/lib/fleet.svelte";
  import { retired } from "@/lib/retired.svelte";
  import {
    backupOptions,
    nodeOptionLabel,
    portsFree,
    retiredPortList,
    reviveBlock,
    reviveBody,
    reviveCandidates,
    type BackupOption,
  } from "@/lib/revive";
  import { api, errMsg } from "@/api/client";
  import { fmtGb, fmtSize } from "@/lib/fmt";
  import type { ScheduledTask, Server } from "@/api/types";

  // The revive sheet (DESIGN.md, Revive Sheet): the deploy sheet with what the
  // server already knows filled in, plus the one choice deploy never had —
  // which backup to restore before the first start. The sections run in the
  // order the Panel does the work: placement, restore, operations. The Panel
  // chains install → restore → start itself (202, `installing`), so there is no
  // client-side start poll: the card reads installing → restoring → offline or
  // running, and a refused start or a failed restore reads on the card's note.

  const open = $derived(!!ui.open.reviveForm);
  const server = $derived<Server | undefined>(fleet.servers.find((s) => s.id === ui.reviveServerId));
  const spec = $derived(server ? specOf(server) : undefined);
  const candidates = $derived(server ? reviveCandidates(server, fleet.nodes) : []);

  let nodeId = $state("");
  let memMb = $state("");
  let restoreId = $state("");
  let startAfter = $state(true);
  let steamGuard = $state("");
  let busy = $state(false);
  let err = $state<string | null>(null);
  // Read once per open from the node the server left: its archives (the
  // restore choice) and its schedules (the ones the retire switched off).
  let backups = $state<BackupOption[]>([]);
  let backupsNote = $state("");
  let schedules = $state<ScheduledTask[]>([]);

  // Seed on open, and only then — the fleet refreshes every few seconds with
  // fresh objects, and a tracked seed would put back what the operator changed
  // (NsForm's rule, for the same reason).
  let seededFor: string | null = null;
  $effect(() => {
    if (!open) {
      seededFor = null;
      return;
    }
    const id = ui.reviveServerId;
    if (!id || id === seededFor) return;
    seededFor = id;
    untrack(() => seed(id));
  });

  function seed(id: string) {
    const sv = fleet.servers.find((s) => s.id === id);
    if (!sv) return;
    const cands = reviveCandidates(sv, fleet.nodes);
    nodeId = cands[0]?.id ?? "";
    const sp = specOf(sv);
    memMb = String(sv.memory_mb || sp?.resources.recommended_memory_mb || sp?.resources.min_memory_mb || 0);
    restoreId = "";
    startAfter = true;
    steamGuard = "";
    err = null;
    backups = [];
    schedules = [];
    backupsNote = "";
    void loadBackups(sv);
    void api
      .listSchedules(id)
      .then((r) => {
        if (seededFor === id) schedules = (r.schedules ?? []).filter((t) => t.disabled_by_retire);
      })
      .catch(() => {});
  }

  async function loadBackups(sv: Server) {
    const oldNode = fleet.nodes.find((n) => n.id === sv.retired_from_node_id);
    if (!oldNode) {
      backupsNote = "its node is gone, and its archives went with it — it comes back as a fresh world.";
      return;
    }
    if (!hasPerm("backup.manage")) {
      backupsNote = `restoring needs the backup permission — its archives are on ${oldNode.name}.`;
      return;
    }
    // Not asked of a node the fleet already reads offline: the answer would
    // only be a dial error, and the node band already says the node is away.
    if (oldNode.status === "offline") {
      backupsNote = `its archives are on ${oldNode.name}, which is offline — they can be restored once it is back.`;
      return;
    }
    try {
      const r = await api.listBackups(sv.id);
      if (seededFor !== sv.id) return;
      backups = backupOptions(r.backups ?? []);
      // the latest archive is the default: revive is for getting the world back
      restoreId = backups[0]?.id ?? "";
      if (backups.length === 0) backupsNote = `no ready backups on ${oldNode.name} — it comes back as a fresh world.`;
    } catch (e) {
      if (seededFor !== sv.id) return;
      backupsNote = `its backups on ${oldNode.name} could not be read (${errMsg(e)}) — it comes back as a fresh world.`;
    }
  }

  const node = $derived(candidates.find((n) => n.id === nodeId));
  const onOldNode = $derived(!!server && nodeId === server.retired_from_node_id);
  // An archive lives on the node it was taken on: a revive elsewhere cannot
  // restore it (the Panel answers backup_not_found), so the restore goes back
  // to none and the sheet says why.
  $effect(() => {
    if (!onOldNode && restoreId) restoreId = "";
  });
  const chosenBackup = $derived(backups.find((b) => b.id === restoreId));

  const oldPorts = $derived(server ? retiredPortList(server) : []);
  const portsAreFree = $derived(portsFree(oldPorts, node));

  const minMb = $derived(spec?.resources.min_memory_mb ?? 0);
  const chosenMb = $derived(Number.isFinite(+memMb) && +memMb > 0 ? Math.round(+memMb) : server?.memory_mb ?? 0);
  const belowMin = $derived(chosenMb < minMb);
  const memAfter = $derived(node ? node.allocated_memory_mb + chosenMb : chosenMb);
  const overCapacity = $derived(!!node && memAfter > node.total_memory_mb);

  const blocked = $derived(server ? reviveBlock(server, spec, fleet.nodes, node) : "");
  const mayRevive = $derived(hasPerm("server.create"));

  async function revive() {
    if (!server || busy || blocked) return;
    if (belowMin) {
      err = `memory must be at least the spec's minimum of ${fmtGb(minMb)}G`;
      return;
    }
    busy = true;
    err = null;
    const id = server.id;
    try {
      await api.reviveServer(
        id,
        reviveBody(server, {
          nodeId,
          memoryMb: chosenMb,
          restoreId: onOldNode ? restoreId : "",
          start: startAfter,
          steamGuard,
        }),
      );
      delete retired.keep[id];
      closeSheet("reviveForm");
      await refreshFleet();
    } catch (e) {
      err = errMsg(e);
    } finally {
      busy = false;
    }
  }
</script>

<div
  class="sheet"
  class:open
  id="reviveForm"
  role="dialog"
  aria-modal="true"
  aria-labelledby="reviveFormTitle"
  use:istyle={`--ox: ${ui.open.reviveForm?.ox ?? "50%"}; --oy: ${ui.open.reviveForm?.oy ?? "50%"}; z-index: ${sheetZ("reviveForm")}`}
  use:sheetFocus
>
  <div class="depth-head">
    <button class="surface-btn" onclick={() => closeSheet("reviveForm")}>
      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6 10 V 2 M 2 6 L 6 2 L 10 6"/></svg>
      surface
    </button>
    <h2 class="depth-title" id="reviveFormTitle">revive <b class="node-name-inline">{server?.name ?? "—"}</b></h2>
    <div class="prefs-note"></div>
  </div>
  <div class="sheet-body ns-body">
    <section class="prefs-group" aria-label="Revive settings">
      <div class="cfg-head">
        <h3 class="pane-label">back the way it was, or not</h3>
        <span class="cfg-badge env">config kept from before it was retired</span>
        {#if portsAreFree}<span class="cfg-badge ok">old ports free</span>{/if}
      </div>
      <div class="ns-pad ns-grid">
        <div class="ns-legend"><h4>placement</h4><i></i><small>its old node first; any node that can host the spec is offered</small></div>
        <div class="cfg-row">
          <span>node</span>
          <select class="cfg-in has-badge" aria-label="Node" bind:value={nodeId} disabled={candidates.length === 0}>
            <button><selectedcontent></selectedcontent>{#if onOldNode}<span class="cfg-badge env">previous</span>{/if}</button>
            {#each candidates as n (n.id)}
              <option value={n.id}>{nodeOptionLabel(n)}</option>
            {:else}
              <option value="">no node can run it</option>
            {/each}
          </select>
        </div>
        {#if oldPorts.length > 0}
          <div class="cfg-row">
            <span>ports</span>
            <span class="cfg-ro has-badge" role="status" aria-label="Ports: {oldPorts.join(' and ')}{portsAreFree ? ', the ones it had before' : ''}"
              ><span>{oldPorts.join(" · ")}</span>{#if portsAreFree}<span class="cfg-badge env">previous</span>{/if}</span
            >
            {#if !portsAreFree}
              <p class="cfg-help">the ones it had before — any already taken on {node?.name ?? "this node"} fall back to the spec's default, then the pool's lowest free port.</p>
            {/if}
          </div>
        {/if}
        <div class="cfg-row">
          <span>memory (mb)</span>
          <input type="text" class="cfg-in" bind:value={memMb} aria-label="Memory in MB" />
          <p class="cfg-help">
            what it had before{spec ? ` · spec minimum ${spec.resources.min_memory_mb}MB` : ""}{spec?.resources.recommended_memory_mb
              ? `, recommended ${spec.resources.recommended_memory_mb}MB`
              : ""}.
          </p>
        </div>

        <div class="ns-legend"><h4>restore</h4><i></i><small>after the install lands, before anything starts</small></div>
        <div class="cfg-row ns-wide">
          <span>from backup</span>
          <select class="cfg-in" aria-label="Backup to restore" bind:value={restoreId} disabled={!onOldNode || backups.length === 0}>
            {#if onOldNode}
              {#each backups as b (b.id)}
                <option value={b.id}>{b.label}</option>
              {/each}
            {/if}
            <option value="">none — a fresh world</option>
          </select>
          {#if !onOldNode && server}
            <p class="cfg-help">its archives are on {fleet.nodes.find((n) => n.id === server.retired_from_node_id)?.name ?? "the node it left"} — revived elsewhere, it starts from a fresh world.</p>
          {:else if backupsNote}
            <p class="cfg-help">{backupsNote}</p>
          {/if}
        </div>

        <div class="ns-legend"><h4>operations</h4><i></i><small>its schedules come back with it</small></div>
        <label class="tgl"><input type="checkbox" bind:checked={startAfter} /><i></i>start once the install and restore finish</label>
        <!-- The mock draws a "re-enable its schedules" toggle; the Panel has no
             such choice — a revive switches back on every schedule the retire
             switched off, once the install lands. So it is said, not asked. -->
        <p class="cfg-help">
          {#if schedules.length > 0}
            its schedules come back on once the install lands: {schedules.map((t) => `${t.name} (${t.cron})`).join(", ")}.
          {:else}
            it had no schedules for the retire to switch off.
          {/if}
        </p>
        {#if spec?.install?.requires_steam_login}
          <div class="cfg-row">
            <span>steam guard code — this game needs a steam login to install</span>
            <input type="text" class="cfg-in" bind:value={steamGuard} autocomplete="off" spellcheck="false" aria-label="Steam guard code" />
          </div>
        {/if}
      </div>
      <div class="ns-alloc">
        <div class="ns-cost" class:over={overCapacity}><span>memory after</span><b>{fmtGb(memAfter)}<em>/{node ? Math.round(node.total_memory_mb / 1024) : "—"}G</em></b></div>
        <div class="ns-cost" class:over={belowMin}><span>memory</span><b>{fmtGb(chosenMb)}<em>G</em></b></div>
        <div class="ns-cost"><span>restore</span><b>{#if chosenBackup}{fmtSize(chosenBackup.bytes)}{:else}none{/if}<em></em></b></div>
        <div class="ns-acts">
          {#if err}<span class="cfg-note" role="alert" use:istyle={"color: var(--crisis)"}>{err}</span>
          {:else if blocked}<span class="cfg-note" role="status" use:istyle={"color: var(--crisis)"}>{blocked}</span>
          {:else if !mayRevive}<span class="cfg-note">reviving needs the server create permission</span>
          {:else if belowMin}<span class="cfg-note" use:istyle={"color: var(--crisis)"}
            >below the spec's {fmtGb(minMb)}G minimum — the game will not boot</span
          >
          {:else if overCapacity && node}<span class="cfg-note" use:istyle={"color: var(--caution)"}
            >{node.name} has {fmtGb(node.total_memory_mb - node.allocated_memory_mb)}G free — this needs {fmtGb(chosenMb)}G</span
          >{/if}
          <button class="cfg-btn ghost" onclick={() => closeSheet("reviveForm")}>cancel</button>
          <button class="cfg-btn solid" disabled={busy || !!blocked || !mayRevive || !server} onclick={() => void revive()}
            >{busy ? "reviving…" : "revive server"}</button
          >
        </div>
      </div>
    </section>
  </div>
</div>
