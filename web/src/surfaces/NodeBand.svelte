<script lang="ts">
  import DotTrack from "@/components/DotTrack.svelte";
  import PacketChan from "@/components/PacketChan.svelte";
  import Spark from "@/components/Spark.svelte";
  import TempSpec from "@/components/TempSpec.svelte";
  import { memLabel, openSheet, type NodeSim } from "@/lib/state.svelte";

  let { node }: { node: NodeSim } = $props();
</script>

<section class="node-band" aria-label={node.aria}>
  <div class="node-id">
    <span class="node-name">{node.name}</span>
    <span class="node-meta">{node.meta1}</span>
    <span class="node-meta">{node.meta2}</span>
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
        ><span>{Math.round(node.cpu.walk.v)}</span><small>%</small></span
      >
    </div>
    <div class="zone-track">
      <DotTrack track={node.cpu} />
      <span class="th t50" aria-hidden="true"></span>
      <span class="th t75" aria-hidden="true"></span>
    </div>
  </div>
  <div class="metric">
    <div class="metric-head">
      <span class="metric-label">memory</span><span class="metric-val"
        ><span>{memLabel(node.mem, node.memGb)}</span><small>/{node.memGb}G</small></span
      >
    </div>
    <div class="zone-track">
      <DotTrack track={node.mem} />
      <span class="th t50" aria-hidden="true"></span>
      <span class="th t75" aria-hidden="true"></span>
    </div>
  </div>
  <div class="metric">
    <div class="metric-head">
      <span class="metric-label">disk</span><span class="metric-val"
        >{node.disk}<small>{node.diskUnit}</small></span
      >
    </div>
    <Spark seed={node.diskSeed} />
  </div>
  <div class="metric">
    <div class="metric-head">
      <span class="metric-label">network</span><span class="metric-val"
        ><span>{node.net.rate.toFixed(1)}</span><small>Mb/s</small></span
      >
    </div>
    <PacketChan rate={node.net.rate / node.net.ref} packets={node.packets} />
  </div>
  <div class="metric">
    <div class="metric-head">
      <span class="metric-label">temp</span><span class="metric-val"
        ><span>{node.temp.deg}</span><small>°C</small></span
      >
    </div>
    <TempSpec deg={node.temp.deg} />
  </div>
</section>
