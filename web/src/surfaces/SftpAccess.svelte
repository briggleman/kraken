<script lang="ts">
  import {
    depth,
    sftpHide,
    sftpRotate,
    sftpAddKey,
    sftpRemoveKey,
    sftpDisable,
  } from "@/lib/depth.svelte";

  const sftp = $derived(depth.sftp);
  // There is no separate "enable" call: the Panel turns SFTP on as a side
  // effect of setting the first credential, so this dialog's rotate/add-key
  // controls are the enable path, and the connection block stays honest about
  // being off until one of them has run.
  const on = $derived(!!sftp?.enabled);
  const host = $derived(sftp?.host ?? "");
  const port = $derived(sftp?.port ?? 0);
  const user = $derived(sftp?.username ?? "");
  const uri = $derived(on && host && port ? `sftp://${user}@${host}:${port}` : "");
  // A tunnel-mode node's SFTP listener is LAN-local. The Panel-side proxy is
  // what makes it reachable from wherever the Panel is; without it the address
  // above is only good from inside the node's own network, and saying so beats
  // handing over a connection string that quietly will not work.
  const lanOnly = $derived(!!sftp?.tunneled && !sftp?.proxied);

  let dialogEl: HTMLDivElement | undefined = $state();
  let copied = $state("");
  let adding = $state(false);
  let newKey = $state("");

  // wasOpen is a plain variable, NOT $state: this effect both reads and writes
  // it, and a reactive read-write in one effect is an infinite update loop.
  let opener: HTMLElement | null = null;
  let wasOpen = false;
  $effect(() => {
    const open = depth.sftpOpen;
    if (open && !wasOpen) {
      opener = document.activeElement as HTMLElement | null;
      adding = false;
      newKey = "";
      // Land focus on the dialog's first *enabled* control once it is on
      // screen. A microtask is too early — the `open` class arrives in the same
      // flush, so the card is still display:none — and asking for the first
      // button alone strands focus on the chip whenever the copy controls are
      // disabled, which is exactly the state a server with SFTP off opens in.
      setTimeout(() => focusable()[0]?.focus());
    } else if (!open && wasOpen) {
      // hand focus back to whatever opened this, the way the sheets do
      if (opener && document.contains(opener)) opener.focus();
      opener = null;
    }
    wasOpen = open;
  });

  function copy(what: string, value: string) {
    if (copied === what || !value) return;
    if (navigator.clipboard) navigator.clipboard.writeText(value).catch(() => {});
    copied = what;
    setTimeout(() => (copied = copied === what ? "" : copied), 1600);
  }

  async function submitKey() {
    await sftpAddKey(newKey);
    if (!depth.sftpError) {
      newKey = "";
      adding = false;
    }
  }

  function focusable(): HTMLElement[] {
    if (!dialogEl) return [];
    return [...dialogEl.querySelectorAll<HTMLElement>("button, input, textarea")].filter(
      (el) => !(el as HTMLButtonElement).disabled && el.offsetParent !== null,
    );
  }

  // Keep tabbing inside the dialog while it owns the screen. Escape is NOT
  // handled here — App's router owns it, so one press closes one layer.
  function trapTab(e: KeyboardEvent) {
    if (e.key !== "Tab") return;
    const f = focusable();
    if (!f.length) return;
    const first = f[0],
      last = f[f.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }
</script>

<!-- svelte-ignore a11y_interactive_supports_focus -->
<!-- keydown here is the focus trap; focus lives on the dialog's own controls -->
<div
  class="sftp"
  class:open={depth.sftpOpen}
  role="dialog"
  aria-modal="true"
  aria-labelledby="sftpTitle"
  bind:this={dialogEl}
  onkeydown={trapTab}
>
  <div class="sftp-veil" onclick={sftpHide} aria-hidden="true"></div>

  <div class="sftp-card">
    <h2 class="sftp-title" id="sftpTitle">
      <svg viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="4.6" cy="9.4" r="2.6"/><path d="M 6.5 7.5 L 11.4 2.6 M 9.4 4.6 l 1.6 1.6"/></svg>
      sftp access <b>{depth.server?.name ?? ""}</b>
    </h2>

    <div class="sftp-field">
      <span>connection</span>
      <div class="sftp-line">
        <div class="cfg-ro">{uri || (host && port ? "sftp is off for this server" : "unavailable")}</div>
        <button
          class="mini-act res copy-act{copied === 'uri' ? ' did' : ''}"
          disabled={!uri}
          onclick={() => copy("uri", uri)}
          aria-label="Copy connection string"
          ><span class="ns-stack"><span class="ns-w on">copy</span><span class="ns-w off">copied</span></span></button
        >
      </div>
    </div>

    <div class="sftp-parts">
      <div class="kv">
        <span>host</span><b>{on && host ? host : "—"}</b>
        <button class="mini-act res copy-act{copied === 'host' ? ' did' : ''}" disabled={!on || !host} onclick={() => copy("host", host)} aria-label="Copy host"
          ><span class="ns-stack"><span class="ns-w on">copy</span><span class="ns-w off">copied</span></span></button
        >
      </div>
      <div class="kv">
        <span>port</span><b>{on && port ? port : "—"}</b>
        <button class="mini-act res copy-act{copied === 'port' ? ' did' : ''}" disabled={!on || !port} onclick={() => copy("port", String(port))} aria-label="Copy port"
          ><span class="ns-stack"><span class="ns-w on">copy</span><span class="ns-w off">copied</span></span></button
        >
      </div>
      <div class="kv">
        <span>user</span><b title={user}>{user || "—"}</b>
        <button class="mini-act res copy-act{copied === 'user' ? ' did' : ''}" disabled={!user} onclick={() => copy("user", user)} aria-label="Copy username"
          ><span class="ns-stack"><span class="ns-w on">copy</span><span class="ns-w off">copied</span></span></button
        >
      </div>
    </div>

    <div class="sftp-rule" aria-hidden="true"></div>

    <div class="sftp-auth">
      <div class="sftp-row">
        <span>password</span>
        <em>{depth.sftpPassword ? "rotated just now" : sftp?.has_password ? "set" : "not set"}</em>
        <button class="mini-act res" disabled={depth.sftpBusy} onclick={sftpRotate}
          >{sftp?.has_password ? "rotate" : "generate"}</button
        >
      </div>

      {#if depth.sftpPassword}
        <div class="sftp-reveal on">
          <div class="sftp-pw">
            <code>{depth.sftpPassword}</code>
            <button
              class="mini-act res copy-act{copied === 'pw' ? ' did' : ''}"
              onclick={() => copy("pw", depth.sftpPassword ?? "")}
              aria-label="Copy password"
              ><span class="ns-stack"><span class="ns-w on">copy</span><span class="ns-w off">copied</span></span></button
            >
          </div>
          <p class="sftp-once">
            copy it now — the panel keeps only a hash, so this is the one time it exists on screen.
          </p>
        </div>
      {/if}

      <div class="sftp-row">
        <span>keys</span>
        <em>{sftp?.keys?.length ?? 0} authorized</em>
        <button class="mini-act" disabled={depth.sftpBusy} onclick={() => (adding = !adding)}
          >{adding ? "cancel" : "add key"}</button
        >
      </div>

      {#each sftp?.keys ?? [] as key (key)}
        <div class="sftp-key">
          <span title={key}>{key}</span>
          <button
            class="mini-act"
            disabled={depth.sftpBusy}
            onclick={() => sftpRemoveKey(key)}
            aria-label="Remove this key">remove</button
          >
        </div>
      {/each}

      {#if adding}
        <div class="cfg-row">
          <span>public key</span>
          <input
            class="cfg-in"
            placeholder="ssh-ed25519 AAAA… you@host"
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
            bind:value={newKey}
            onkeydown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                void submitKey();
              }
            }}
          />
          <div class="sftp-acts">
            <button class="cfg-btn solid" disabled={!newKey.trim() || depth.sftpBusy} onclick={submitKey}
              >add key</button
            >
          </div>
        </div>
      {/if}
    </div>

    {#if depth.sftpError}
      <p class="sftp-once" role="status" aria-live="polite">{depth.sftpError}</p>
    {/if}

    <p class="sftp-note">
      password and keys both work; either is enough. access is chrooted to this server's data
      directory — nothing above it is reachable.{#if lanOnly}
        this node reaches the panel through a tunnel and its sftp listener is not proxied, so the
        address above only works from the node's own network.{/if}
    </p>

    <div class="sftp-acts">
      {#if on}
        <button class="cfg-btn danger" disabled={depth.sftpBusy} onclick={sftpDisable}>disable sftp</button>
      {/if}
      <button class="cfg-btn ghost" onclick={sftpHide}>close</button>
    </div>
  </div>
</div>
