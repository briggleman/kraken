<script lang="ts">
  import DotTrack from "@/components/DotTrack.svelte";
  import PacketChan from "@/components/PacketChan.svelte";
  import Spark from "@/components/Spark.svelte";
  import TempSpec from "@/components/TempSpec.svelte";
  import { openSheet } from "@/lib/state.svelte";
  import { instrumentsFor, nodeMemLabel } from "@/lib/views.svelte";
  import type { Node } from "@/api/types";

  // Identity, memory, and status are real (from /nodes). The cpu / disk /
  // network / temp instruments are synthetic texture until the agent reports
  // node telemetry — see the backlog issue on node & fleet stats.
  let { node, index }: { node: Node; index: number } = $props();

  const inst = $derived(instrumentsFor(node, index));
  const mem = $derived(nodeMemLabel(node));
  const num = $derived("node " + String(index + 1).padStart(2, "0"));
  const statusWord = $derived(node.cordoned ? "cordoned" : node.status);
</script>

<section class="node-band" aria-label="Node {node.name} vitals">
  <div class="node-id">
    <span class="node-name">{node.name.toUpperCase()}</span>
    <span class="node-meta">{num} · {node.os}{node.wine_enabled ? " · wine" : ""} · {statusWord}</span>
    <span class="node-meta">{node.address || node.public_host || "—"}{node.agent_version ? " · agent " + node.agent_version : ""}</span>
    <span class="node-actions">
      <button
        class="prefs-open"
        aria-label="Open node settings"
        onclick={(e) => openSheet("nodeCfg", e.clientX, e.clientY, e.currentTarget)}
        >node settings</button
      >
      <button
        class="prefs-open ns-form-btn"
        aria-label="Create a new server"
        onclick={(e) => openSheet("nsForm", e.clientX, e.clientY, e.currentTarget)}
        ><svg width="12" height="12" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 7 2.4 V 11.6 M 2.4 7 H 11.6"/></svg><span class="btn-cased">New Server</span></button
      >
    </span>
  </div>
  <div class="metric">
    <div class="metric-head">
      <span class="metric-label">cpu</span><span class="metric-val"
        ><span>{Math.round(inst.cpu.walk.v)}</span><small>%</small></span
      >
    </div>
    <div class="zone-track">
      <DotTrack track={inst.cpu} />
      <span class="th t50" aria-hidden="true"></span>
      <span class="th t75" aria-hidden="true"></span>
    </div>
  </div>
  <div class="metric">
    <div class="metric-head">
      <span class="metric-label">memory</span><span class="metric-val"
        ><span>{mem.used}</span><small>/{mem.total}</small></span
      >
    </div>
    <div class="zone-track">
      <DotTrack track={inst.alloc} />
      <span class="th t50" aria-hidden="true"></span>
      <span class="th t75" aria-hidden="true"></span>
    </div>
  </div>
  <div class="metric">
    <div class="metric-head">
      <span class="metric-label">disk</span><span class="metric-val">—<small></small></span>
    </div>
    <Spark seed={inst.diskSeed} />
  </div>
  <div class="metric">
    <div class="metric-head">
      <span class="metric-label">network</span><span class="metric-val"
        ><span>{inst.net.rate.toFixed(1)}</span><small>Mb/s</small></span
      >
    </div>
    <PacketChan rate={inst.net.rate / inst.net.ref} packets={inst.packets} />
  </div>
  <div class="metric">
    <div class="metric-head">
      <span class="metric-label">temp</span><span class="metric-val"
        ><span>{inst.temp.deg}</span><small>°C</small></span
      >
    </div>
    <TempSpec deg={inst.temp.deg} />
  </div>
</section>
