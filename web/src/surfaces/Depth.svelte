<script lang="ts">
  import {
    ui,
    sim,
    surface,
    openConfirm,
    backupRestore,
    backupDelete,
    backupCreate,
    scheduleToggle,
    scheduleDelete,
    scheduleAdd,
    scheduleNext,
    SCH_NEXT,
  } from "@/lib/state.svelte";

  const d = $derived(ui.depthServer?.depth ?? null);
  const name = $derived(ui.depthServer?.name ?? "");
  const running = $derived(d?.state === "running");

  let surfaceBtn: HTMLButtonElement;
  let consoleLog: HTMLDivElement | undefined = $state();

  $effect(() => {
    if (ui.depthOpen) surfaceBtn.focus();
  });

  // keep the log pinned to the newest line
  $effect(() => {
    d?.lines.length;
    if (consoleLog) consoleLog.scrollTop = consoleLog.scrollHeight;
  });

  // vitals readouts
  const cpuPct = $derived(d && d.cpuV != null ? Math.round(d.cpuV) : 0);
  const memPct = $derived(d && d.memU != null ? Math.round((d.memU / d.memM) * 100) : 0);
  const tickRate = $derived(
    d && d.tickV != null ? Math.pow(31 / d.tickV, 2).toFixed(2) : "1",
  );

  // endpoint copy flip (the one-cell stack, so the flip never reflows the row)
  let copied = $state(false);
  function copyEndpoint() {
    if (copied || !d) return;
    const v = "72.14.201.88:" + d.port;
    if (navigator.clipboard) navigator.clipboard.writeText(v).catch(() => {});
    copied = true;
    setTimeout(() => (copied = false), 1600);
  }

  // schedules form
  let schFormOpen = $state(false);
  let schName = $state("");
  let schAction = $state("restart");
  let schCmd = $state("");
  let schCron = $state("0 4 * * *");
  let schEnabled = $state(true);
  let schNameEl: HTMLInputElement | undefined = $state();

  function schOpenForm() {
    schFormOpen = true;
    setTimeout(() => schNameEl?.focus());
  }
  function schAddRow() {
    const action = schAction;
    const rowName = schName.trim() || action;
    const cron = schCron.trim() || "0 4 * * *";
    const detail = action === "command" && schCmd.trim() ? "command: " + schCmd.trim() : action;
    scheduleAdd({ name: rowName, detail, cron, paused: !schEnabled });
    schFormOpen = false;
    schName = "";
    schCmd = "";
  }
</script>

<div
  class="depth"
  class:open={ui.depthOpen}
  id="depth"
  role="dialog"
  aria-modal="true"
  aria-labelledby="depthTitle"
  style="--ox: {ui.depthOrigin.ox}; --oy: {ui.depthOrigin.oy}"
