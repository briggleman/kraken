<script lang="ts">
  import { istyle } from "@/lib/istyle";
  import { untrack } from "svelte";
  import { ui, closeSheet, openConfirm, CD_NODE_BODY } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";
  import { api } from "@/api/client";
  import { fleet } from "@/lib/fleet.svelte";
  import type { NodeConfig, NodeConfigUpdate } from "@/api/types";

  type Res = { cls: "ok" | "bad"; text: string } | null;

  function errMsg(e: unknown): string {
    return e instanceof Error ? e.message : "request failed";
  }

  // the node this sheet is about — set by the node band before opening
  const node = $derived(fleet.nodes.find((n) => n.id === ui.nodeCfgId) ?? null);

  let cfg = $state<NodeConfig | null>(null);
  let loadErr = $state<string | null>(null);

  // capacity — lives on the node record, saved via PATCH /nodes/{id}
  let memMB = $state("");
  let portStart = $state("");
  let portEnd = $state("");

  // system config — GET/PUT /nodes/{id}/config; secrets are write-only
  let target = $state("local");
  let backupDir = $state("");
  let nodeName = $state("");
  let sftpHost = $state("");
  let sftpUser = $state("");
  let sftpPassword = $state("");
  let sftpKey = $state("");
  let sftpBase = $state("");
  let sftpKnownHost = $state("");
  let replicate = $state(false);
  let steamUser = $state("");
  let steamPass = $state("");

  let busy = $state(false);
  let res = $state<Res>(null);

  function seed(c: NodeConfig) {
    cfg = c;
    target = c.backup_target || "local";
    backupDir = c.backup_dir ?? "";
    sftpHost = c.sftp_host ?? "";
    sftpUser = c.sftp_user ?? "";
    sftpBase = c.sftp_base_path ?? "";
    sftpKnownHost = c.sftp_known_host_key ?? "";
    replicate = c.replicate_to_sftp;
    steamUser = c.steam_username ?? "";
  }

  async function load(id: string) {
    try {
      seed(await api.getNodeConfig(id));
    } catch (e) {
      loadErr = errMsg(e);
    }
  }

  // seed on open only — the fleet poll must not clobber in-progress edits,
  // so fleet reads inside are untracked
  $effect(() => {
    if (!ui.open.nodeCfg) return;
    const id = ui.nodeCfgId;
    untrack(() => {
      cfg = null;
      res = null;
      loadErr = null;
      sftpPassword = "";
      sftpKey = "";
      steamPass = "";
      const n = fleet.nodes.find((x) => x.id === id) ?? null;
      memMB = n && n.total_memory_mb ? String(n.total_memory_mb) : "";
      const range = n?.ports?.ranges?.[0];
      portStart = range ? String(range.start) : "";
      portEnd = range ? String(range.end) : "";
      nodeName = n?.name ?? "";
      if (id) void load(id);
      else loadErr = "no node selected";
    });
  });

  const showSftp = $derived(target === "sftp" || replicate);

  async function save() {
    const id = ui.nodeCfgId;
    if (!id || busy) return;
    busy = true;
    res = null;
    let capacitySaved = false;
    // capacity first, changed fields only — a validation refusal aborts the save
    try {
      const n = fleet.nodes.find((x) => x.id === id);
      const patch: {
        name?: string;
        total_memory_mb?: number;
        port_start?: number;
        port_end?: number;
      } = {};
      if (nodeName.trim() === "") {
        res = { cls: "bad", text: "node name cannot be blank." };
        busy = false;
        return;
      }
      if (n && nodeName.trim() !== n.name) patch.name = nodeName.trim();
      if (memMB.trim() !== "" && n && +memMB !== n.total_memory_mb) patch.total_memory_mb = +memMB;
      if (portStart.trim() !== "" || portEnd.trim() !== "") {
        if (portStart.trim() === "" || portEnd.trim() === "") {
          res = { cls: "bad", text: "set both port range fields (or clear both to leave unchanged)." };
          busy = false;
          return;
        }
        const range = n?.ports?.ranges?.[0];
        if (+portStart !== range?.start || +portEnd !== range?.end) {
          patch.port_start = +portStart;
          patch.port_end = +portEnd;
        }
      }
      if (Object.keys(patch).length) {
        await api.updateNode(id, patch);
        capacitySaved = true;
      }
    } catch (e) {
      res = { cls: "bad", text: errMsg(e) };
      busy = false;
      return;
    }
    // config — changed fields only; blank secret inputs keep the stored value
    const input: NodeConfigUpdate = {};
    if (target !== (cfg?.backup_target || "local")) input.backup_target = target;
    if (backupDir !== (cfg?.backup_dir ?? "")) input.backup_dir = backupDir;
    if (sftpHost !== (cfg?.sftp_host ?? "")) input.sftp_host = sftpHost;
    if (sftpUser !== (cfg?.sftp_user ?? "")) input.sftp_user = sftpUser;
    if (sftpBase !== (cfg?.sftp_base_path ?? "")) input.sftp_base_path = sftpBase;
    if (sftpKnownHost !== (cfg?.sftp_known_host_key ?? "")) input.sftp_known_host_key = sftpKnownHost;
    if (replicate !== (cfg?.replicate_to_sftp ?? false)) input.replicate_to_sftp = replicate;
    if (steamUser !== (cfg?.steam_username ?? "")) input.steam_username = steamUser;
    if (sftpPassword) input.sftp_password = sftpPassword;
    if (sftpKey) input.sftp_private_key = sftpKey;
    if (steamPass) input.steam_password = steamPass;
    if (!Object.keys(input).length) {
      res = { cls: "ok", text: capacitySaved ? "capacity saved." : "nothing changed." };
      busy = false;
      return;
    }
    try {
      const r = await api.updateNodeConfig(id, input);
      seed(r);
      sftpPassword = "";
      sftpKey = "";
      steamPass = "";
      if (!r.applied) res = { cls: "ok", text: r.apply_detail || "saved — applies when the node next checks in." };
      else if (r.apply_ok) res = { cls: "ok", text: `saved and applied.${r.apply_detail ? " " + r.apply_detail : ""}` };
      else res = { cls: "bad", text: `saved, but the node is unreachable: ${r.apply_detail}` };
    } catch (e) {
      res = { cls: "bad", text: errMsg(e) };
    }
    busy = false;
  }
