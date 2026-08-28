<script lang="ts">
  import { istyle } from "@/lib/istyle";
  // Packet channel: wan/lan ends with packets crossing between them. --rate
  // scales the whole channel's tempo against the readout's reference rate, so
  // the animation speeds up when the node's throughput does.
  //
  // The packets themselves are decoration, not data — each is a size, duration,
  // delay and track position. They live here rather than in a view model
  // because nothing outside this component means anything by them; `variant`
  // just picks a set so adjacent node bands don't animate in lockstep.
  //
  // unknown: no network feed for this node. The channel renders empty — ends
  // but no traffic — instead of animating packets that stand for nothing.
  let {
    id = undefined,
    rate,
    variant = 0,
    unknown = false,
  }: { id?: string; rate: number; variant?: number; unknown?: boolean } = $props();

  const PACKET_SETS: string[][] = [
    [
      "--s:4; --d:3.1s; --dl:-0.5s; top:30%",
      "--s:6; --d:4.4s; --dl:-2.4s; top:42%",
      "--s:2.5; --d:2.2s; --dl:-1.4s; top:24%",
      "--s:3; --d:2.7s; --dl:-0.2s; top:36%",
      "rev|--s:3; --d:3.6s; --dl:-1.8s; top:62%",
      "rev|--s:2; --d:2.5s; --dl:-0.7s; top:72%",
      "rev|--s:4.5; --d:4.9s; --dl:-3s; top:68%",
    ],
    [
      "--s:3; --d:3.4s; --dl:-0.9s; top:28%",
      "--s:5; --d:4.8s; --dl:-2.1s; top:44%",
      "--s:2; --d:2.6s; --dl:-1.1s; top:34%",
      "rev|--s:2.5; --d:3.9s; --dl:-2.6s; top:64%",
      "rev|--s:3.5; --d:4.3s; --dl:-0.4s; top:70%",
    ],
  ];

  const parsed = $derived(
    unknown
      ? []
      : PACKET_SETS[variant % PACKET_SETS.length].map((p) => {
          const rev = p.startsWith("rev|");
          return { rev, style: rev ? p.slice(4) : p };
        }),
  );

  // The CSS divides each packet's duration by --rate, so a zero would be a
  // division by zero and an invalid animation. An idle link floors here and
  // drifts, which is what idle should look like.
  const tempo = $derived(Math.max(0.05, rate));
</script>

<div class="chan" {id} aria-hidden="true" use:istyle={`--rate: ${tempo.toFixed(2)}`}>
  <span class="end l">wan</span>
  <span class="end r">lan</span>
  {#each parsed as p}
    <i class="pk{p.rev ? ' rev' : ''}" style={p.style}></i>
  {/each}
</div>
