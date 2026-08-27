<script lang="ts">
  import { istyle } from "@/lib/istyle";
  import { ui, closeSheet } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";
  import { auth } from "@/lib/auth.svelte";
  import { api } from "@/api/client";
  import type { AdminUser, Role } from "@/api/types";

  let users = $state<AdminUser[]>([]);
  let roles = $state<Role[]>([]);
  let err = $state<string | null>(null);
  let query = $state("");

  // the "shown once" reveals: a reset password, and a freshly created account's
  // password. both are generated in the browser and never come back from the api.
  let revealed = $state<{ username: string; password: string } | null>(null);
  let createdPw = $state<{ username: string; password: string } | null>(null);

  // create-account form
  let newUsername = $state("");
  let newEmail = $state("");
  let newRoleId = $state("");

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
      err = null;
    }
  });

  async function load() {
    err = null;
    try {
      const [u, r] = await Promise.all([api.listUsers(), api.listRoles()]);
      users = u.users ?? [];
      roles = r.roles ?? [];
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

  async function createAccount() {
    const username = newUsername.trim();
    if (!username || !newRoleId) return;
    err = null;
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
  use:istyle={`--ox: ${ui.open.users?.ox ?? '50%'}; --oy: ${ui.open.users?.oy ?? '50%'}`}
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
          </span>
          <span class="u-act">
            <button class="mini-act" onclick={() => void resetPassword(u)}>reset</button>
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

    <section class="prefs-group" aria-label="Create an account">
      <div class="cfg-head">
        <h3 class="pane-label">create an account</h3>
        <span class="cfg-badge enc">one-time password</span>
        <span class="cfg-badge env">generated in your browser</span>
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
        {#if createdPw}
          <div class="cfg-row">
            <span>one-time password for {createdPw.username} — copy it once, it is not shown again</span>
            <div class="cfg-ro">{createdPw.password}</div>
          </div>
        {/if}
        <p class="cfg-help">there are no invite links yet — the account is created directly, with a strong password generated in your browser and shown once above. copy it and hand it to the user; the panel never shows it again. the reset action in the table mints a new one the same way.</p>
      </div>
      <div class="cfg-actions">
        <span class="cfg-note">sign-ins and account changes are recorded in the audit log</span>
        <button class="cfg-btn ghost" onclick={() => closeSheet("users")}>close</button>
        <button class="cfg-btn solid" disabled={!newUsername.trim() || !newRoleId} onclick={() => void createAccount()}>create account</button>
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
