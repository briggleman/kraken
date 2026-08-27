<script lang="ts">
  import { ui, openSheet, closeSheet } from "@/lib/state.svelte";
  import { fleet } from "@/lib/fleet.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";
  import type { PlatformKind, Spec } from "@/api/types";

  const n = $derived(fleet.specs.length);

  // platform kind → short tag; also the row-class suffix the CSS :has() chips
  // filter on (.p-linux / .p-win / .p-wine)
  const PLAT: Record<PlatformKind, string> = {
    "linux-native": "linux",
    "windows-native": "win",
    "linux-wine": "wine",
  };

  function rowClass(s: Spec): string {
    return "spec-row" + s.platforms.map((p) => " p-" + PLAT[p.kind]).join("");
  }

  function manage(s: Spec, e: MouseEvent & { currentTarget: HTMLElement }) {
    ui.specEditId = s.id;
    openSheet("specEdit", e.clientX, e.clientY, e.currentTarget);
  }
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
      {#each fleet.specs as row (row.id)}
        <div class={rowClass(row)}>{#if row.banner_url}<span class="spec-art" style="background-image: url('{row.banner_url}')" aria-hidden="true"></span>{/if}<span class="spec-shade" aria-hidden="true"></span><span class="spec-id"><span class="spec-name">{row.name}</span><span class="spec-slug">{row.slug}</span></span><span class="spec-plat">{#each row.platforms as p (p.kind)}<span class="spec-tag">{PLAT[p.kind]}</span>{/each}</span><span class="spec-ver">v{row.version}</span><span class="spec-act"><button class="cfg-btn ghost" onclick={(e) => manage(row, e)}>manage</button><button class="cfg-btn ghost spec-go" onclick={(e) => openSheet("nsForm", e.clientX, e.clientY, e.currentTarget)}>deploy</button></span></div>
      {/each}
    </div>

    <div class="specs-foot">
      <span class="audit-note">a spec is the recipe for one game: which image runs it, how it installs, how it starts and stops, and which ports and variables a server may set. the new server sheet reads its game list from here.</span>
      <span class="audit-note">{n === 0 ? "no specs" : `showing 1–${n} of ${n}`}</span>
    </div>
  </div>
</div>
