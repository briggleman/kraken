<script lang="ts">
  import { istyle } from "@/lib/istyle";
  import {
    depth,
    stream,
    surface,
    power,
    filesGo,
    filesUpload,
    backupCreate,
    backupRestore,
    backupDelete,
    scheduleToggle,
    scheduleDelete,
    scheduleAdd,
    dnsPublish,
    dnsUnpublish,
    forwardSet,
    settingsApply,
    reinstall,
    sftpShow,
  } from "@/lib/depth.svelte";
  import { openConfirm } from "@/lib/state.svelte";
  import { specOf } from "@/lib/fleet.svelte";
  import { fmtClock, fmtGb, fmtSize, fmtUptime, fmtWhen } from "@/lib/fmt";
  import type { ScheduleAction } from "@/api/types";

  const server = $derived(depth.server);
  const name = $derived(server?.name ?? "");
  const running = $derived(server?.state === "running");
  const stats = $derived(stream.stats);
  // While a server provisions, and after a failed attempt, the console pane
  // carries the installer's output rather than a container's stdout.
  const installing = $derived(
    server?.state === "installing" || server?.state === "install_failed",
  );
  // The Panel refuses a restore unless the server is stopped — the agent swaps
  // out the very files a running game holds open — so the button carries the
  // reason instead of offering a click that comes back 409.
  const restoreBlocked = $derived.by(() => {
    const st = server?.state;
    if (st === "offline" || st === "crashed" || st === "install_failed") return "";
    return `stop the server before restoring (state: ${st ?? "unknown"})`;
  });
  // The stored reason usually already opens with "install failed:", which the
  // notice's own heading says — strip it so the sentence reads once, and close
  // it so the pointer to the log that follows is a separate sentence.
  // Whether pointing the operator at the console pane is honest: the buffer is
  // in-Panel memory, so a restart between the attempt and the drill-in leaves
  // nothing to read. Lines already in hand, or a stream still working, count.
  const haveInstallLog = $derived(
    stream.lines.length > 0 || (stream.status !== "ended" && stream.status !== "idle"),
  );
  const failReason = $derived.by(() => {
    const raw = (server?.last_error ?? "").replace(/^install failed:\s*/i, "").trim();
    if (!raw) return "";
    return /[.!?]$/.test(raw) ? raw : raw + ".";
  });

  function ui_now(): string {
    return new Date().toLocaleTimeString("en-US", { hour12: false });
  }

  let surfaceBtn: HTMLButtonElement;
  let consoleLog: HTMLDivElement | undefined = $state();
  let cmdInput = $state("");

  $effect(() => {
    if (depth.open) surfaceBtn.focus();
  });

  // keep the log pinned to the newest line
  $effect(() => {
    stream.lines.length;
    if (consoleLog) consoleLog.scrollTop = consoleLog.scrollHeight;
  });

  function sendCommand(e: KeyboardEvent) {
    if (e.key !== "Enter" || !cmdInput.trim()) return;
    stream.send(cmdInput.trim());
    cmdInput = "";
  }

  // vitals — real stream stats
  const cpuPct = $derived(stats ? Math.round(stats.cpu_percent) : 0);
  // Docker reports cpu across all cores, so a busy server legitimately reads
  // past 100 (Valheim generating a world sat at 104). The number is true and
  // stays as measured; the rail is a 0-100 track, so only the fill is capped —
  // a bar cannot be more than full, and clamping the readout would lie.
  const cpuRail = $derived(Math.max(0, Math.min(100, cpuPct)));
  const memPct = $derived(
    stats && stats.mem_limit_mb > 0 ? Math.round((stats.mem_used_mb / stats.mem_limit_mb) * 100) : 0,
  );
  const memRail = $derived(Math.max(0, Math.min(100, memPct)));
  // net rate from successive cumulative byte counters. lastNet is a plain
  // variable, NOT $state: the effect both reads and writes it, and a reactive
  // read-write in one effect is an infinite update loop.
  let lastNet: { ts: number; bytes: number } | null = null;
  let netRate = $state(0); // Mb/s
  $effect(() => {
    if (!stats) {
      lastNet = null;
      netRate = 0;
      return;
    }
    const bytes = stats.net_rx_bytes + stats.net_tx_bytes;
    if (lastNet && stats.ts > lastNet.ts) {
      const mbits = ((bytes - lastNet.bytes) * 8) / 1e6;
      const secs = (stats.ts - lastNet.ts) / 1000;
      if (secs > 0 && mbits >= 0) netRate = mbits / secs;
    }
    lastNet = { ts: stats.ts, bytes };
  });

  const meta = $derived({
    up: running && stats ? fmtUptime(stats.uptime_seconds) : "—",
    players:
      server && stats?.players_known
        ? `${stats.players}/${stats.max_players}`
        : server?.players_known
          ? `${server.players ?? 0}/${server.max_players ?? 0}`
          : "—",
    port: server ? Object.values(server.ports ?? {})[0] ?? "—" : "—",
    ver: server ? `v${specOf(server)?.version ?? "?"}` : "—",
  });

  // files
  const crumbs = $derived.by(() => {
    const dir = depth.filesDir === "." ? "" : depth.filesDir;
    return dir.split("/").filter(Boolean);
  });
  function crumbPath(i: number) {
    return crumbs.slice(0, i + 1).join("/") || ".";
  }
  let uploadInput: HTMLInputElement | undefined = $state();

  // settings — editable copies of the real values/variables
  let edited = $state<Record<string, string>>({});
  let editedVars = $state<Record<string, string>>({});
  let settingsNote = $state<string | null>(null);
  $effect(() => {
    depth.settings; // reset edits when a new server's settings load
    edited = {};
    editedVars = {};
    settingsNote = null;
  });
  function fieldValue(key: string, fallback: string | undefined): string {
    return edited[key] ?? depth.settings?.values[key] ?? fallback ?? "";
  }
  async function applySettings() {
    settingsNote = await settingsApply({ ...edited }, { ...editedVars });
    if (settingsNote) {
      edited = {};
      editedVars = {};
    }
  }
  function revertSettings() {
    edited = {};
    editedVars = {};
    settingsNote = null;
  }

  // schedules form
  const CRON_PRESETS: { cron: string; label: string }[] = [
    { cron: "0 * * * *", label: "hourly" },
    { cron: "0 */6 * * *", label: "every 6h" },
    { cron: "0 4 * * *", label: "daily 04:00" },
    { cron: "0 4 * * 0", label: "sun 04:00" },
  ];
  let schFormOpen = $state(false);
  let schName = $state("");
  let schAction = $state<ScheduleAction>("restart");
  let schCmd = $state("");
  let schCron = $state("0 4 * * *");
  let schEnabled = $state(true);
  let schNameEl: HTMLInputElement | undefined = $state();

  function schOpenForm() {
    schFormOpen = true;
    setTimeout(() => schNameEl?.focus());
  }
  async function schAddRow() {
    const ok = await scheduleAdd({
      name: schName.trim() || schAction,
      action: schAction,
      cron: schCron.trim() || "0 4 * * *",
      command: schAction === "command" ? schCmd.trim() || undefined : undefined,
      enabled: schEnabled,
    });
    if (ok) {
      schFormOpen = false;
      schName = "";
      schCmd = "";
    }
  }
  function schNext(t: { enabled: boolean; next_run_at?: string }): string {
    if (!t.enabled) return "paused";
    if (!t.next_run_at) return "—";
    return fmtWhen(new Date(t.next_run_at).getTime()).replace("today ", "");
  }

  // dns + forwards — local field/toggle state, resynced from the server whenever
  // depth.dns changes (publish/unpublish refetch it) and explicitly after every
  // call: a one-way `checked={…}` attribute is memoized by the compiler, so a
  // refused call would leave the DOM toggle stuck showing a state the server
  // never reached.
  let dnsName = $state("");
  let dnsSvc = $state("");
  let dnsPublished = $state(false);
  let portOpen = $state<Record<string, boolean>>({});
  $effect(() => {
    dnsName = depth.dns?.dns?.name ?? "";
    dnsSvc = depth.dns?.dns?.service ?? "";
    dnsPublished = !!depth.dns?.dns;
    const fwds = depth.dns?.forwards ?? {};
    const next: Record<string, boolean> = {};
    for (const name of Object.keys(depth.dns?.ports ?? {})) next[name] = fwds[name]?.enabled ?? false;
    portOpen = next;
  });
  async function dnsFlip() {
    if (dnsPublished && !dnsName.trim()) {
      depth.error = "enter a hostname before publishing";
      dnsPublished = false;
      return;
    }
    if (dnsPublished) await dnsPublish(dnsName.trim(), dnsSvc.trim() || undefined);
    else await dnsUnpublish();
    dnsPublished = !!depth.dns?.dns; // resync to reality — success or failure
  }
  async function dnsBlur() {
    // editing while published re-publishes with the new value on blur
    const cur = depth.dns?.dns;
    if (!cur) return;
    const nm = dnsName.trim();
    if (nm && (nm !== cur.name || dnsSvc.trim() !== (cur.service ?? ""))) {
      await dnsPublish(nm, dnsSvc.trim() || undefined);
    }
  }
  async function portFlip(portName: string) {
    await forwardSet(portName, portOpen[portName] ?? false);
    // resync: success lands the new forwards map, a refusal snaps the pill back
    portOpen[portName] = depth.dns?.forwards?.[portName]?.enabled ?? false;
  }

  // endpoint copy flip
  const endpoint = $derived(
    depth.dns ? `${depth.dns.target_host}:${meta.port}` : `${meta.port}`,
  );
  let copied = $state(false);
  function copyEndpoint() {
    if (copied) return;
    if (navigator.clipboard) navigator.clipboard.writeText(endpoint).catch(() => {});
    copied = true;
    setTimeout(() => (copied = false), 1600);
  }

  const backupsBusy = $derived(depth.backups.some((b) => b.state === "pending"));

  // Mirrors the agent's fixed retention cap (internal/agent/docker.go
  // backupRetentionKeep). The agent enforces it — the UI only reads it to state
  // the policy and warn before the create button. There is no per-server knob yet.
  const BACKUP_KEEP = 5;

  // The word at a backup's right edge is the state of its OFF-NODE copy, not a
  // verdict on the archive — each state the agent reports gets its own word and
  // colour, so a failed mirror can never read as a green "ok" (The Spoken Mirror
  // Rule). A capture that failed outright shows "failed" and cannot be restored.
  function bkState(b: { state: string; replication: string }): {
    word: string;
    cls: string;
    failed: boolean;
  } {
    if (b.state === "failed") return { word: "failed", cls: "failed", failed: true };
    switch (b.replication) {
      case "pending":
        return { word: "mirroring", cls: "mirroring", failed: false };
      case "done":
        return { word: "mirrored", cls: "", failed: false };
      case "failed":
        return { word: "mirror failed", cls: "mirror-failed", failed: false };
      default:
        return { word: "ok", cls: "", failed: false }; // replication not configured
    }
  }
  // the mirror line in a backup's detail — the node's off-node target plus this
  // archive's replication state, or null when this archive was never mirrored
  function bkMirror(rep: string, target: string): string | null {
    if (rep === "") return null;
    const dest = target || "off-node copy";
    switch (rep) {
      case "pending":
        return `${dest} · in progress`;
      case "done":
        return `${dest} · done`;
      case "failed":
        return `${dest} · failed`;
      default:
        return null;
    }
  }

  // What a backup of this game captures, from the spec's current backup globs.
  // No block means the whole data dir. Excludes are shown by their distinguishing
  // tail segment ("Pal/Saved/Logs/**" → "Logs") so the line reads at a glance.
  function fmtCaptured(bk?: { include?: string[]; exclude?: string[] }): string {
    if (!bk || !bk.include?.length) return "whole data dir";
    const inc = bk.include.join(", ");
    if (!bk.exclude?.length) return inc;
    const tail = (g: string) => {
      const seg = g
        .replace(/\/\*+$/, "")
        .split("/")
        .filter(Boolean);
      return seg[seg.length - 1] || g;
    };
    return `${inc} − ${bk.exclude.map(tail).join(", ")}`;
  }
  const capturedGlobs = $derived(server ? fmtCaptured(specOf(server)?.backup) : "whole data dir");

  // Retention is a fact about the list, derived — never written down twice. Only
  // archives that kept data (state !== failed) hold a slot; a failed attempt
  // captured nothing, so it neither counts toward the cap nor is ever evicted.
  const keptBackups = $derived(depth.backups.filter((b) => b.state !== "failed"));
  const backupDiskBytes = $derived(keptBackups.reduce((n, b) => n + (b.size || 0), 0));
  const backupsAtCap = $derived(keptBackups.length >= BACKUP_KEEP);
  const replicationOn = $derived(depth.backupMirror !== "");
  // the archive the next backup will evict — the oldest slot-holder
  const oldestKept = $derived.by(() =>
    keptBackups.length
      ? keptBackups.reduce((a, b) => (a.created_ms <= b.created_ms ? a : b))
      : null,
  );

  // Per-row disclosure: undefined means "use the default" — the newest row (index
  // 0) opens so the detail pattern is discoverable, the rest stay collapsed.
  let backupOpen = $state<Record<string, boolean>>({});
  const isBackupOpen = (id: string, i: number): boolean => backupOpen[id] ?? i === 0;
  function toggleBackup(id: string, i: number) {
    backupOpen = { ...backupOpen, [id]: !isBackupOpen(id, i) };
  }
