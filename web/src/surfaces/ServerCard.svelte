<script lang="ts">
  import DotTrack from "@/components/DotTrack.svelte";
  import { memLabel, openDepth, type ServerSim } from "@/lib/state.svelte";

  let { server }: { server: ServerSim } = $props();

  const running = $derived(server.state === "running");
</script>

<button
  class="srv{running ? '' : ' is-stopped'}"
  aria-label="{server.name} — {server.state}. Open full-screen control."
  onclick={(e) => openDepth(server, e.clientX, e.clientY, e.currentTarget)}
>
  <span class="srv-art" style="background-image: url('{server.art}')" aria-hidden="true"
  ></span><span class="srv-shade" aria-hidden="true"></span>

  <span class="srv-id">
    <span class="nd-rack">{server.rackNode} <b>{server.rackHost}</b></span><span
      class="srv-name">{server.name}</span
    >
    <span class="srv-meta">{server.meta}</span>
    {#if running}
      <span class="chip run"><i></i>running</span>
    {:else}
      <span class="chip stop"><i></i>stopped</span>
    {/if}
  </span>

  {#if running && server.cpu && server.mem}
    <span class="players">
      <span class="players-num">{server.playersNum}<small> / {server.playersMax}</small></span>
      <span class="cap"><i style="width: {server.capPct}%"></i></span>
      <span class="players-label">players online</span>
    </span>
    <span class="srv-chart"
      ><span class="srv-chart-label"><span>cpu</span><b>{Math.round(server.cpu.walk.v)}%</b></span
      ><span class="zone-track"><DotTrack track={server.cpu} tag="span" /><span
          class="th t50"
          aria-hidden="true"
        ></span><span class="th t75" aria-hidden="true"></span></span
      ></span
    >
    <span class="srv-chart"
      ><span class="srv-chart-label"
        ><span>memory</span><b
          ><span>{memLabel(server.mem, server.memGb!)}</span> / {server.memGb}G</b
        ></span
      ><span class="zone-track"><DotTrack track={server.mem} tag="span" /><span
          class="th t50"
          aria-hidden="true"
        ></span><span class="th t75" aria-hidden="true"></span></span
      ></span
    >
    <span class="descend"
      ><svg viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M 2 5 L 7 10 L 12 5"/></svg>open</span
    >
  {:else}
    <span class="players">
      <span class="players-num">—</span>
      <span class="cap"></span>
      <span class="players-label">offline</span>
    </span>
    <span class="dead-note" style="grid-column: span 2">{server.deadNote}</span>
    <span class="descend"
      ><svg viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M 4 2 L 11 7 L 4 12 Z"/></svg>start</span
    >
  {/if}
</button>
