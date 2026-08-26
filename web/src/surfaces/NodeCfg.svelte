<script lang="ts">
  import { ui, closeSheet } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";
</script>

<div
  class="sheet"
  class:open={!!ui.open.nodeCfg}
  id="nodeCfg"
  role="dialog"
  aria-modal="true"
  aria-labelledby="nodeCfgTitle"
  style="--ox: {ui.open.nodeCfg?.ox ?? '50%'}; --oy: {ui.open.nodeCfg?.oy ?? '50%'}"
  use:sheetFocus
>
  <div class="depth-head">
    <button class="surface-btn" onclick={() => closeSheet("nodeCfg")}>
      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6 10 V 2 M 2 6 L 6 2 L 10 6"/></svg>
      surface
    </button>
    <h2 class="depth-title" id="nodeCfgTitle">node settings <b class="node-name-inline">behemoth</b></h2>
    <div class="prefs-note"><span class="synthetic">values are sample — surface not wired</span></div>
  </div>
  <div class="sheet-body">
    <section class="prefs-group" aria-label="Capacity">
      <div class="cfg-head"><h3 class="pane-label">capacity</h3></div>
      <div class="cfg">
        <p class="cfg-desc">schedulable capacity, where this node stores backups, and whether they are mirrored off-node.</p>
        <label class="cfg-row">
          <span>total memory (mb)</span>
          <input class="cfg-in" type="text" value="64000" />
          <p class="cfg-help">memory the scheduler may hand to game servers. 20600MB is already reserved by servers on this node.</p>
        </label>
        <label class="cfg-row"><span>port start</span><input class="cfg-in" type="text" value="28000" /></label>
        <label class="cfg-row">
          <span>port end</span>
          <input class="cfg-in" type="text" value="28999" />
          <p class="cfg-help">game ports the scheduler allocates from. changing the range never touches running servers — their ports stay reserved. nodes sharing one ip need non-overlapping ranges.</p>
        </label>
      </div>
    </section>

    <section class="prefs-group" aria-label="Backups">
      <div class="cfg-head"><h3 class="pane-label">backups</h3></div>
      <div class="cfg">
        <label class="cfg-row">
          <span>backup target</span>
          <select class="cfg-in"><option>local disk</option><option>sftp remote</option></select>
        </label>
        <label class="cfg-row">
          <span>backup dir (optional)</span>
          <input class="cfg-in" type="text" placeholder="leave blank for the node default" />
          <p class="cfg-help">supports {"{{SLUG}}"} (the game's slug) for per-game folders, e.g. /var/backups/{"{{SLUG}}"}.</p>
        </label>
        <label class="tgl"><input type="checkbox" /><i></i>mirror backups to an sftp remote</label>
      </div>
    </section>

    <section class="prefs-group" aria-label="Steam account">
      <div class="cfg-head"><h3 class="pane-label">steam account</h3><span class="cfg-badge enc">encrypted</span></div>
      <div class="cfg">
        <p class="cfg-desc">needed only for games whose dedicated server is not anonymous-downloadable. enter a steam account that owns the game; provide the steam guard code at deploy time.</p>
        <label class="cfg-row"><span>username</span><input class="cfg-in" type="text" /></label>
        <label class="cfg-row"><span>password</span><input class="cfg-in" type="password" /></label>
      </div>
      <div class="cfg-actions">
        <button class="cfg-btn ghost" onclick={() => closeSheet("nodeCfg")}>close</button>
        <button class="cfg-btn solid">save &amp; apply</button>
      </div>
    </section>
  </div>
</div>
