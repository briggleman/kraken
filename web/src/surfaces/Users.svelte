<script lang="ts">
  import { ui, closeSheet } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";
</script>

<div
  class="sheet"
  class:open={!!ui.open.users}
  id="users"
  role="dialog"
  aria-modal="true"
  aria-labelledby="usersTitle"
  style="--ox: {ui.open.users?.ox ?? '50%'}; --oy: {ui.open.users?.oy ?? '50%'}"
  use:sheetFocus
>
  <div class="depth-head">
    <button class="surface-btn" onclick={() => closeSheet("users")}>
      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6 10 V 2 M 2 6 L 6 2 L 10 6"/></svg>
      surface
    </button>
    <h2 class="depth-title" id="usersTitle">users</h2>
    <div class="prefs-note"><span class="synthetic">values are sample — surface not wired</span></div>
  </div>
  <div class="sheet-body users-body">
    <div class="audit-bar">
      <input class="cfg-in audit-search" type="search" placeholder="filter by name or address" />
      <div class="audit-chips" role="group" aria-label="Filter by role">
        <label class="audit-chip uc-all"><input class="au-r" type="radio" name="ucf" checked />all</label>
        <label class="audit-chip uc-owner"><input class="au-r" type="radio" name="ucf" />owner</label>
        <label class="audit-chip uc-op"><input class="au-r" type="radio" name="ucf" />operators</label>
        <label class="audit-chip uc-view"><input class="au-r" type="radio" name="ucf" />viewers</label>
      </div>
      <span class="audit-count">5 accounts · 1 owner · 1 invite pending</span>
    </div>

    <div class="users-table">
      <div class="users-head">
        <span>username</span><span>email</span><span>role</span><span>status</span><span class="u-c-act">actions</span>
      </div>
      <div class="users-row self r-owner"><span class="u-name">ben <em>(you)</em></span><span class="u-mail">ben@zerooneone.io</span><select class="cfg-in u-sel" aria-label="Role"><option selected>owner</option><option>operator</option><option>viewer</option></select><span class="u-status"><label class="tgl u-tgl"><input type="checkbox" checked /><i></i>active</label></span><span class="u-act"><button class="mini-act ">reset</button></span></div>
      <div class="users-row r-operator"><span class="u-name">kestrel</span><span class="u-mail">—</span><select class="cfg-in u-sel" aria-label="Role"><option>owner</option><option selected>operator</option><option>viewer</option></select><span class="u-status"><label class="tgl u-tgl"><input type="checkbox" checked /><i></i>active</label></span><span class="u-act"><button class="mini-act ">reset</button><button class="mini-act del">remove</button></span></div>
      <div class="users-row r-operator"><span class="u-name">tidewalker</span><span class="u-mail">tidewalker@zerooneone.io</span><select class="cfg-in u-sel" aria-label="Role"><option>owner</option><option selected>operator</option><option>viewer</option></select><span class="u-status"><span class="u-state invited">invited</span></span><span class="u-act"><button class="mini-act res">resend</button><button class="mini-act del">revoke</button></span></div>
      <div class="users-row r-viewer"><span class="u-name">marrow</span><span class="u-mail">marrow@zerooneone.io</span><select class="cfg-in u-sel" aria-label="Role"><option>owner</option><option>operator</option><option selected>viewer</option></select><span class="u-status"><label class="tgl u-tgl"><input type="checkbox" checked /><i></i>active</label></span><span class="u-act"><button class="mini-act ">reset</button><button class="mini-act del">remove</button></span></div>
      <div class="users-row r-viewer"><span class="u-name">oldwatch</span><span class="u-mail">—</span><select class="cfg-in u-sel" aria-label="Role"><option>owner</option><option>operator</option><option selected>viewer</option></select><span class="u-status"><label class="tgl u-tgl"><input type="checkbox" /><i></i>disabled</label></span><span class="u-act"><button class="mini-act ">reset</button><button class="mini-act del">remove</button></span></div>
    </div>

    <section class="prefs-group" aria-label="Invite a user">
      <div class="cfg-head">
        <h3 class="pane-label">invite a user</h3>
        <span class="cfg-badge enc">one-time link</span>
        <span class="cfg-badge env">no password is set here</span>
      </div>
      <div class="cfg">
        <div class="cfg-row">
          <span>email address</span>
          <input class="cfg-in" type="email" placeholder="name@example.com" aria-label="Email address" />
        </div>
        <div class="cfg-row">
          <span>role</span>
          <select class="cfg-in"><option>viewer — read only</option><option>operator — start, stop, files</option><option>owner — everything, including settings</option></select>
        </div>
        <div class="cfg-row">
          <span>link expires</span>
          <select class="cfg-in"><option>24 hours</option><option>7 days</option><option>1 hour</option></select>
        </div>
        <div class="cfg-row">
          <span>invite link — copy it once, it is not shown again</span>
          <div class="cfg-ro">https://kraken.zerooneone.io/invite/sample-invite-not-a-real-credential</div>
        </div>
        <p class="cfg-help">the invitee sets their own password from the link, so no credential is ever typed on this screen or stored in a form. an unused link can be revoked from the table above.</p>
      </div>
      <div class="cfg-actions">
        <span class="cfg-note">sign-ins and invites are recorded in the audit log</span>
        <button class="cfg-btn ghost" onclick={() => closeSheet("users")}>close</button>
        <button class="cfg-btn solid">create invite link</button>
      </div>
    </section>

    <div class="users-foot">
      <div class="users-legend"><b>owner</b><span>everything, including settings, nodes and deletes</span></div>
      <div class="users-legend"><b>operator</b><span>start, stop, restart, console and files — no settings</span></div>
      <div class="users-legend"><b>viewer</b><span>read the dashboard and the logs, act on nothing</span></div>
      <div class="users-legend"><b>machine identities</b><span>system and agent:&lt;node&gt; act in the audit log but are not accounts</span></div>
    </div>
  </div>
</div>
