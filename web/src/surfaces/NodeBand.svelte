<script lang="ts">
  import DotTrack from "@/components/DotTrack.svelte";
  import PacketChan from "@/components/PacketChan.svelte";
  import Spark from "@/components/Spark.svelte";
  import TempSpec from "@/components/TempSpec.svelte";
  import { onDestroy } from "svelte";
  import { api } from "@/api/client";
  import { hasPerm } from "@/lib/auth.svelte";
  import { refreshFleet } from "@/lib/fleet.svelte";
  import { fmtCapacityMB } from "@/lib/fmt";
  import { openSheet, ui } from "@/lib/state.svelte";
  import { TELEMETRY_HISTORY, netMbps, vitalsFor } from "@/lib/telemetry.svelte";
  import { agentDrift, containerDrift, nodeMemLabel } from "@/lib/views.svelte";
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
  // The padlock beside the name carries "locked", so the meta line must not say it
  // too — the same fact twice in one cell. A cordoned node is reachable and
  // healthy, so it reads "online" here and the glyph supplies the condition.
  const statusWord = $derived(node.cordoned && node.status === "cordoned" ? "online" : node.status);

  // The agent build this node is behind, or undefined when it is current. Only
  // node.manage may push a binary, so a viewer sees the fact without the action —
  // the drift is information about the fleet either way.
  const drift = $derived(agentDrift(node));
  const containers = $derived(containerDrift(node));

  // One state at a time, named, so the CSS can grant the sweep to the two idle
  // states rather than revoke it from the three busy ones. "rest" and "ahead"
  // both mean nothing is in flight; the difference is only which word the chip
  // uses, because "update" on a node whose agent is NEWER than the panel would
  // be describing a downgrade.
  type ChipState = "rest" | "ahead" | "pushing" | "restarting" | "failed";

  let phase = $state<"idle" | "pushing" | "restarting" | "failed">("idle");
  let pushErr = $state("");

  // A failed-and-reverted update reports itself through the node record, not
  // through a job: the agent rolls back, and the reason arrives as
  // last_update_error on the next reconcile. It was plumbed all the way to the
  // web types and rendered nowhere, so a push that died AFTER the restart was
  // invisible. An in-flight job wins the line while it lasts; otherwise this is
  // the failure worth showing. It clears server-side on the next successful
  // update, so there is nothing to dismiss.
  const rollbackErr = $derived(phase === "idle" ? (node.last_update_error ?? "") : "");
  const failure = $derived(pushErr || rollbackErr);

  const chipState = $derived<ChipState>(
    phase === "pushing"
      ? "pushing"
      : phase === "restarting"
        ? "restarting"
        : failure
          ? "failed"
          : drift?.direction === "ahead"
            ? "ahead"
            : "rest",
  );

  // The chip says what clicking it DOES. "failed" would name the state a second
  // time — the reason on the line already does that — and offer nothing.
  const chipLabel = $derived(
    chipState === "pushing"
      ? "pushing…"
      : chipState === "restarting"
        ? "restarting…"
        : chipState === "failed"
          ? "retry"
          : chipState === "ahead"
            ? "match panel"
            : "update",
  );

  // The POST answers 202 and the Panel streams ~17MB in the background, so the
  // outcome arrives by polling the job rather than by awaiting the request. The
  // job ends at `restarting` — the agent has taken the binary and is rebooting
  // itself — and the drift line then clears on its own once the node reports the
  // new version, which is the actual success signal. A 404 means this Panel has
  // no job for the node (it restarted, or the job aged out): stop polling and
  // trust the node record. This deliberately keeps the current label swap; the
  // in-flight and failure DESIGN is #159, which will consume bytes_sent.
  let pollTimer: ReturnType<typeof setInterval> | undefined;

  function stopPoll() {
    clearInterval(pollTimer);
    pollTimer = undefined;
  }

  onDestroy(stopPoll);

  async function pushAgent() {
    if (phase === "pushing") return;
    phase = "pushing";
    pushErr = "";
    try {
      await api.updateNodeAgent(node.id);
    } catch (e) {
      // Preflight is still synchronous, so this is a real refusal worth showing
      // as-is: already current, no embedded binary, agent unreachable.
      pushErr = e instanceof Error ? e.message : String(e);
      phase = "failed";
      return;
    }
    stopPoll();
    pollTimer = setInterval(() => void pollJob(), 2000);
  }

  async function pollJob() {
    let job;
    try {
      job = await api.agentUpdateStatus(node.id);
    } catch {
      // 404 (or any read failure): the job is not knowable here. The node
      // record is authoritative either way.
      stopPoll();
      phase = "idle";
      await refreshFleet();
      return;
    }
    if (job.phase === "pushing") return;
    stopPoll();
    if (job.phase === "failed") {
      pushErr = job.error || "the agent refused the update";
      phase = "failed";
    } else {
      // restarting: the agent has the binary and is rebooting into it. Hold the
      // state rather than inventing a timeout — the drift line dissolving when
      // the node reports the new version IS the success signal, and a rollback
      // arrives as last_update_error. Both come from the node record.
      phase = "restarting";
    }
    await refreshFleet();
  }