>
  <div class="depth-head">
    <button class="surface-btn" id="surfaceBtn" bind:this={surfaceBtn} onclick={surface}>
      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6 10 V 2 M 2 6 L 6 2 L 10 6"/></svg>
      surface
    </button>
    <h2 class="depth-title" id="depthTitle">{name}</h2>
    <div class="depth-meta">
      <span>state <b class={running ? "ok-txt" : ""} id="dState">{d?.state ?? ""}</b></span>
      <span>uptime <b>{d?.up ?? ""}</b></span>
      <span>players <b>{d?.players ?? ""}</b></span>
      <span>port <b>{d?.port ?? ""}</b></span>
      <span>ver <b>{d?.ver ?? ""}</b></span>
    </div>
  </div>
  <div class="depth-body">
    {#key ui.depthServer}
      <section class="console" aria-label="Server station">
        <input type="radio" name="stn" id="stnConsole" class="stn-r" checked />
        <input type="radio" name="stn" id="stnSettings" class="stn-r" />
        <input type="radio" name="stn" id="stnFiles" class="stn-r" />
        <div class="stn-tabs" role="tablist">
          <label for="stnConsole">live console</label>
          <label for="stnSettings">settings</label>
          <label for="stnFiles">files</label>
        </div>
        <div class="stn-panel p-console">
          <div class="console-log" id="consoleLog" bind:this={consoleLog}>
            {#each d?.lines ?? [] as line}
              <div><span class="t">{line.t}</span>{@html line.html}</div>
            {/each}
          </div>
          <div class="console-in">
            <span>&gt;</span>
            <input type="text" placeholder="broadcast, save, kick <player> … (mock)" aria-label="Console command" />
          </div>
        </div>
        <div class="stn-panel p-settings">
          <div class="cfg">
            <label class="cfg-row"><span>server name</span><input class="cfg-in" type="text" value="{name} — behemoth" /></label>
            <label class="cfg-row"><span>join password</span><input class="cfg-in" type="password" value="deepwater" /></label>
            <label class="cfg-row"><span>max players</span><input class="cfg-in" type="text" value={d?.players.split("/")[1] || "32"} /></label>
            <label class="cfg-row"><span>difficulty</span><input class="cfg-in" type="text" value="normal" /></label>
            <label class="cfg-row"><span>xp rate</span><input class="cfg-in" type="text" value="1.0" /></label>
            <label class="cfg-row"><span>day / night speed</span><input class="cfg-in" type="text" value="1.0 / 1.0" /></label>
            <label class="tgl"><input type="checkbox" checked /><i></i>pvp enabled</label>
            <label class="tgl"><input type="checkbox" checked /><i></i>show map markers</label>
            <label class="tgl"><input type="checkbox" /><i></i>hardcore drops</label>
            <label class="tgl"><input type="checkbox" checked /><i></i>autosave every 15 min</label>
          </div>
          <div class="cfg-foot">
            <span class="cfg-note">changes apply on next restart</span>
            <button class="cfg-btn ghost">revert</button>
            <button class="cfg-btn solid">apply settings</button>
          </div>
        </div>
        <div class="stn-panel p-files">
          <div class="files-crumb">/<b>srv</b>/<b>{name.toLowerCase().replace(/\s+/g, "-")}</b>/</div>
          <div class="files-list">
            <button class="f-row dir"><span>Config/</span><span>—</span><span>aug 22 03:11</span></button>
            <button class="f-row dir"><span>Saves/</span><span>—</span><span>today 23:30</span></button>
            <button class="f-row dir"><span>Logs/</span><span>—</span><span>today 23:41</span></button>
            <button class="f-row dir"><span>Mods/</span><span>—</span><span>aug 04 19:52</span></button>
            <button class="f-row"><span>ServerSettings.ini</span><span>4.2K</span><span>aug 22 03:11</span></button>
            <button class="f-row"><span>GameUserSettings.ini</span><span>1.1K</span><span>aug 20 17:45</span></button>
            <button class="f-row"><span>server.log</span><span>218K</span><span>today 23:41</span></button>
            <button class="f-row"><span>banlist.txt</span><span>0.3K</span><span>aug 12 09:02</span></button>
          </div>
          <div class="files-foot">
            <span><span>{d?.worldG ?? ""}</span>G on disk · 8 items</span>
            <button class="cfg-btn ghost">upload</button>
          </div>
        </div>
      </section>
    {/key}
    <div class="depth-side">
      <div class="controls-row" id="dControls">
        {#if running}
          <button class="ctl ctl-stop">stop</button>
          <button class="ctl ctl-restart">restart</button>
        {:else}
          <button class="ctl ctl-start">start</button>
        {/if}
      </div>
      <section class="side-block" aria-label="Players online">
        <h3 class="pane-label" id="rosterLabel">online · {running ? (d?.roster.length ?? 0) : 0}</h3>
        <div class="side-body roster" id="dRoster">
          {#each d?.roster ?? [] as r}
            <div class="roster-row"><span>{r.name}</span><time>{r.time}</time><button class="kick">kick</button></div>
          {:else}
            <p class="roster-empty">no one aboard — server is dark</p>
          {/each}
        </div>
      </section>
      <section class="side-block" aria-label="Server vitals">
        <h3 class="pane-label">vitals</h3>
        <div class="side-body">
          <div class="kv railed"><span>cpu</span><span class="heat-rail" style="--pct: {cpuPct}%"><span class="heat-ghost"></span><span class="heat-fill"></span></span><b><span>{d?.cpuV == null ? "—" : cpuPct}</span><small>%</small></b></div>
          <div class="kv railed"><span>mem</span><span class="heat-rail" style="--pct: {d?.memU == null ? 0 : memPct}%"><span class="heat-ghost"></span><span class="heat-fill"></span></span><b><span>{d?.memU == null ? "—" : d.memU.toFixed(1) + " / " + d.memM}</span><small>G</small></b></div>
          <div class="kv railed"><span>tick</span><span class="tick-lane{d?.tickV == null ? ' dead' : ''}" aria-hidden="true" style="--rate: {tickRate}"><i class="tk" style="--d:1.9s; --dl:-0.2s; opacity:0.9"></i><i class="tk" style="--d:2.6s; --dl:-1.3s; opacity:0.6"></i><i class="tk" style="--d:2.2s; --dl:-1.9s; opacity:0.75"></i><i class="tk" style="--d:3s; --dl:-0.7s; opacity:0.5"></i><i class="tk" style="--d:2.4s; --dl:-2.1s; opacity:0.8"></i><i class="tk" style="--d:2.8s; --dl:-0.4s; opacity:0.55"></i></span><b><span>{d?.tickV == null ? "—" : Math.round(d.tickV)}</span><small>ms</small></b></div>
          <div class="kv"><span>world size</span><b><span>{d?.worldG ?? ""}</span><small>G</small></b></div>
        </div>
      </section>
      <section class="side-block" aria-label="Network">
        <h3 class="pane-label">network</h3>
        <div class="side-body net-table">
          <div class="kv net-kv"><span>game port</span><b><span>{d?.port ?? ""}</span></b><i class="nu">udp</i><input type="checkbox" class="ns-r" id="nsGamePort" checked aria-label="game port" /><label class="net-state" for="nsGamePort"><span class="ns-stack"><span class="ns-w on">open</span><span class="ns-w off">closed</span></span></label></div>
          <div class="kv net-kv"><span>query port</span><b>27015</b><i class="nu">udp</i><input type="checkbox" class="ns-r" id="nsQueryPort" checked aria-label="query port" /><label class="net-state" for="nsQueryPort"><span class="ns-stack"><span class="ns-w on">open</span><span class="ns-w off">closed</span></span></label></div>
          <div class="kv net-kv"><span>rcon</span><b>25575</b><i class="nu">local</i><input type="checkbox" class="ns-r" id="nsRcon" aria-label="rcon" /><label class="net-state" for="nsRcon"><span class="ns-stack"><span class="ns-w on">on</span><span class="ns-w off">off</span></span></label></div>
          <div class="kv net-kv"><span>endpoint</span><b>72.14.201.88:{d?.port ?? ""}</b><i class="nu" aria-hidden="true"></i><button class="mini-act res copy-act{copied ? ' did' : ''}" onclick={copyEndpoint}><span class="ns-stack"><span class="ns-w on">copy</span><span class="ns-w off">copied</span></span></button></div>
          <div class="dns-sep"></div>
          <div class="kv net-kv"><span>hostname</span><b class="dns-ed" contenteditable="plaintext-only" spellcheck="false" role="textbox" aria-label="hostname">palworld.zerooneone.io</b><i class="nu">dns</i><input type="checkbox" class="ns-r" id="dnsPub" checked aria-label="dns published" /><label class="net-state" for="dnsPub"><span class="ns-stack"><span class="ns-w on">unpublish</span><span class="ns-w off">publish</span></span></label></div>
          <div class="kv net-kv"><span>srv service</span><b class="dns-ed" contenteditable="plaintext-only" spellcheck="false" role="textbox" aria-label="srv service">_palworld._udp</b><i class="nu">srv</i></div>
        </div>
      </section>
      <section class="side-block" aria-label="Backups">
        <h3 class="pane-label">backups</h3>
        <div class="side-body" id="backupBody">
          {#if sim.backupLive}
            <div class="bk-live"><span>{sim.backupLive.name} · creating…</span><span class="pct">{Math.round(sim.backupLive.prog)}%</span><span class="bk-progress" style="--prog:{sim.backupLive.prog}"><i></i></span></div>
          {/if}
          {#each sim.backups as row}
            {#if row.kind === "scheduled"}
              <div class="backup-row"><span>{row.label}</span><span class="good">scheduled</span></div>
            {:else}
              <div class="backup-row" style={row.leaving ? "opacity: 0.3" : ""}><span>{row.label}</span><span class="bk-acts"><span class="good">ok</span><button class="mini-act res" disabled={row.restoring} onclick={() => backupRestore(row)}>{row.restoring ? "restoring…" : "restore"}</button><button class="mini-act del" onclick={() => backupDelete(row)}>delete</button></span></div>
            {/if}
          {/each}
          <button class="bk-big" disabled={!!sim.backupLive} onclick={backupCreate}>create backup now</button>
        </div>
      </section>
      <section class="side-block" aria-label="Schedules">
        <h3 class="pane-label">schedules</h3>
        <div class="side-body">
          <div class="side-body" id="schBody" style="padding: 0; gap: 12px">
            {#each sim.schedules as row}
              <div class="sch-row{row.paused ? ' paused' : ''}" data-cron={row.cron} style={row.leaving ? "opacity: 0.3" : ""}>
                <span class="sch-main"><span>{row.name}</span><small>{row.detail} · {row.cron}</small></span>
                <span class="sch-acts"><span class="sch-next">{scheduleNext(row)}</span><button class="mini-act res sch-pause" onclick={() => scheduleToggle(row)}>{row.paused ? "resume" : "pause"}</button><button class="mini-act del" onclick={() => scheduleDelete(row)}>delete</button></span>
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
              {#each Object.entries(SCH_NEXT).filter(([c]) => c !== "0 3 * * *") as [cron]}
                <button class="sch-preset{schCron.trim() === cron ? ' on' : ''}" data-cron={cron} onclick={() => (schCron = cron)}>{cron === "0 * * * *" ? "hourly" : cron === "0 */6 * * *" ? "every 6h" : cron === "0 4 * * *" ? "daily 04:00" : "sun 04:00"}</button>
              {/each}
            </div>
            <label class="tgl"><input type="checkbox" bind:checked={schEnabled} /><i></i>enabled</label>
            <div class="sch-btns">
              <button class="cfg-btn ghost" onclick={schAddRow}>add schedule</button>
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
