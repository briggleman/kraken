<script lang="ts">
  import { istyle } from "@/lib/istyle";
  import { ui, closeSheet, sheetZ } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";
  import { auth } from "@/lib/auth.svelte";
  import { api, errMsg } from "@/api/client";
  import { inviteChip, inviteModeAvailable } from "@/lib/invite";
  import type { AdminUser, PanelSettings, Role } from "@/api/types";

  let users = $state<AdminUser[]>([]);
  let roles = $state<Role[]>([]);
  // the panel's email settings decide whether the create form can offer an
  // invite; they fail soft — no settings, no invite mode
  let settings = $state<PanelSettings | null>(null);
  let err = $state<string | null>(null);
  let query = $state("");

  // the create form's two modes: a one-time password shown here, or an
  // emailed invite. the toggle is drawn only once an invite is possible.
  let useInvite = $state(true);
  let inviteNote = $state<string | null>(null);

  // the "shown once" reveals: a reset password, and a freshly created account's
  // password. both are generated in the browser and never come back from the api.
  let revealed = $state<{ username: string; password: string } | null>(null);
  let createdPw = $state<{ username: string; password: string } | null>(null);

  // create-account form
  let newUsername = $state("");
  let newEmail = $state("");
  let newRoleId = $state("");
  const inviteReady = $derived(inviteModeAvailable(settings, newEmail));
  const inviting = $derived(inviteReady && useInvite);

  // fetch once per open; reveals are cleared on close — they are shown once.
  // fetchedOpen is a plain variable, NOT $state: the effect both reads and
  // writes it, and a reactive read-write in one effect is an infinite loop.
  let fetchedOpen = false;
  $effect(() => {
    const open = !!ui.open.users;
    if (open && !fetchedOpen) {
      fetchedOpen = true;
      void load();
    } else if (!open && fetchedOpen) {
      fetchedOpen = false;
      revealed = null;
      createdPw = null;
      inviteNote = null;
      err = null;
    }
  });

  async function load() {
    err = null;
    try {
      const [u, r, s] = await Promise.all([api.listUsers(), api.listRoles(), api.getPanelSettings().catch(() => null)]);
      users = u.users ?? [];
      roles = r.roles ?? [];
      settings = s;
      if (!roles.some((x) => x.id === newRoleId)) {
        newRoleId =
          roles.find((x) => x.name.toLowerCase().includes("operator"))?.id ?? roles[0]?.id ?? "";
      }
    } catch (e) {
      err = e instanceof Error ? e.message : "failed to load users";
    }
  }

  function fail(e: unknown, fallback: string) {
    err = e instanceof Error ? e.message : fallback;
  }

  function roleName(roleId: string): string {
    return roles.find((r) => r.id === roleId)?.name ?? roleId;
  }

  // the CSS :has() chips filter rows on these classes. Keyed off the role id
  // first (owner / admin / operator / readonly are the built-ins) and the
  // display name second, so "Read-only" lands on the viewer chip rather than
  // falling through unclassified.
  function roleClass(u: AdminUser): string {
    const key = (u.role_id + " " + roleName(u.role_id)).toLowerCase();
    if (key.includes("owner") || key.includes("admin")) return " r-owner";
    if (key.includes("operator")) return " r-operator";
    if (key.includes("readonly") || key.includes("read-only") || key.includes("view"))
      return " r-viewer";
    return "";
  }

  const shown = $derived(
    users.filter((u) => {
      const q = query.trim().toLowerCase();
      if (!q) return true;
      return u.username.toLowerCase().includes(q) || (u.email ?? "").toLowerCase().includes(q);
    }),
  );

  const ownerCount = $derived(users.filter((u) => roleClass(u) === " r-owner").length);
  const countLine = $derived(
    `${users.length} account${users.length === 1 ? "" : "s"} · ${ownerCount} owner${ownerCount === 1 ? "" : "s"}`,
  );

  // a strong random password, minted in the browser — never hardcoded, never
  // echoed by the api. unambiguous alphabet (no I/O/i/l/o/0/1), rejection
  // sampling so every character is uniform. ~138 bits.
  function genPassword(): string {
    const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789";
    const limit = alphabet.length * Math.floor(256 / alphabet.length);
    const buf = new Uint8Array(48);
    let out = "";
    while (out.length < 24) {
      crypto.getRandomValues(buf);
      for (const b of buf) {
        if (out.length >= 24) break;
        if (b < limit) out += alphabet[b % alphabet.length];
      }
    }
    return out;
  }

  async function setRole(u: AdminUser, roleId: string) {
    err = null;
    try {
      const updated = await api.updateUser(u.id, { role_id: roleId });
      users = users.map((x) => (x.id === u.id ? updated : x));
    } catch (e) {
      fail(e, "role change failed");
      void load(); // resync the select with what the server kept
    }
  }

  async function setActive(u: AdminUser, checked: boolean) {
    err = null;
    try {
      const updated = await api.updateUser(u.id, { disabled: !checked });
      users = users.map((x) => (x.id === u.id ? updated : x));
    } catch (e) {
      fail(e, "status change failed");
      void load();
    }
  }

  async function resetPassword(u: AdminUser) {
    err = null;
    const pw = genPassword();
    try {
      await api.resetUserPassword(u.id, pw);
      revealed = { username: u.username, password: pw };
    } catch (e) {
      fail(e, "password reset failed");
    }
  }

  async function removeUser(u: AdminUser) {
    err = null;
    try {
      await api.deleteUser(u.id);
      users = users.filter((x) => x.id !== u.id);
    } catch (e) {
      fail(e, "remove failed");
    }
  }

  async function resendInvite(u: AdminUser) {
    err = null;
    try {
      const r = await api.sendInvite(u.id);
      users = users.map((x) => (x.id === u.id ? { ...x, invite: r.invite } : x));
      inviteNote = `a new invite went to ${r.invite.sent_to} — the old link stopped working`;
    } catch (e) {
      fail(e, "resend failed");
      void load(); // a 424 leaves a failed invite on the row; show it
    }
  }

  async function revokeInvite(u: AdminUser) {
    err = null;
    try {
      await api.revokeInvite(u.id);
      users = users.map((x) => (x.id === u.id ? { ...x, invite: undefined } : x));
      inviteNote = `the invite for ${u.username} was revoked — the account stays, locked, until you resend or reset`;
    } catch (e) {
      fail(e, "revoke failed");
    }
  }

  async function createAccount() {
    const username = newUsername.trim();
    if (!username || !newRoleId) return;
    err = null;
    inviteNote = null;
    if (inviting) {
      try {
        const created = await api.createUser({
          username,
          email: newEmail.trim(),
          role_id: newRoleId,
          invite: true,
        });
        users = [...users, created];
        inviteNote = `invite sent to ${created.invite?.sent_to ?? newEmail.trim()} — the link is good for 24 hours; the account cannot be used until ${created.username} sets a password`;
        newUsername = "";
        newEmail = "";
      } catch (e) {
        // a 424 means the account exists but the email did not go out; the
        // list shows it with a failed chip and a resend action
        err = errMsg(e);
        void load();
      }
      return;
    }
    const pw = genPassword();
    try {
      const created = await api.createUser({
        username,
        email: newEmail.trim(),
        password: pw,
        role_id: newRoleId,
      });
      users = [...users, created];
      createdPw = { username: created.username, password: pw };
      newUsername = "";
      newEmail = "";
    } catch (e) {
      fail(e, "account creation failed");
    }
  }
