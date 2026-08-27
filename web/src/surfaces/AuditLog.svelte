<script lang="ts">
  import { ui, closeSheet } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";
  import { fleet } from "@/lib/fleet.svelte";
  import type { AuditEntry } from "@/api/types";

  let query = $state("");

  const shown = $derived(
    fleet.audit.filter((e) => {
      const q = query.trim().toLowerCase();
      if (!q) return true;
      return (
        e.actor.toLowerCase().includes(q) ||
        e.action.toLowerCase().includes(q) ||
        `${e.method} ${e.path}`.toLowerCase().includes(q) ||
        (e.target_type ?? "").toLowerCase().includes(q) ||
        (e.target_id ?? "").toLowerCase().includes(q)
      );
    }),
  );

  const total = $derived(fleet.audit.length);
  const countLine = $derived(
    `${total} entr${total === 1 ? "y" : "ies"}${shown.length !== total ? ` · ${shown.length} shown` : ""}`,
  );

  // the CSS :has() chips filter on these row classes
  function rowClass(e: AuditEntry): string {
    let cls = "audit-row";
    if (e.method !== "GET") cls += " change";
    if (e.path.startsWith("/auth") || /login|password/i.test(e.action)) cls += " auth";
    if (e.status >= 400 && e.status < 500) cls += " f4";
    if (e.status >= 500) cls += " f5";
    return cls;
  }

  function resClass(status: number): string {
    if (status >= 500) return "s5";
    if (status >= 400) return "s4";
    return "s2";
  }

  function fmtTime(iso: string): string {
    const d = new Date(iso);
    const mon = d.toLocaleString("en-US", { month: "short" }).toLowerCase();
    return `${d.getDate()} ${mon} · ${d.toLocaleTimeString("en-US", { hour12: false })}`;
  }
</script>

<div
  class="sheet"
  class:open={!!ui.open.auditLog}
  id="auditLog"
  role="dialog"
  aria-modal="true"
  aria-labelledby="auditTitle"
  style="--ox: {ui.open.auditLog?.ox ?? '50%'}; --oy: {ui.open.auditLog?.oy ?? '50%'}"
  use:sheetFocus
>
  <div class="depth-head">
    <button class="surface-btn" onclick={() => closeSheet("auditLog")}>
      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6 10 V 2 M 2 6 L 6 2 L 10 6"/></svg>
      surface
    </button>
    <h2 class="depth-title" id="auditTitle">audit log</h2>
    <div class="prefs-note"></div>
  </div>
  <div class="sheet-body audit-body">
    <div class="audit-bar">
      <input class="cfg-in audit-search" type="search" placeholder="filter by actor, verb, route or target" bind:value={query} />
      <div class="audit-chips" role="group" aria-label="Filter entries">
        <label class="audit-chip ch-all"><input class="au-r" type="radio" name="auf" checked />all</label>
        <label class="audit-chip ch-change"><input class="au-r" type="radio" name="auf" />changes</label>
        <label class="audit-chip ch-auth"><input class="au-r" type="radio" name="auf" />sign-in</label>
        <label class="audit-chip ch-fail"><input class="au-r" type="radio" name="auf" />failures</label>
      </div>
      <span class="audit-count">{countLine}</span>
    </div>

    <div class="audit-table">
      <div class="audit-head">
        <span>when</span><span>actor</span><span>action</span><span>target</span><span>source</span><span class="a-c-res">status</span>
      </div>
      {#each shown as e (e.id)}
        <div class={rowClass(e)}>
          <span class="a-t">{fmtTime(e.time)}</span>
          <span class="a-who">{e.actor}</span>
          <span class="a-act"><b class="a-verb">{e.action}</b><em class="a-route">{e.method} {e.path}</em></span>
          <span class="a-tgt">{e.target_type || "—"}{#if e.target_id}<em> · {e.target_id}</em>{/if}</span>
          <span class="a-src">{e.ip || "—"}</span>
          <span class="a-res {resClass(e.status)}">{e.status}</span>
        </div>
      {:else}
        <div class="audit-row"><span class="a-t">—</span><span class="a-who">—</span><span class="a-act"><b class="a-verb">no entries yet</b></span><span class="a-tgt">—</span><span class="a-src">—</span><span class="a-res"></span></div>
      {/each}
    </div>

    <div class="audit-foot">
      <span class="audit-note">written by the api and retained 90 days — entries cannot be edited or removed here. 2xx is green, 4xx violet (the caller got it wrong), 5xx magenta (we did).</span>
      <span class="audit-note">source is the address the api saw. behind a reverse proxy that is the proxy, not the client, unless the panel is trusting X-Forwarded-For — until it does, treat this column as advisory.</span>
    </div>
  </div>
</div>
