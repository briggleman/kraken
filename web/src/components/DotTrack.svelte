<script lang="ts">
  import { istyle } from "@/lib/istyle";

  // The house chart: one column per sample, newest last (carries .now), zone
  // classes recomputed per sample — Status Gold below 50, Caution Violet
  // 50–75 (.w), Crisis Magenta above 75 (.r). Node bands wrap this in a div
  // grammar, server cards in a span grammar (button content), so the tag is
  // a prop to keep the DOM identical to the mock.
  //
  // history is percentages, oldest first. A short history (a real feed still
  // filling up) simply draws fewer columns.
  let {
    history,
    tag = "div",
    id = undefined,
  }: { history: number[]; tag?: "div" | "span"; id?: string } = $props();
</script>

<svelte:element this={tag} class="seg-track" {id} aria-hidden="true">
  {#each history as v, i (i)}
    <i
      class="dotcol{v >= 75 ? ' r' : v >= 50 ? ' w' : ''}{i === history.length - 1
        ? ' now'
        : ''}"
      use:istyle={`--lvl:${v.toFixed(1)}%`}
    ></i>
  {/each}
</svelte:element>
