<script lang="ts">
  import DotTrack from "@/components/DotTrack.svelte";
  import { openDepth } from "@/lib/depth.svelte";
  import {
    cardTracksFor,
    chipKind,
    deadNote,
    playersLabel,
    rackOf,
    serverArt,
    serverMeta,
  } from "@/lib/views.svelte";
  import type { Server } from "@/api/types";

  // Identity, state, players, ports, and placement are real (from /servers).
  // The cpu/mem dot tracks are synthetic texture — per-server stats exist
  // only on the drill-in stream today (see the telemetry backlog issue).
  let { server }: { server: Server } = $props();

  const kind = $derived(chipKind(server.state));
  const running = $derived(kind === "run");
  const rack = $derived(rackOf(server));
  const players = $derived(playersLabel(server));
  const art = $derived(serverArt(server));
  const tracks = $derived(running ? cardTracksFor(server) : null);
  const memGb = $derived(server.memory_mb / 1024);
</script>

<button
  class="srv{running ? '' : ' is-stopped'}"
  aria-label="{server.name} — {server.state}. Open full-screen control."
  onclick={(e) => openDepth(server.id, e.clientX, e.clientY, e.currentTarget)}
>
  {#if art}
    <span class="srv-art" style="background-image: url('{art}')" aria-hidden="true"></span>
  {/if}<span class="srv-shade" aria-hidden="true"></span>

  <span class="srv-id">
    <span class="nd-rack">{rack.node} <b>{rack.host}</b></span><span class="srv-name"
      >{server.name}</span
    >
    <span class="srv-meta">{serverMeta(server)}</span>
    <span class="chip {kind}"><i></i>{server.state.replace("_", " ")}</span>
  </span>

  {#if running && tracks}
    <span class="players">
      <span class="players-num"
        >{players.num}{#if players.max}<small> / {players.max}</small>{/if}</span
      >
      <span class="cap">{#if players.pct}<i style="width: {players.pct}%"></i>{/if}</span>
      <span class="players-label">{players.num === "—" ? "players unknown" : "players online"}</span>
    </span>
    <span class="srv-chart"
      ><span class="srv-chart-label"><span>cpu</span><b>{Math.round(tracks.cpu.walk.v)}%</b></span
      ><span class="zone-track"><DotTrack track={tracks.cpu} tag="span" /><span
          class="th t50"
          aria-hidden="true"
        ></span><span class="th t75" aria-hidden="true"></span></span
      ></span
    >
    <span class="srv-chart"
      ><span class="srv-chart-label"
        ><span>memory</span><b
          ><span>{((tracks.mem.walk.v / 100) * memGb).toFixed(1)}</span> / {Math.round(memGb)}G</b
        ></span
      ><span class="zone-track"><DotTrack track={tracks.mem} tag="span" /><span
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
    <span class="dead-note" style="grid-column: span 2">{deadNote(server)}</span>
    <span class="descend"
      ><svg viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M 4 2 L 11 7 L 4 12 Z"/></svg>{server.state ===
      "offline"
        ? "start"
        : "open"}</span
    >
  {/if}
</button>
