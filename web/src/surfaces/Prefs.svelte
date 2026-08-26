<script lang="ts">
  import { ui, openSheet, closeSheet } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";
</script>

<div
  class="prefs"
  class:open={!!ui.open.prefs}
  id="prefs"
  role="dialog"
  aria-modal="true"
  aria-labelledby="prefsTitle"
  style="--ox: {ui.open.prefs?.ox ?? '50%'}; --oy: {ui.open.prefs?.oy ?? '50%'}"
  use:sheetFocus
>
  <div class="depth-head">
    <button class="surface-btn" id="prefsSurfaceBtn" onclick={() => closeSheet("prefs")}>
      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6 10 V 2 M 2 6 L 6 2 L 10 6"/></svg>
      surface
    </button>
    <h2 class="depth-title" id="prefsTitle">console settings</h2>
    <div class="prefs-note">
      <span class="synthetic">values are sample — surface not wired</span>
    </div>
  </div>
  <div class="prefs-body pb-split">
    <div class="pb-col">
      <div class="pb-col-head"><h3 class="pane-label">integrations</h3><span class="pb-col-note">things you configure here</span></div>
      <section class="prefs-group" aria-label="Cloudflare DNS">
        <div class="cfg-head"><h3 class="pane-label">cloudflare dns</h3><span class="cfg-badge enc">encrypted</span><span class="cfg-badge ok">configured</span></div>
        <div class="cfg"><p class="cfg-desc">a scoped cloudflare api token (dns edit) lets servers publish a dns name to your domains.</p><label class="cfg-row"><span>replace api token</span><input class="cfg-in" type="password" placeholder="•••••••••• (leave blank to keep)" /><p class="cfg-help">stored server-side and never shown again.</p></label></div>
        <div class="cfg-actions"><button class="cfg-btn ghost">test connection</button><button class="cfg-btn solid">save token</button></div>
      </section>
      <section class="prefs-group" aria-label="UniFi gateway">
        <div class="cfg-head"><h3 class="pane-label">unifi gateway</h3><span class="cfg-badge enc">encrypted</span><span class="cfg-badge ok">configured</span></div>
        <div class="cfg"><p class="cfg-desc">a unifi os api key lets servers open port forwards on your gateway.</p><label class="cfg-row"><span>controller url</span><input class="cfg-in" type="text" placeholder="https://192.168.1.1" /></label><label class="cfg-row"><span>replace api key</span><input class="cfg-in" type="password" placeholder="•••••••••• (leave blank to keep)" /></label><label class="cfg-row"><span>site</span><input class="cfg-in" type="text" placeholder="default" /></label><label class="tgl "><input type="checkbox" /><i></i>verify tls certificate</label><p class="cfg-help">off by default — unifi gateways ship with a self-signed cert on the lan. turn on once a trusted certificate is installed on the controller.</p></div>
        <div class="cfg-actions"><button class="cfg-btn ghost">test connection</button><button class="cfg-btn solid">save</button></div>
      </section>
    </div>
    <div class="pb-col">
      <div class="pb-col-head"><h3 class="pane-label">platform</h3><span class="pb-col-note">mostly pinned by the environment</span></div>
      <section class="prefs-group" aria-label="Database">
        <div class="cfg-head"><h3 class="pane-label">database</h3><span class="cfg-badge env">env-managed</span></div>
        <div class="cfg"><div class="cfg-row"><div class="cfg-ro">kraken@postgres:5432/kraken · sslmode=disable</div><p class="cfg-help">managed via KRAKEN_DATABASE_URL.</p></div></div>
      </section>
      <section class="prefs-group" aria-label="Sessions and security">
        <div class="cfg-head"><h3 class="pane-label">sessions &amp; security</h3></div>
        <div class="cfg"><label class="cfg-row"><span>session lifetime</span><input class="cfg-in" type="text" value="86400" /><p class="cfg-help">duration in seconds — ≈ 24.0h a login stays valid.</p></label><label class="cfg-row"><span>allowed websocket origins <span class="cfg-badge env">env-managed</span></span><input class="cfg-in" type="text" value="https://kraken.zerooneone.io" disabled /><p class="cfg-help">managed via KRAKEN_ALLOWED_ORIGINS.</p></label><label class="tgl is-locked"><input type="checkbox" checked disabled /><i></i>auto-create the bootstrap admin when no users exist</label><p class="cfg-help">read-only — pinned on via the KRAKEN_BOOTSTRAP_ADMIN_* env vars.</p></div>
        <div class="cfg-actions"><span class="cfg-note">env-managed rows cannot be edited here</span><button class="cfg-btn solid">save</button></div>
      </section>
      <section class="prefs-group" aria-label="Users and access">
        <div class="cfg-head"><h3 class="pane-label">users &amp; access</h3><span class="cfg-badge ok">5 accounts</span></div>
        <div class="cfg"><p class="cfg-desc">sessions above decide how long a login lasts; this decides who gets one, and what they may do once they are in.</p><div class="cfg-row"><span>roles in use</span><div class="cfg-ro">1 owner · 2 operators · 2 viewers</div></div><p class="cfg-help">the bootstrap admin above holds the owner role until another account is promoted. every sign-in, invite and role change lands in the audit log.</p></div>
        <div class="cfg-actions"><span class="cfg-note">1 invite is still unused</span><button class="cfg-btn solid" onclick={(e) => openSheet("users", e.clientX, e.clientY, e.currentTarget)}>manage users</button></div>
      </section>
      <section class="prefs-group" aria-label="Game specs">
        <div class="cfg-head"><h3 class="pane-label">game specs</h3><span class="cfg-badge ok">8 specs</span></div>
        <div class="cfg"><p class="cfg-desc">the recipe for each game: which image runs it, how it installs, how it starts and stops, and which ports and variables a server may set.</p><div class="cfg-row"><span>platforms covered</span><div class="cfg-ro">3 linux · 5 windows · 2 wine</div></div><p class="cfg-help">the new server sheet reads its game list from here, so a game missing there is a spec missing here. rarely touched once a game works.</p></div>
        <div class="cfg-actions"><span class="cfg-note">a spec is shared by every server of that game, so it is edited here rather than per server</span><button class="cfg-btn solid" onclick={(e) => openSheet("specs", e.clientX, e.clientY, e.currentTarget)}>manage specs</button></div>
      </section>
    </div>
  </div>
</div>
