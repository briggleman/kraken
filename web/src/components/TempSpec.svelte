<script lang="ts">
  import { istyle } from "@/lib/istyle";
  // Heat spectrum fill: the full thermal gradient as a ghost, solid fill
  // clipped to the live value on a 20–90°C scale, cool/warm/hot beneath.
  let { id = undefined, deg }: { id?: string; deg: number } = $props();

  const pct = $derived(Math.max(0, Math.min(100, ((deg - 20) / 70) * 100)).toFixed(1));
</script>

<div class="spec-wrap" {id} aria-hidden="true" use:istyle={`--pct: ${pct}%`}>
  <div class="heat-rail">
    <span class="heat-ghost"></span>
    <span class="heat-fill"></span>
    <span class="heat-edge"></span>
  </div>
  <span class="spec-zones">
    <b use:istyle={"left:2%"}>cool</b>
    <b use:istyle={"left:66%"}>warm</b>
    <b use:istyle={"right:0"}>hot</b>
  </span>
</div>