</script>

<section class="node-band" aria-label="Node {node.name} vitals">
  <div class="node-id">
    <span class="node-name-row">
      <span class="node-name">{node.name.toUpperCase()}</span>
      {#if node.cordoned}
        <!-- An indicator, never a control: its job is that a node you locked and
             forgot cannot look like one you did not. -->
        <span
          class="node-lock"
          title="locked — the scheduler places no new servers here"
          aria-label="{node.name} is locked"
          ><svg width="21" height="21" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3.5" y="7" width="9" height="6" rx="1.2"/><path d="M 5.6 7 V 5.4 A 2.4 2.4 0 0 1 10.4 5.4 V 7"/></svg></span
        >
      {/if}
    </span>
    <span class="node-meta">{num} · {node.os}{node.wine_enabled ? " · wine" : ""} · {statusWord}</span>
    <!-- the agent version rides the address line while it is merely a fact; once the
         panel has outrun it, it moves to the drift line below rather than printing twice -->
    <span class="node-meta">{node.address || node.public_host || "—"}{node.agent_version && !drift ? " · agent " + node.agent_version : ""}</span>
    {#if drift}
      <span class="node-meta node-cond agent-drift st-{chipState}">
        <span class="nc-k">agent</span><b class="nc-v">{drift.from}</b><span class="nc-sep" aria-hidden="true">→</span><b class="nc-v act">{drift.to}</b>
        {#if failure}
          <!-- the reason takes the line's one act colour; .st-failed hands it over
               from the target version, because the reason is the thing to act on -->
          <b class="nc-v nc-fail" title={failure}>{failure}</b>
        {/if}
        {#if hasPerm("node.manage")}
          <button
            class="nc-go ad-go"
            disabled={chipState === "pushing" || chipState === "restarting"}
            title={failure ||
              (drift.direction === "ahead"
                ? `downgrade the agent on ${node.name} to the panel's ${drift.to}`
                : `push the panel's ${drift.to} agent to ${node.name} — it restarts itself`)}
            aria-label="{drift.direction === 'ahead' ? 'Downgrade' : 'Update'} the agent on {node.name} from {drift.from} to {drift.to}"
            onclick={() => void pushAgent()}>{chipLabel}</button
          >
        {/if}
      </span>
    {/if}
    {#if containers}
      <!-- The one comparison that runs from the agent's own count inward. A
           surplus is holding memory and ports the scheduler believes are free. -->
      <span class="node-meta node-cond container-drift">
        <span class="nc-k">containers</span><b class="nc-v">{containers.running} running</b><span class="nc-sep" aria-hidden="true">·</span><b class="nc-v act">{containers.delta} {containers.word}</b>
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
