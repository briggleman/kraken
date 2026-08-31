<script lang="ts">
  import DotTrack from "@/components/DotTrack.svelte";
  import PacketChan from "@/components/PacketChan.svelte";
  import Spark from "@/components/Spark.svelte";
  import TempSpec from "@/components/TempSpec.svelte";
  import { api } from "@/api/client";
  import { hasPerm } from "@/lib/auth.svelte";
  import { refreshFleet } from "@/lib/fleet.svelte";
  import { fmtCapacityMB } from "@/lib/fmt";
  import { openSheet, ui } from "@/lib/state.svelte";
  import { TELEMETRY_HISTORY, netMbps, vitalsFor } from "@/lib/telemetry.svelte";
  import { agentDrift, nodeMemLabel } from "@/lib/views.svelte";
  import type { Node } from "@/api/types";

  // cpu, disk, network and temp are live host readings from the node's agent
  // (see lib/telemetry); memory is the scheduler's commitment from the node
  // record. A metric the host can't report — no thermal sensor, an agent too
  // old for the telemetry RPC, a node the panel can't reach — reads as an
  // em-dash with its chart blanked, never as a zero.
  let { node, index }: { node: Node; index: number } = $props();

  const vitals = $derived(vitalsFor(node.id));
  const now = $derived(vitals?.now);

  const cpu = $derived(now?.cpu_known ? now.cpu_percent : undefined);
  const temp = $derived(now?.temp_known ? now.temp_celsius : undefined);
  const mbps = $derived(netMbps(now));

  // Memory is the scheduler's commitment, not host usage: it answers "will the
  // next server fit". It comes from the node record, so it reads true even for
  // a node whose agent is unreachable.
  const mem = $derived(nodeMemLabel(node));

  // The packet channel's tempo is throughput against this node's own observed
  // peak; there is no link speed to normalize against.
  const netRate = $derived(
    mbps === undefined ? 0 : mbps / Math.max(vitals?.netPeakMbps ?? 0, 1),
  );

  const num = $derived("node " + String(index + 1).padStart(2, "0"));
  const statusWord = $derived(node.cordoned ? "cordoned" : node.status);

  // The agent build this node is behind, or undefined when it is current. Only
  // node.manage may push a binary, so a viewer sees the fact without the action —
  // the drift is information about the fleet either way.
  const drift = $derived(agentDrift(node));

  let pushing = $state(false);
  let pushErr = $state("");

  // The push returns 202 and the agent restarts itself onto the new build, so
  // there is nothing to confirm: the node drops and returns, and the drift line
  // disappears on its own once it reports the new version. Refresh immediately so
  // that happens as soon as it is true rather than at the next poll tick.
  async function pushAgent() {
    if (pushing) return;
    pushing = true;
    pushErr = "";
    try {
      await api.updateNodeAgent(node.id);
      await refreshFleet();
    } catch (e) {
      pushErr = e instanceof Error ? e.message : String(e);
    } finally {
      pushing = false;
    }
  }
</script>

<section class="node-band" aria-label="Node {node.name} vitals">
  <div class="node-id">
    <span class="node-name">{node.name.toUpperCase()}</span>
    <span class="node-meta">{num} · {node.os}{node.wine_enabled ? " · wine" : ""} · {statusWord}</span>
    <!-- the agent version rides the address line while it is merely a fact; once the
         panel has outrun it, it moves to the drift line below rather than printing twice -->
    <span class="node-meta">{node.address || node.public_host || "—"}{node.agent_version && !drift ? " · agent " + node.agent_version : ""}</span>
    {#if drift}
      <span class="node-meta agent-drift">
        <span class="ad-k">agent</span><b class="ad-v">{drift.from}</b><span class="ad-arw" aria-hidden="true">→</span><b class="ad-v new">{drift.to}</b>
        {#if hasPerm("node.manage")}
          <button
            class="ad-go"
            disabled={pushing}
            title={pushErr || `push the panel's ${drift.to} agent to ${node.name} — it restarts itself`}
            aria-label="Update the agent on {node.name} from {drift.from} to {drift.to}"
            onclick={() => void pushAgent()}>{pushing ? "pushing…" : pushErr ? "failed" : "update"}</button
          >
        {/if}
      </span>
    {/if}
    <span class="node-actions">
      <button
        class="prefs-open"
        aria-label="Open node settings"
        onclick={(e) => {
          ui.nodeCfgId = node.id;
          openSheet("nodeCfg", e.clientX, e.clientY, e.currentTarget);
        }}
        >node settings</button
      >
      <button
        class="prefs-open ns-form-btn"
        aria-label="Create a new server"
        onclick={(e) => {
          ui.nsFormNodeId = node.id;
          openSheet("nsForm", e.clientX, e.clientY, e.currentTarget);
        }}
        ><svg width="12" height="12" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 7 2.4 V 11.6 M 2.4 7 H 11.6"/></svg><span class="btn-cased">New Server</span></button
      >
    </span>
  </div>
  <div class="metric">
    <div class="metric-head">
      <span class="metric-label">cpu</span><span class="metric-val"
        >{#if cpu === undefined}—<small></small>{:else}<span>{Math.round(cpu)}</span><small>%</small>{/if}</span
      >
    </div>
    <div class="zone-track">
      <DotTrack history={vitals?.cpu ?? []} />
      <span class="th t50" aria-hidden="true"></span>
      <span class="th t75" aria-hidden="true"></span>
    </div>
  </div>
  <div class="metric">
    <div class="metric-head">
      <span class="metric-label">memory</span><span class="metric-val"
        >{#if mem.used}<span>{mem.used}</span><small>/{mem.total}</small>{:else}—<small
          ></small>{/if}</span
      >
    </div>
    <div class="zone-track">
      <DotTrack history={vitals?.alloc ?? []} />
      <span class="th t50" aria-hidden="true"></span>
      <span class="th t75" aria-hidden="true"></span>
    </div>
  </div>
  <div class="metric">
    <div class="metric-head">
      <span class="metric-label">disk</span><span class="metric-val"
        >{#if now?.disk_known}<span>{fmtCapacityMB(now.disk_used_mb)}</span><small
            >/{fmtCapacityMB(now.disk_total_mb)}</small
          >{:else}—<small></small>{/if}</span
      >
    </div>
    <Spark values={vitals?.disk ?? []} capacity={TELEMETRY_HISTORY} flat={!now?.disk_known} />
  </div>
  <div class="metric">
    <div class="metric-head">
      <span class="metric-label">network</span><span class="metric-val"
        >{#if mbps === undefined}—<small></small>{:else}<span>{mbps.toFixed(1)}</span><small
            >Mb/s</small
          >{/if}</span
      >
    </div>
    <PacketChan rate={netRate} variant={index} unknown={mbps === undefined} />
  </div>
  <div class="metric">
    <div class="metric-head">
      <span class="metric-label">temp</span><span class="metric-val"
        >{#if temp === undefined}—<small></small>{:else}<span>{Math.round(temp)}</span><small
            >°C</small
          >{/if}</span
      >
    </div>
    <TempSpec deg={temp ?? 0} unknown={temp === undefined} />
  </div>
</section>
