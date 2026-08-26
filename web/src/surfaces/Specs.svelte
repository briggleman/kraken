<script lang="ts">
  import { ui, sim, openSheet, closeSheet } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";

  const n = $derived(sim.specs.length);
</script>

<div
  class="sheet"
  class:open={!!ui.open.specs}
  id="specs"
  role="dialog"
  aria-modal="true"
  aria-labelledby="specsTitle"
  style="--ox: {ui.open.specs?.ox ?? '50%'}; --oy: {ui.open.specs?.oy ?? '50%'}"
  use:sheetFocus
>
  <div class="depth-head">
    <button class="surface-btn" onclick={() => closeSheet("specs")}>
      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6 10 V 2 M 2 6 L 6 2 L 10 6"/></svg>
      surface
    </button>
    <h2 class="depth-title" id="specsTitle">game specs</h2>
    <div class="prefs-note"><span class="synthetic">values are sample — surface not wired</span></div>
  </div>
  <div class="sheet-body specs-body">
    <div class="audit-bar">
      <input class="cfg-in audit-search" type="search" placeholder="search name or slug" />
      <div class="audit-chips" role="group" aria-label="Filter by platform">
        <label class="audit-chip sp-all"><input class="au-r" type="radio" name="spf" checked />all</label>
        <label class="audit-chip sp-linux"><input class="au-r" type="radio" name="spf" />linux</label>
        <label class="audit-chip sp-win"><input class="au-r" type="radio" name="spf" />windows</label>
        <label class="audit-chip sp-wine"><input class="au-r" type="radio" name="spf" />wine</label>
      </div>
      <span class="audit-count">{n} spec{n === 1 ? "" : "s"}</span>
    </div>

    <div class="spec-list">
      {#each sim.specs as row}
        <div class="spec-row{row.plats.map((p) => " p-" + p).join("")}" style={row.leaving ? "opacity: 0.2" : ""}>{#if row.art}<span class="spec-art" style="background-image: url('{row.art}')" aria-hidden="true"></span>{/if}<span class="spec-shade" aria-hidden="true"></span><span class="spec-id"><span class="spec-name">{row.name}</span><span class="spec-slug">{row.slug}</span></span><span class="spec-plat">{#each row.plats as p}<span class="spec-tag">{p}</span>{/each}</span><span class="spec-ver">{row.ver}</span><span class="spec-act"><button class="cfg-btn ghost" onclick={(e) => openSheet("specEdit", e.clientX, e.clientY, e.currentTarget)}>manage</button><button class="cfg-btn ghost spec-go" onclick={(e) => openSheet("nsForm", e.clientX, e.clientY, e.currentTarget)}>deploy</button></span></div>
      {/each}
    </div>

    <div class="specs-foot">
      <span class="audit-note">a spec is the recipe for one game: which image runs it, how it installs, how it starts and stops, and which ports and variables a server may set. the new server sheet reads its game list from here.</span>
      <span class="audit-note">{n === 0 ? "no specs" : `showing 1–${n} of ${n}`}</span>
    </div>
  </div>
</div>