</script>

<div
  class="depth"
  class:open={depth.open}
  id="depth"
  role="dialog"
  aria-modal="true"
  aria-labelledby="depthTitle"
  use:istyle={`--ox: ${depth.origin.ox}; --oy: ${depth.origin.oy}`}
>
  <div class="depth-head">
    <button class="surface-btn" id="surfaceBtn" bind:this={surfaceBtn} onclick={surface}>
      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6 10 V 2 M 2 6 L 6 2 L 10 6"/></svg>
      surface
    </button>
    <h2 class="depth-title" id="depthTitle">{name}</h2>
    <div class="depth-meta">
      <span>state <b class={running ? "ok-txt" : ""} id="dState">{server?.state.replace("_", " ") ?? ""}</b></span>
      <span>uptime <b>{meta.up}</b></span>
      <span>players <b>{meta.players}</b></span>
      <span>port <b>{meta.port}</b></span>
      <span>ver <b>{meta.ver}</b></span>
    </div>
  </div>
  <div class="depth-body">
    {#key depth.serverId}
      <section class="console" aria-label="Server station">
        <input type="radio" name="stn" id="stnConsole" class="stn-r" checked />
        <input type="radio" name="stn" id="stnSettings" class="stn-r" />
        <input type="radio" name="stn" id="stnFiles" class="stn-r" />
        <div class="stn-tabs" role="tablist">
          <label for="stnConsole">{installing ? "install log" : "live console"}</label>
          <label for="stnSettings">settings</label>
          <label for="stnFiles">files</label>
          <!-- The chip is present whenever the node can answer for SFTP at all,
               not only when it is on: per the Far-End Affordance Rule it exists
               so the strip answers "is SFTP even on?" without being opened, and
               a chip that vanishes when disabled leaves no way to enable it. -->
          {#if depth.sftp}
            <button
              type="button"
              class="stn-chip"
              onclick={sftpShow}
              aria-label="SFTP connection details"
            >
              <svg viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="4.6" cy="9.4" r="2.6"/><path d="M 6.5 7.5 L 11.4 2.6 M 9.4 4.6 l 1.6 1.6"/></svg>
              <span class="stn-chip-k">sftp</span>
              <span class="stn-chip-v"
                >{depth.sftp.enabled && depth.sftp.port ? ":" + depth.sftp.port : "off"}</span
              >
            </button>
          {/if}
        </div>
        <div class="stn-panel p-console">
          <div class="console-log" id="consoleLog" bind:this={consoleLog}>
            {#if stream.status === "retrying"}
              <div><span class="t">{ui_now()}</span><span class="warn">[panel] stream lost — reconnecting</span></div>
            {/if}
            {#each stream.lines as line}
              <div><span class="t">{fmtClock(line.ts)}</span>{#if line.stream === "stderr" || line.stream === "error"}<span class="warn">{line.text}</span>{:else}{line.text}{/if}</div>
            {:else}
              {#if stream.status === "ended" || stream.status === "idle"}
                <div><span class="t">—</span>{installing
                    ? "no install output kept — the panel restarted since this attempt"
                    : "no output — server is dark"}</div>
              {/if}
            {/each}
          </div>
          <div class="console-in">
            <span>&gt;</span>
            <input
              type="text"
              placeholder={running
              ? "broadcast, save, kick <player> …"
              : installing
                ? "an install takes no commands"
                : "server is not running"}
              aria-label="Console command"
              disabled={!stream.connected || !running}
              bind:value={cmdInput}
              onkeydown={sendCommand}
            />
          </div>
        </div>
        <div class="stn-panel p-settings">
          <div class="cfg">
            {#if depth.settings}
              {#each depth.settings.groups as group (group.id)}
                {#each group.fields as field (field.key)}
                  {#if field.type === "bool"}
                    <label class="tgl{field.read_only ? ' is-locked' : ''}">
                      <input
                        type="checkbox"
                        checked={fieldValue(field.key, field.default) === "true"}
                        disabled={field.read_only}
                        onchange={(e) => (edited[field.key] = e.currentTarget.checked ? "true" : "false")}
                      /><i></i>{field.label || field.key}</label
                    >
                  {:else if field.type === "enum"}
                    <label class="cfg-row"
                      ><span>{field.label || field.key}</span><select
                        class="cfg-in"
                        disabled={field.read_only}
                        value={fieldValue(field.key, field.default)}
                        onchange={(e) => (edited[field.key] = e.currentTarget.value)}
                        >{#each field.options ?? [] as opt}<option value={opt}>{opt}</option>{/each}</select
                      ></label
                    >
                  {:else}
                    <label class="cfg-row"
                      ><span>{field.label || field.key}</span><input
                        class="cfg-in"
                        type={field.type === "password" ? "password" : "text"}
                        disabled={field.read_only}
                        value={fieldValue(field.key, field.default)}
                        oninput={(e) => (edited[field.key] = e.currentTarget.value)}
                      />{#if field.help}<p class="cfg-help">{field.help}</p>{/if}</label
                    >
                  {/if}
                {/each}
              {/each}
              {#each depth.settings.variables ?? [] as v (v.key)}
                <label class="cfg-row"
                  ><span>{v.label || v.key}</span><input
                    class="cfg-in"
                    type="text"
                    disabled={!v.user_editable}
                    value={editedVars[v.key] ?? v.value}
                    oninput={(e) => (editedVars[v.key] = e.currentTarget.value)}
                  /></label
                >
              {/each}
            {:else}
              <p class="cfg-desc">no settings surface — the spec defines none.</p>
            {/if}
          </div>
          <div class="cfg-foot">
            <span class="cfg-note"
              >{settingsNote ??
                (depth.settings?.hot_reload
                  ? "the game re-reads config live"
                  : "changes apply on next restart")}</span
            >
            <button class="cfg-btn ghost" onclick={revertSettings}>revert</button>
            <button
              class="cfg-btn solid"
              disabled={!Object.keys(edited).length && !Object.keys(editedVars).length}
              onclick={() => void applySettings()}>apply settings</button
            >
          </div>
        </div>
        <div class="stn-panel p-files">
          <div class="files-crumb">
            /<b
              role="button"
              tabindex="0"
              use:istyle={"cursor: pointer"}
              onclick={() => void filesGo(".")}
              onkeydown={(e) => e.key === "Enter" && void filesGo(".")}>{name.toLowerCase().replace(/\s+/g, "-")}</b
            >/{#each crumbs as c, i}<b
                role="button"
                tabindex="0"
                use:istyle={"cursor: pointer"}
                onclick={() => void filesGo(crumbPath(i))}
                onkeydown={(e) => e.key === "Enter" && void filesGo(crumbPath(i))}>{c}</b
              >/{/each}
          </div>
          <div class="files-list">
            {#each depth.files?.entries ?? [] as f (f.path)}
              <button
                class="f-row{f.is_dir ? ' dir' : ''}"
                onclick={() => f.is_dir && void filesGo(f.path)}
                ><span>{f.name}{f.is_dir ? "/" : ""}</span><span
                  >{f.is_dir ? "—" : fmtSize(f.size)}</span
                ><span>{fmtWhen(f.modified_ms)}</span></button
              >
            {:else}
              <button class="f-row" disabled><span>empty</span><span>—</span><span>—</span></button>
            {/each}
          </div>
          <div class="files-foot">
            <span
              >{#if stats?.disk_used_mb}<span>{fmtGb(stats.disk_used_mb)}</span>G on disk · {/if}{depth
                .files?.entries?.length ?? 0} items</span
            >
            <input
              type="file"
              multiple
              hidden
              bind:this={uploadInput}
              onchange={(e) => {
                const files = [...(e.currentTarget.files ?? [])];
                e.currentTarget.value = "";
                void filesUpload(files);
              }}
            />
            <button class="cfg-btn ghost" onclick={() => uploadInput?.click()}>upload</button>
          </div>
        </div>
      </section>
    {/key}
    <div class="depth-side">
      {#if server?.state === "installing"}
        <p class="depth-notice" role="status">
          <b>installing — the game files are downloading on the node. this can take a while for a
            large game; the install log reads live in the console pane.</b>
        </p>
      {/if}
      {#if server?.state === "install_failed"}
        <p class="depth-notice bad" role="alert">
          <b>install failed — the server never provisioned.{failReason
              ? " " + failReason
              : ""}{haveInstallLog ? " the full install log is in the console pane." : ""}</b>
          <button class="mini-act res" disabled={depth.powerBusy} onclick={() => void reinstall()}>reinstall</button>
        </p>
      {/if}
      {#if depth.error}
        <p class="depth-notice" role="alert">
          <b>{depth.error}</b>
          <button class="mini-act" onclick={() => (depth.error = null)}>dismiss</button>
        </p>
      {/if}
      <div class="controls-row" id="dControls">
        {#if running || server?.state === "starting" || server?.state === "stopping"}
          <button class="ctl ctl-stop" disabled={depth.powerBusy} onclick={() => void power("stop")}>stop</button>
          <button class="ctl ctl-restart" disabled={depth.powerBusy} onclick={() => void power("restart")}>restart</button>
        {:else}
          <button
            class="ctl ctl-start"
            disabled={depth.powerBusy || server?.state === "installing"}
            onclick={() => void power("start")}>start</button
          >
        {/if}
      </div>
      <section class="side-block" aria-label="Players online">
        <h3 class="pane-label" id="rosterLabel">
          online · {stats?.players_known ? stats.players : running ? "?" : 0}
        </h3>
        <div class="side-body roster" id="dRoster">
          <!-- the agent reports counts, not names — a roster needs game-side query support -->
          <p class="roster-empty">
            {#if running && stats?.players_known && stats.players > 0}
              {stats.players} aboard — names need game query support
            {:else if running}
              player names unavailable for this game
            {:else}
              no one aboard — server is dark
            {/if}
          </p>
        </div>
      </section>
      <section class="side-block" aria-label="Server vitals">
        <h3 class="pane-label">vitals</h3>
        <div class="side-body">
          <div class="kv railed"><span>cpu</span><span class="heat-rail" use:istyle={`--pct: ${cpuRail}%`}><span class="heat-ghost"></span><span class="heat-fill"></span></span><b><span>{stats ? cpuPct : "—"}</span><small>%</small></b></div>
          <div class="kv railed"><span>mem</span><span class="heat-rail" use:istyle={`--pct: ${memRail}%`}><span class="heat-ghost"></span><span class="heat-fill"></span></span><b><span>{stats ? fmtGb(stats.mem_used_mb) + " / " + Math.round(stats.mem_limit_mb / 1024) : "—"}</span><small>G</small></b></div>
          <div class="kv railed"><span>net</span><span class="tick-lane{stats ? '' : ' dead'}" aria-hidden="true" use:istyle={`--rate: ${Math.max(0.2, Math.min(3, netRate / 4 + 0.4)).toFixed(2)}`}><i class="tk" use:istyle={"--d:1.9s; --dl:-0.2s; opacity:0.9"}></i><i class="tk" use:istyle={"--d:2.6s; --dl:-1.3s; opacity:0.6"}></i><i class="tk" use:istyle={"--d:2.2s; --dl:-1.9s; opacity:0.75"}></i><i class="tk" use:istyle={"--d:3s; --dl:-0.7s; opacity:0.5"}></i><i class="tk" use:istyle={"--d:2.4s; --dl:-2.1s; opacity:0.8"}></i><i class="tk" use:istyle={"--d:2.8s; --dl:-0.4s; opacity:0.55"}></i></span><b><span>{stats ? netRate.toFixed(1) : "—"}</span><small>Mb/s</small></b></div>
          <div class="kv"><span>world size</span><b><span>{stats?.disk_used_mb ? fmtGb(stats.disk_used_mb) : "—"}</span><small>G</small></b></div>
        </div>
      </section>
      <section class="side-block" aria-label="Network">
        <h3 class="pane-label">network</h3>
        <div class="side-body net-table">
          {#each Object.entries(depth.dns?.ports ?? server?.ports ?? {}) as [portName, portNum] (portName)}
            <div class="kv net-kv">
              <span>{portName} port</span><b><span>{portNum}</span></b><i class="nu">{depth.dns?.unifi_configured ? "fwd" : ""}</i>
              {#if depth.dns?.unifi_configured}
                <input
                  type="checkbox"
                  class="ns-r"
                  id="nsPort-{portName}"
                  bind:checked={portOpen[portName]}
                  aria-label="{portName} port"
                  onchange={() => void portFlip(portName)}
                />
                <label class="net-state" for="nsPort-{portName}"><span class="ns-stack"><span class="ns-w on">open</span><span class="ns-w off">closed</span></span></label>
              {:else}
                <span class="nu">local</span>
              {/if}
            </div>
          {/each}
          <div class="kv net-kv"><span>endpoint</span><b>{endpoint}</b><i class="nu" aria-hidden="true"></i><button class="mini-act res copy-act{copied ? ' did' : ''}" onclick={copyEndpoint}><span class="ns-stack"><span class="ns-w on">copy</span><span class="ns-w off">copied</span></span></button></div>
          {#if depth.dns?.cloudflare_configured}
            <div class="dns-sep"></div>
            <div class="kv net-kv"><span>hostname</span><input class="dns-ed" bind:value={dnsName} placeholder="game.example.com" spellcheck="false" aria-label="hostname" onblur={() => void dnsBlur()} /><i class="nu">dns</i><input type="checkbox" class="ns-r" id="dnsPub" bind:checked={dnsPublished} aria-label="dns published" onchange={() => void dnsFlip()} /><label class="net-state" for="dnsPub"><span class="ns-stack"><span class="ns-w on">unpublish</span><span class="ns-w off">publish</span></span></label></div>
            <div class="kv net-kv"><span>srv service</span><input class="dns-ed" bind:value={dnsSvc} placeholder="_game._udp" spellcheck="false" aria-label="srv service" onblur={() => void dnsBlur()} /><i class="nu">srv</i></div>
          {/if}
        </div>
      </section>
      <section class="side-block" aria-label="Backups">
        <h3 class="pane-label">backups</h3>
        <div class="side-body" id="backupBody">
          {#each depth.backups as b, i (b.id)}
            {#if b.state === "pending"}
              <div class="bk-live"><span>{b.name} · creating…</span><span class="pct"></span><span class="bk-progress" use:istyle={"--prog:60"}><i></i></span></div>
            {:else}
              {@const s = bkState(b)}
              {@const mirror = bkMirror(b.replication, depth.backupMirror)}
              {@const open = isBackupOpen(b.id, i)}
              <div class="bk">
                <div class="backup-row">
                  <span class="bk-sum">
                    <button
                      class="bk-more"
                      aria-expanded={open}
                      aria-controls="bkd-{b.id}"
                      aria-label="{open ? 'Hide' : 'Show'} details for the {b.name} backup"
                      onclick={() => toggleBackup(b.id, i)}
                    ><svg viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 4.5 2.5 L 8 6 L 4.5 9.5"/></svg></button>
                    <span>{fmtWhen(b.created_ms)} · {b.name}{b.size ? " · " + fmtSize(b.size) : ""}</span>
                  </span>
                  <span class="bk-acts">
                    <span class="bk-state {s.cls}">{s.word}</span>
                    <button class="mini-act res" disabled={depth.restoringBackup === b.id || s.failed || restoreBlocked !== ""} title={s.failed ? "nothing was captured — there is no archive to restore" : restoreBlocked || undefined} onclick={() => void backupRestore(b)}>{depth.restoringBackup === b.id ? "restoring…" : "restore"}</button>
                    <button class="mini-act del" onclick={() => void backupDelete(b)}>delete</button>
                  </span>
                </div>
                <div class="bk-detail" id="bkd-{b.id}" hidden={!open}>
                  {#if !s.failed}<div class="kv"><span>captured</span><b>{capturedGlobs}</b></div>{/if}
                  <div class="kv"><span>archive</span><b>{b.name}</b></div>
                  {#if mirror}<div class="kv"><span>mirror</span><b class={b.replication === "failed" ? "warn" : ""}>{mirror}</b></div>{/if}
                  {#if b.error}<div class="kv"><span>note</span><b class={s.failed ? "crisis" : "warn"}>{b.error}</b></div>{/if}
                </div>
              </div>
            {/if}
          {:else}
            <div class="backup-row"><span>no backups yet</span><span class="good"></span></div>
          {/each}
          {#if keptBackups.length > 0}
            {#if backupsAtCap && oldestKept}
              <p class="bk-evict">at capacity — the next backup removes <b>{fmtWhen(oldestKept.created_ms)} · {oldestKept.name}</b>{replicationOn ? ", on this node and its mirror" : ""}</p>
            {/if}
            <div class="bk-foot">
              <span>keep <b>{BACKUP_KEEP}</b> · <b class={backupsAtCap ? "at-cap" : ""}>{keptBackups.length} of {BACKUP_KEEP}</b> · <b>{fmtSize(backupDiskBytes)}</b> on disk</span>
              {#if replicationOn}<span>mirror <b>{depth.backupMirror}</b></span>{/if}
            </div>
          {/if}
          <button class="bk-big" disabled={depth.creatingBackup || backupsBusy} onclick={() => void backupCreate()}>create backup now</button>
        </div>
      </section>
      <section class="side-block" aria-label="Schedules">
        <h3 class="pane-label">schedules</h3>
        <div class="side-body">
          <div class="side-body" id="schBody" use:istyle={"padding: 0; gap: 12px"}>
            {#each depth.schedules as t (t.id)}
              <div class="sch-row{t.enabled ? '' : ' paused'}" data-cron={t.cron}>
                <span class="sch-main"><span>{t.name}</span><small>{t.action === "command" && t.command ? "command: " + t.command : t.action} · {t.cron}</small></span>
                <span class="sch-acts"><span class="sch-next">{schNext(t)}</span><button class="mini-act res sch-pause" onclick={() => void scheduleToggle(t)}>{t.enabled ? "pause" : "resume"}</button><button class="mini-act del" onclick={() => void scheduleDelete(t)}>delete</button></span>
              </div>
            {/each}
          </div>
          {#if !schFormOpen}
            <button class="cfg-btn ghost sch-new" onclick={schOpenForm}>new schedule</button>
          {/if}
          <div class="sch-form" hidden={!schFormOpen}>
            <div class="cfg-row">
              <span>name</span>
              <input type="text" class="cfg-in" bind:this={schNameEl} bind:value={schName} placeholder="nightly restart" aria-label="Schedule name" />
            </div>
            <div class="cfg-row">
              <span>action</span>
              <select class="cfg-in" bind:value={schAction} aria-label="Schedule action"><option value="restart">restart</option><option value="backup">backup</option><option value="command">console command</option><option value="replicate">replicate backups</option></select>
            </div>
            <div class="cfg-row" hidden={schAction !== "command"}>
              <span>command</span>
              <input type="text" class="cfg-in" bind:value={schCmd} placeholder="save-all" aria-label="Console command" />
            </div>
            <div class="cfg-row">
              <span>cron — min hour dom month dow</span>
              <input type="text" class="cfg-in" bind:value={schCron} aria-label="Cron expression" />
            </div>
            <div class="sch-presets" role="group" aria-label="Cron presets">
              {#each CRON_PRESETS as p (p.cron)}
                <button class="sch-preset{schCron.trim() === p.cron ? ' on' : ''}" data-cron={p.cron} onclick={() => (schCron = p.cron)}>{p.label}</button>
              {/each}
            </div>
            <label class="tgl"><input type="checkbox" bind:checked={schEnabled} /><i></i>enabled</label>
            <div class="sch-btns">
              <button class="cfg-btn ghost" onclick={() => void schAddRow()}>add schedule</button>
              <button class="cfg-btn ghost" onclick={() => (schFormOpen = false)}>cancel</button>
            </div>
          </div>
        </div>
      </section>
      <section class="side-block danger-block" aria-label="Delete server">
        <h3 class="pane-label">danger</h3>
        <div class="side-body">
          <p class="danger-note">deleting removes this server's world, backups and config. it cannot be undone.</p>
          <button
            class="ctl ctl-delete"
            id="deleteSrvBtn"
            onclick={(e) => openConfirm(name, e.currentTarget, { noun: "server" })}
          >
            <svg width="13" height="13" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 2.5 4 H 11.5 M 5.5 4 V 2.5 H 8.5 V 4 M 3.75 4 L 4.25 11.5 H 9.75 L 10.25 4 M 6 6.25 V 9.5 M 8 6.25 V 9.5"/></svg>
            delete server
          </button>
        </div>
      </section>
    </div>
  </div>
</div>