</script>

<div
  class="sheet"
  class:open={!!ui.open.nodeCfg}
  id="nodeCfg"
  role="dialog"
  aria-modal="true"
  aria-labelledby="nodeCfgTitle"
  use:istyle={`--ox: ${ui.open.nodeCfg?.ox ?? '50%'}; --oy: ${ui.open.nodeCfg?.oy ?? '50%'}`}
  use:sheetFocus
>
  <div class="depth-head">
    <button class="surface-btn" onclick={() => closeSheet("nodeCfg")}>
      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6 10 V 2 M 2 6 L 6 2 L 10 6"/></svg>
      surface
    </button>
    <h2 class="depth-title" id="nodeCfgTitle">node settings <b class="node-name-inline">{node?.name ?? "—"}</b></h2>
    {#if loadErr}
      <div class="prefs-note"><span class="synthetic">config failed to load — {loadErr}</span></div>
    {/if}
  </div>
  <div class="sheet-body">
    <section class="prefs-group" aria-label="Identity">
      <div class="cfg-head"><h3 class="pane-label">identity</h3></div>
      <div class="cfg">
        <label class="cfg-row">
          <span>node name</span>
          <input class="cfg-in" type="text" maxlength="64" bind:value={nodeName} />
          <p class="cfg-help">display only — servers reference this node by id, so renaming never disturbs what is running on it.</p>
        </label>
      </div>
    </section>

    <section class="prefs-group" aria-label="Capacity">
      <div class="cfg-head"><h3 class="pane-label">capacity</h3></div>
      <div class="cfg">
        <p class="cfg-desc">schedulable capacity, where this node stores backups, and whether they are mirrored off-node.</p>
        <label class="cfg-row">
          <span>total memory (mb)</span>
          <input class="cfg-in" type="text" bind:value={memMB} />
          <p class="cfg-help">memory the scheduler may hand to game servers. {node?.allocated_memory_mb ?? 0}MB is already reserved by servers on this node.</p>
        </label>
        <label class="cfg-row"><span>port start</span><input class="cfg-in" type="text" placeholder="28000" bind:value={portStart} /></label>
        <label class="cfg-row">
          <span>port end</span>
          <input class="cfg-in" type="text" placeholder="28999" bind:value={portEnd} />
          <p class="cfg-help">game ports the scheduler allocates from. changing the range never touches running servers — their ports stay reserved. nodes sharing one ip need non-overlapping ranges.</p>
        </label>
      </div>
    </section>

    <section class="prefs-group" aria-label="Backups">
      <div class="cfg-head"><h3 class="pane-label">backups</h3></div>
      <div class="cfg">
        <label class="cfg-row">
          <span>backup target</span>
          <select class="cfg-in" bind:value={target}><option value="local">local disk</option><option value="sftp">sftp remote</option></select>
        </label>
        <label class="cfg-row">
          <span>backup dir (optional)</span>
          <input class="cfg-in" type="text" placeholder="leave blank for the node default" bind:value={backupDir} />
          <p class="cfg-help">supports {"{{SLUG}}"} (the game's slug) for per-game folders, e.g. /var/backups/{"{{SLUG}}"}.</p>
        </label>
        <label class="tgl"><input type="checkbox" bind:checked={replicate} /><i></i>mirror backups to an sftp remote</label>
      </div>
    </section>

    {#if showSftp}
      <section class="prefs-group" aria-label="SFTP remote">
        <div class="cfg-head"><h3 class="pane-label">sftp remote</h3><span class="cfg-badge enc">encrypted</span>{#if cfg?.sftp_password_configured || cfg?.sftp_key_configured}<span class="cfg-badge ok">credential stored</span>{/if}</div>
        <div class="cfg">
          <label class="cfg-row"><span>host</span><input class="cfg-in" type="text" placeholder="host:port (default 22)" autocomplete="off" bind:value={sftpHost} /></label>
          <label class="cfg-row"><span>username</span><input class="cfg-in" type="text" autocomplete="off" bind:value={sftpUser} /></label>
          <label class="cfg-row"><span>{cfg?.sftp_password_configured ? "replace password" : "password"}</span><input class="cfg-in" type="password" autocomplete="off" placeholder={cfg?.sftp_password_configured ? "•••••••• (leave blank to keep)" : "or use a private key"} bind:value={sftpPassword} /></label>
          <label class="cfg-row">
            <span>{cfg?.sftp_key_configured ? "replace private key (pem)" : "private key (pem, optional)"}</span>
            <input class="cfg-in" type="password" autocomplete="off" placeholder={cfg?.sftp_key_configured ? "•••••••• (leave blank to keep stored key)" : "paste the full pem key"} bind:value={sftpKey} />
            <p class="cfg-help">write-only — the stored key is never shown again. paste the whole pem, newlines included.</p>
          </label>
          <label class="cfg-row">
            <span>base path</span>
            <input class="cfg-in" type="text" placeholder="/backups/kraken" bind:value={sftpBase} />
            <p class="cfg-help">supports {"{{SLUG}}"} (the game's slug), e.g. /backups/{"{{SLUG}}"}.</p>
          </label>
          <label class="cfg-row">
            <span>known host key</span>
            <input class="cfg-in" type="text" placeholder="ssh-ed25519 AAAAC3Nz…" autocomplete="off" bind:value={sftpKnownHost} />
            <p class="cfg-help">pin the sftp host's ssh public key to defeat mitm — authorized_keys format, grab it with <code>ssh-keyscan -t ed25519 &lt;host&gt;</code>. leave blank to trust-on-use (a warning is logged on every connect).</p>
          </label>
        </div>
      </section>
    {/if}

    <section class="prefs-group" aria-label="Steam account">
      <div class="cfg-head"><h3 class="pane-label">steam account</h3><span class="cfg-badge enc">encrypted</span>{#if cfg?.steam_configured}<span class="cfg-badge ok">configured</span>{/if}</div>
      <div class="cfg">
        <p class="cfg-desc">needed only for games whose dedicated server is not anonymous-downloadable. enter a steam account that owns the game; provide the steam guard code at deploy time.</p>
        <label class="cfg-row"><span>username</span><input class="cfg-in" type="text" autocomplete="off" bind:value={steamUser} /></label>
        <label class="cfg-row"><span>{cfg?.steam_configured ? "replace password" : "password"}</span><input class="cfg-in" type="password" autocomplete="off" placeholder={cfg?.steam_configured ? "•••••••• (leave blank to keep)" : ""} bind:value={steamPass} /></label>
      </div>
      <!-- The Opposite Ends Rule: the destructive control takes the far end of the
           row, opposite the one committing control, with a hairline between them. -->
      <div class="cfg-actions acts-split">
        <button
          class="cfg-btn danger"
          disabled={!node}
          onclick={(e) => openConfirm(nodeName || "this node", e.currentTarget, { noun: "node", body: CD_NODE_BODY })}
          >delete node</button
        >
        {#if res}<span class="cfg-note wz-res shown {res.cls}">{res.text}</span>{:else}<span class="cfg-note">blank secret fields keep their stored values</span>{/if}
        <button class="cfg-btn ghost" onclick={() => closeSheet("nodeCfg")}>close</button>
        <button class="cfg-btn solid" disabled={busy || !cfg} onclick={() => void save()}>{busy ? "saving…" : "save & apply"}</button>
      </div>
    </section>
  </div>
</div>
