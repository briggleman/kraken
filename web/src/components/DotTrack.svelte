<script lang="ts">
  import { istyle } from "@/lib/istyle";
  import type { Track } from "@/lib/views.svelte";

  // The house chart: one column per sample, newest last (carries .now), zone
  // classes recomputed per sample — Status Gold below 50, Caution Violet
  // 50–75 (.w), Crisis Magenta above 75 (.r). Node bands wrap this in a div
  // grammar, server cards in a span grammar (button content), so the tag is
  // a prop to keep the DOM identical to the mock.
  let {
    track,
    tag = "div",
    id = undefined,
  }: { track: Track; tag?: "div" | "span"; id?: string } = $props();
</script>

<svelte:element this={tag} class="seg-track" {id} aria-hidden="true">
  {#each track.history as v, i (i)}
    <i
      class="dotcol{v >= 75 ? ' r' : v >= 50 ? ' w' : ''}{i === track.history.length - 1
        ? ' now'
        : ''}"
      use:istyle={`--lvl:${v.toFixed(1)}%`}
    ></i>
  {/each}
</svelte:element>
