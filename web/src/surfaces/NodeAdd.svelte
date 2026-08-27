<script lang="ts">
  import { istyle } from "@/lib/istyle";
  import { ui, closeSheet } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";
  import { Enrollment, type EnrollMode } from "@/lib/enroll.svelte";

  // Real enrollment: the token, CA fingerprint, and install commands are the
  // panel's own; the status badge follows the agent's actual lifecycle and a
  // redeemed token registers the node.
  const enroll = new Enrollment();
  const COPY_LABEL = "copy to clipboard";

  let mode = $state<EnrollMode>("tunnel");
  let os = $state<"linux" | "windows">("linux");

  const panelOrigin = location.origin;
  const shown = $derived(enroll.status !== "idle");

  let cmdLinuxEl: HTMLPreElement | undefined = $state();
  let cmdWinEl: HTMLPreElement | undefined = $state();
  let copyLinuxLabel = $state(COPY_LABEL);
  let copyWinLabel = $state(COPY_LABEL);

  async function copyCmd(el: HTMLPreElement | undefined, set: (v: string) => void) {
    try {
      await navigator.clipboard.writeText(el?.textContent ?? "");
      set("copied");
    } catch {
      set("select and copy");
    }
    setTimeout(() => set(COPY_LABEL), 1800);
  }

  // leaving the sheet stops the polling; the token stays valid server-side
  $effect(() => {
    if (!ui.open.nodeAdd) enroll.stop();
  });

  const tok = $derived(enroll.token || "<enrollment-token>");
  const fp = $derived(enroll.caFingerprint || "<ca-fingerprint>");
</script>

<div
  class="sheet"
  class:open={!!ui.open.nodeAdd}
  id="nodeAdd"
  role="dialog"
  aria-modal="true"
  aria-labelledby="nodeAddTitle"
  use:istyle={`--ox: ${ui.open.nodeAdd?.ox ?? '50%'}; --oy: ${ui.open.nodeAdd?.oy ?? '50%'}`}
  use:sheetFocus
>
  <div class="depth-head">
    <button class="surface-btn" onclick={() => closeSheet("nodeAdd")}>
      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6 10 V 2 M 2 6 L 6 2 L 10 6"/></svg>
      surface
    </button>
    <h2 class="depth-title" id="nodeAddTitle">add a node</h2>
    <div class="prefs-note"></div>
  </div>
  <div class="sheet-body">
    <section class="prefs-group" aria-label="Connect a remote node">
      <div class="cfg-head"><h3 class="pane-label">connect a remote node</h3></div>
      <div class="cfg">
        <p class="cfg-desc">generate a one-time enrollment token, pick the target os, and run the commands on the remote host. the node names itself from its KRAKEN_NODE_ID.</p>
        <div class="cfg-row">
          <span>connection</span>
          <div class="opt-set">
            <label class="opt opt-inline"><input type="radio" name="nconn" class="opt-r-in" value="tunnel" bind:group={mode} /><span class="opt-t">node dials the panel</span><span class="opt-n">no inbound ports — works behind nat</span></label>
            <label class="opt opt-inline"><input type="radio" name="nconn" class="opt-r-in" value="direct" bind:group={mode} /><span class="opt-t">panel dials the node</span><span class="opt-n">needs inbound 9090 open on the node</span></label>
          </div>
        </div>
        {#if enroll.lines.length}
          <div class="wz-log shown" role="status" aria-live="polite">
            {#each enroll.lines as entry}
              <span class={entry.pending ? "pending" : ""}><i>{entry.pending ? "▸" : "✓"}</i><span>{entry.text}</span></span>
            {/each}
          </div>
        {/if}
        {#if enroll.error}
          <p class="cfg-help" use:istyle={"color: var(--crisis)"}>{enroll.error}</p>
        {/if}
      </div>
      <div class="cfg-actions actions-lead">
        <button
          class="cfg-btn solid"
          id="genTokenBtn"
          disabled={enroll.status === "waiting" || enroll.status === "redeemed" || enroll.status === "registered"}
          onclick={() => void enroll.generate(mode, os)}>generate enrollment token</button
        >
        <span class="cfg-note">the token is valid once, for 15 minutes</span>
      </div>
    </section>

    <section class="prefs-group token-wrap" class:shown id="tokenWrap" aria-label="Enrollment token">
      <div class="cfg-head">
        <h3 class="pane-label">one-time enrollment token — valid 15 minutes</h3>
        <span class="cfg-badge enc">one-time</span>
        <span class="cfg-badge env" id="enrollState">{enroll.badge}</span>
      </div>
      <div class="cfg">
        <div class="cfg-ro" id="tokenVal">{enroll.token || "—"}</div>
        <div class="os-tabs">
          <div class="os-tablist" role="tablist">
            <label class="os-tab"><input type="radio" name="nos" class="os-r-in" value="linux" bind:group={os} />linux install</label>
            <label class="os-tab"><input type="radio" name="nos" class="os-r-in" value="windows" bind:group={os} />windows install</label>
          </div>
          <div class="os-panel os-linux">
            <h4 class="os-step">1 · install + enroll + start (one command)</h4>
            <div class="cmd-wrap">
              <pre class="cmd" id="cmdLinux" bind:this={cmdLinuxEl}>curl -fsSL https://raw.githubusercontent.com/briggleman/kraken/main/deploy/install.sh | \
  sudo bash -s -- --role agent --panel-url {panelOrigin} \
  --enroll-token <b class="tok">{tok}</b> \
  --ca-fingerprint <b>{fp}</b>{mode === "tunnel" ? " \\\n  --tunnel" : ""}</pre>
              <button class="cfg-btn ghost cmd-copy" data-copy="cmdLinux" onclick={() => copyCmd(cmdLinuxEl, (v) => (copyLinuxLabel = v))}>{copyLinuxLabel}</button>
            </div>
            <p class="cfg-help">the node takes its name from KRAKEN_NODE_ID. the agent dials the panel's tunnel listener on port 9443. if the url above is a proxied domain (cloudflare, nginx), pass <code>--tunnel-addr &lt;panel-lan-ip&gt;:9443</code> instead — proxies can't carry the raw mtls tunnel.</p>
          </div>
          <div class="os-panel os-win">
            <h4 class="os-step">1 · install + enroll + start (elevated powershell, one command)</h4>
            <div class="cmd-wrap">
              <pre class="cmd" id="cmdWin" bind:this={cmdWinEl}>iwr -useb https://raw.githubusercontent.com/briggleman/kraken/main/deploy/windows/install.ps1 -OutFile $env:TEMP\kraken-install.ps1
  powershell -ExecutionPolicy Bypass -File $env:TEMP\kraken-install.ps1 `
  -PanelUrl {panelOrigin} `
  -Token <b class="tok">{tok}</b> `
  -CaFingerprint <b>{fp}</b>{mode === "tunnel" ? " `\n  -Tunnel" : ""}</pre>
              <button class="cfg-btn ghost cmd-copy" data-copy="cmdWin" onclick={() => copyCmd(cmdWinEl, (v) => (copyWinLabel = v))}>{copyWinLabel}</button>
            </div>
            <p class="cfg-help">the node takes its name from KRAKEN_NODE_ID. the agent dials the panel's tunnel listener on port 9443. if the url above is a proxied domain (cloudflare, nginx), pass <code>-TunnelAddr &lt;panel-lan-ip&gt;:9443</code> instead — proxies can't carry the raw mtls tunnel.</p>
          </div>
        </div>
      </div>
    </section>
  </div>
</div>