</script>

<div
  class="sheet"
  class:open={!!ui.open.users}
  id="users"
  role="dialog"
  aria-modal="true"
  aria-labelledby="usersTitle"
  use:istyle={`--ox: ${ui.open.users?.ox ?? '50%'}; --oy: ${ui.open.users?.oy ?? '50%'}; z-index: ${sheetZ("users")}`}
  use:sheetFocus
>
  <div class="depth-head">
    <button class="surface-btn" onclick={() => closeSheet("users")}>
      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6 10 V 2 M 2 6 L 6 2 L 10 6"/></svg>
      surface
    </button>
    <h2 class="depth-title" id="usersTitle">users</h2>
    <div class="prefs-note"></div>
  </div>
  <div class="sheet-body users-body">
    <div class="audit-bar">
      <input class="cfg-in audit-search" type="search" placeholder="filter by name or address" bind:value={query} />
      <div class="audit-chips" role="group" aria-label="Filter by role">
        <label class="audit-chip uc-all"><input class="au-r" type="radio" name="ucf" checked />all</label>
        <label class="audit-chip uc-owner"><input class="au-r" type="radio" name="ucf" />owner</label>
        <label class="audit-chip uc-op"><input class="au-r" type="radio" name="ucf" />operators</label>
        <label class="audit-chip uc-view"><input class="au-r" type="radio" name="ucf" />viewers</label>
      </div>
      <span class="audit-count">{err ?? countLine}</span>
    </div>

    <div class="users-table">
      <div class="users-head">
        <span>username</span><span>email</span><span>role</span><span>status</span><span class="u-c-act">actions</span>
      </div>
      {#each shown as u (u.id)}
        {@const self = auth.user?.id === u.id}
        {@const chip = inviteChip(u.invite)}
        <div class="users-row{self ? ' self' : ''}{roleClass(u)}">
          <span class="u-name">{u.username}{#if self} <em>(you)</em>{/if}</span>
          <span class="u-mail">{u.email || "—"}</span>
          <select
            class="cfg-in u-sel"
            aria-label="Role"
            value={u.role_id}
            onchange={(e) => void setRole(u, e.currentTarget.value)}
          >
            {#each roles as r (r.id)}<option value={r.id}>{r.name}</option>{/each}
          </select>
          <span class="u-status">
            <label class="tgl u-tgl"
              ><input
                type="checkbox"
                checked={!u.disabled}
                disabled={self}
                onchange={(e) => void setActive(u, e.currentTarget.checked)}
              /><i></i>{u.disabled ? "disabled" : "active"}</label
            >
            {#if chip}<span class="u-state invited" title="sent to {u.invite?.sent_to}">{chip.text}</span>{/if}
          </span>
          <span class="u-act">
            {#if u.invite && !self}
              <button class="mini-act" onclick={() => void resendInvite(u)}>resend</button>
              <button class="mini-act" onclick={() => void revokeInvite(u)}>revoke</button>
            {:else}
              <button class="mini-act" onclick={() => void resetPassword(u)}>reset</button>
            {/if}
            {#if !self}<button class="mini-act del" onclick={() => void removeUser(u)}>remove</button>{/if}
          </span>
        </div>
      {:else}
        <div class="users-row"><span class="u-name">no accounts yet</span><span class="u-mail">—</span><span></span><span class="u-status"></span><span class="u-act"></span></div>
      {/each}
    </div>

    {#if revealed}
      <div class="cfg">
        <div class="cfg-row">
          <span>new password for {revealed.username} — copy it once, it is not shown again</span>
          <div class="cfg-ro">{revealed.password}</div>
        </div>
      </div>
    {/if}

    {#if inviteNote}
      <p class="wz-res shown ok" role="status" aria-live="polite"><svg width="13" height="13" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 2.5 7.2 L 5.4 10 L 11.5 3.6"/></svg><span>{inviteNote}</span></p>
    {/if}

    <section class="prefs-group" aria-label="Create an account">
      <div class="cfg-head">
        <h3 class="pane-label">create an account</h3>
        {#if inviting}
          <span class="cfg-badge enc">emailed invite</span>
          <span class="cfg-badge env">good for 24 hours</span>
        {:else}
          <span class="cfg-badge enc">one-time password</span>
          <span class="cfg-badge env">generated in your browser</span>
        {/if}
      </div>
      <div class="cfg">
        <div class="cfg-row">
          <span>username</span>
          <input class="cfg-in" type="text" placeholder="captain" aria-label="Username" bind:value={newUsername} />
        </div>
        <div class="cfg-row">
          <span>email address</span>
          <input class="cfg-in" type="email" placeholder="name@example.com" aria-label="Email address" bind:value={newEmail} />
        </div>
        <div class="cfg-row">
          <span>role</span>
          <select class="cfg-in" aria-label="Role for the new account" bind:value={newRoleId}>
            {#each roles as r (r.id)}<option value={r.id}>{r.name}</option>{/each}
          </select>
        </div>
        {#if inviteReady}
          <label class="tgl"><input type="checkbox" bind:checked={useInvite} /><i></i>send an invite instead of a one-time password</label>
        {/if}
        {#if createdPw}
          <div class="cfg-row">
            <span>one-time password for {createdPw.username} — copy it once, it is not shown again</span>
            <div class="cfg-ro">{createdPw.password}</div>
          </div>
        {/if}
        {#if inviting}
          <p class="cfg-help">the panel emails a one-time link, good for 24 hours, that lets them choose their own password and signs them in. nobody — not you, not the panel — ever sees a password for the account before they do. the row shows the invite until it is used; resend replaces the link, revoke kills it.</p>
        {:else if settings?.smtp_configured && settings.public_url}
          <p class="cfg-help">the account is created directly, with a strong password generated in your browser and shown once above. copy it and hand it to the user; the panel never shows it again. the reset action in the table mints a new one the same way. with an email address filled in, the toggle above sends an invite instead.</p>
        {:else}
          <p class="cfg-help">the account is created directly, with a strong password generated in your browser and shown once above. copy it and hand it to the user; the panel never shows it again. the reset action in the table mints a new one the same way. to email invites instead, set an smtp relay and a public address under console settings.</p>
        {/if}
      </div>
      <div class="cfg-actions">
        <span class="cfg-note">sign-ins, invites and account changes are recorded in the audit log</span>
        <button class="cfg-btn ghost" onclick={() => closeSheet("users")}>close</button>
        <button class="cfg-btn solid" disabled={!newUsername.trim() || !newRoleId} onclick={() => void createAccount()}>{inviting ? "send invite" : "create account"}</button>
      </div>
    </section>

    <div class="users-foot">
      <div class="users-legend"><b>owner</b><span>everything, including settings, nodes and deletes</span></div>
      <div class="users-legend"><b>admin</b><span>every server, node and setting, and the accounts</span></div>
      <div class="users-legend"><b>operator</b><span>runs the servers they create or are shared — start, stop, console, files; no settings</span></div>
      <div class="users-legend"><b>read-only</b><span>watches the servers shared with them, acts on nothing</span></div>
      <div class="users-legend"><b>machine identities</b><span>system and agent:&lt;node&gt; act in the audit log but are not accounts</span></div>
    </div>
  </div>
</div>
