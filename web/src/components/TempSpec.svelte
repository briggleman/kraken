<script lang="ts">
  import { istyle } from "@/lib/istyle";
  // Heat spectrum fill: the full thermal gradient as a ghost, solid fill
  // clipped to the live value on a 20–90°C scale, cool/warm/hot beneath.
  //
  // unknown: this host reports no temperature (no thermal zone in a VM, no WMI
  // source on Windows). The ghost gradient stays — it's the scale, not a
  // reading — but nothing is filled, matching the readout's em-dash. Drawing a
  // fill at 0 would render as a confident "stone cold".
  let { id = undefined, deg, unknown = false }: { id?: string; deg: number; unknown?: boolean } =
    $props();

  const pct = $derived(
    unknown ? "0" : Math.max(0, Math.min(100, ((deg - 20) / 70) * 100)).toFixed(1),
  );
</script>

<div class="spec-wrap" {id} aria-hidden="true" use:istyle={`--pct: ${pct}%`}>
  <div class="heat-rail">
    <span class="heat-ghost"></span>
    {#if !unknown}
      <span class="heat-fill"></span>
      <span class="heat-edge"></span>
    {/if}
  </div>
  <span class="spec-zones">
    <b use:istyle={"left:2%"}>cool</b>
    <b use:istyle={"left:66%"}>warm</b>
    <b use:istyle={"right:0"}>hot</b>
  </span>
</div>
