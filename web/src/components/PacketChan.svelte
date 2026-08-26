<script lang="ts">
  // Packet channel: wan/lan ends with packets crossing between them. Packet
  // styles are data ("rev|" prefix marks the return direction), --rate scales
  // the whole channel's tempo against the readout's reference rate.
  let {
    id = undefined,
    rate,
    packets,
  }: { id?: string; rate: number; packets: string[] } = $props();

  const parsed = $derived(
    packets.map((p) => {
      const rev = p.startsWith("rev|");
      return { rev, style: rev ? p.slice(4) : p };
    }),
  );
</script>

<div class="chan" {id} aria-hidden="true" style="--rate: {rate.toFixed(2)}">
  <span class="end l">wan</span>
  <span class="end r">lan</span>
  {#each parsed as p}
    <i class="pk{p.rev ? ' rev' : ''}" style={p.style}></i>
  {/each}
</div>
