<script lang="ts">
  import {
    ui,
    wz,
    wzGo,
    wzMarkDone,
    wzReachable,
    dbConnectRestart,
    closeSheet,
    openSheet,
  } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";

  const STEPS = [
    { n: 1, label: "database", aria: "Step 1: database" },
    { n: 2, label: "secure", aria: "Step 2: secure" },
    { n: 3, label: "connect a node", aria: "Step 3: connect a node" },
    { n: 4, label: "add a game", aria: "Step 4: add a game" },
    { n: 5, label: "deploy", aria: "Step 5: deploy" },
  ];

  // -- step 1: the database
  let dbName = $state("kraken");

  function dbTest() {
    const name = dbName.trim();
    if (!name) {
      wz.dbRes = { cls: "bad", text: "name the database first." };
      return;
    }
    wz.dbRes = { cls: "ok", text: "connected — " + name + " will be created." };
  }

  // -- step 3: enrollment
  const SAMPLE_TOKEN = "sample-token-not-a-real-credential";
  let tokenShown = $state(false);
  let enrollState = $state("waiting for the agent");
  let nodeOk = $state(false);
  interface LogEntry {
    text: string;
    pending: boolean;
  }
  let wzLog = $state<LogEntry[]>([]);

  function genToken() {
    tokenShown = true;
    wzLog = [
      { text: "one-time enrollment token generated — valid 15 minutes", pending: false },
      { text: "waiting for the agent to enroll — run the command on the remote host", pending: true },
    ];
    setTimeout(() => {
      wzLog[1] = {
        text: "agent enrolled from 10.0.0.4 — advertised hosts: 10.0.0.4, 172.17.0.1",
        pending: false,
      };
      enrollState = "enrolled";
    }, 1700);
    setTimeout(() => {
      wzLog.push({ text: 'node "behemoth" (linux) reported online', pending: false });
      nodeOk = true;
    }, 3000);
  }

  // cmd-copy label flips
  let copyLabels = $state(["copy to clipboard", "copy to clipboard"]);
  let cmdLinuxEl: HTMLPreElement | undefined = $state();
  let cmdWinEl: HTMLPreElement | undefined = $state();
  async function cmdCopy(i: number) {
    const el = i === 0 ? cmdLinuxEl : cmdWinEl;
    try {
      await navigator.clipboard.writeText(el?.textContent ?? "");
      copyLabels[i] = "copied";
    } catch {
      copyLabels[i] = "select and copy";
    }
    setTimeout(() => (copyLabels[i] = "copy to clipboard"), 1800);
  }

  // -- step 4: the catalog import — staged rather than instant: eight rows
  // flipping at once is a flash, and the point is that you can see it did
  // something
  const IMP_GAMES: { name: string; slug: string; plats: string[] }[] = [
    { name: "Abiotic Factor", slug: "abiotic-factor", plats: ["win", "wine"] },
    { name: "RuneScape Dragonwilds", slug: "runescape-dragonwilds", plats: ["wine"] },
    { name: "Enshrouded", slug: "enshrouded", plats: ["win"] },
    { name: "Factorio", slug: "factorio", plats: ["linux"] },
    { name: "Palworld", slug: "palworld", plats: ["linux", "win"] },
    { name: "Valheim", slug: "valheim", plats: ["linux"] },
    { name: "V Rising", slug: "v-rising", plats: ["win"] },
    { name: "Windrose", slug: "windrose", plats: ["win"] },
  ];
  let imported = $state(IMP_GAMES.map(() => false));
  const impCount = $derived(imported.filter(Boolean).length);

  function importAll() {
    IMP_GAMES.forEach((_, i) =>
      setTimeout(() => {
        imported[i] = true;
      }, i * 90),
    );
  }

  // -- step 5: hand off into the create-server sheet the app already has
  function deploy() {
    closeSheet("setup");
    openSheet("nsForm", innerWidth / 2, innerHeight / 2, null);
  }

  // leaving the wizard is leaving it — nothing here is destructive, so
  // nothing here asks twice
  function skip() {
    closeSheet("setup");
  }
</script>

<div
  class="sheet wz"
  class:open={!!ui.open.setup}
  id="setup"
  role="dialog"
  aria-modal="true"
  aria-labelledby="wzTitle"
  style="--ox: {ui.open.setup?.ox ?? '50%'}; --oy: {ui.open.setup?.oy ?? '50%'}"
  use:sheetFocus
>
  <div class="depth-head">
    <div class="wz-head">
      <span class="wz-eyebrow">first run</span>
      <h2 class="depth-title" id="wzTitle">Get Kraken ready</h2>
    </div>
    <div class="prefs-note"><span class="synthetic">values are sample — surface not wired</span></div>
  </div>
  <div class="sheet-body">
    <div class="wz-body">
      <div class="wz-rail" id="wzRail" role="group" aria-label="Setup progress">
        {#each STEPS as step}
          <div class="wz-step{wz.done[step.n - 1] ? ' done' : ''}" data-step={step.n}>
            <button class="wz-hit" disabled={!wzReachable(step.n)} aria-label={step.aria} onclick={() => wzGo(step.n)}>
              <span class="wz-dot"><b>{step.n}</b><svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 2.5 7.2 L 5.4 10 L 11.5 3.6"/></svg></span>
              <span class="wz-lbl">{step.label}</span>
            </button>
            <input type="radio" name="wzstep" tabindex="-1" aria-hidden="true" checked={wz.at === step.n} />
            {#if step.n < 5}<span class="wz-line" aria-hidden="true"></span>{/if}
          </div>
        {/each}
      </div>

      <section class="wz-panel{wz.at === 1 ? ' on' : ''}" id="wzPanel1" aria-label="Step 1: database">
        <div class="prefs-group">
          <div class="cfg-head">
            <h3 class="pane-label">database</h3>
            <span class="cfg-badge {wz.dbMode === 'postgres' ? 'enc' : 'env'}" id="dbMode">{wz.dbMode}</span>
          </div>
          <div class="cfg cfg-2">
            <p class="cfg-desc cfg-wide">kraken is on the built-in <b>in-memory</b> store — everything resets when the panel restarts. connect postgres to persist. the database is created if it does not exist, migrations run, then the panel restarts and you sign in again.</p>
            <label class="cfg-row"><span>host</span><input class="cfg-in" id="dbHost" type="text" value="localhost" spellcheck="false" autocapitalize="none" autocorrect="off" /></label>
            <label class="cfg-row"><span>port</span><input class="cfg-in" id="dbPort" type="text" value="5432" inputmode="numeric" spellcheck="false" /></label>
            <label class="cfg-row"><span>user</span><input class="cfg-in" id="dbUser" type="text" value="kraken" spellcheck="false" autocapitalize="none" autocorrect="off" /></label>
            <label class="cfg-row"><span>password</span><input class="cfg-in" id="dbPass" type="password" value="samplepassword" autocomplete="off" /></label>
            <label class="cfg-row"><span>database</span><input class="cfg-in" id="dbName" type="text" bind:value={dbName} spellcheck="false" autocapitalize="none" autocorrect="off" /></label>
            <div class="cfg-row">
              <span>ssl mode</span>
              <select class="cfg-in" id="dbSsl" aria-label="SSL mode">
                <button><selectedcontent></selectedcontent></button>
                <option value="disable" selected>disable</option>
                <option value="require">require</option>
                <option value="verify-full">verify-full</option>
              </select>
            </div>
            <p class="wz-res cfg-wide{wz.dbRes ? ' shown ' + wz.dbRes.cls : ''}" id="dbRes" role="status" aria-live="polite">{#if wz.dbRes}<svg width="13" height="13" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 2.5 7.2 L 5.4 10 L 11.5 3.6"/></svg><span>{wz.dbRes.text}</span>{/if}</p>
          </div>
          <div class="cfg-actions">
            <button class="cfg-btn ghost" id="dbTest" onclick={dbTest}>test connection</button>
            <button class="cfg-btn solid" id="dbConnect" onclick={dbConnectRestart}>connect &amp; restart</button>
            <span class="cfg-note">the bundled postgres is already listening on 5432</span>
          </div>
        </div>
        <p class="cfg-help">or continue on the in-memory store for now — fine for trying things out, not for anything you would miss.</p>
        <div class="wz-foot">
          <button class="wz-skip" onclick={skip}>skip for now</button>
          <div class="wz-fwd"><button class="cfg-btn solid" onclick={() => wzGo(2)}>continue</button></div>
        </div>
      </section>

      <section class="wz-panel{wz.at === 2 ? ' on' : ''}" id="wzPanel2" aria-label="Step 2: secure">
        <div class="prefs-group">
          <div class="cfg-head"><h3 class="pane-label">secure the admin account</h3></div>
          <div class="cfg">
            <p class="wz-res ok shown"><svg width="12" height="12" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 2.5 7.2 L 5.4 10 L 11.5 3.6"/></svg> admin password set — the shipped credentials no longer work.</p>
            <p class="cfg-desc">next, connect a node: a host running the kraken agent, so there is somewhere for a game server to actually live. one node is enough — it can be this same machine.</p>
          </div>
        </div>
        <div class="wz-foot">
          <button class="cfg-btn ghost" onclick={() => wzGo(1)}>back</button>
          <div class="wz-fwd"><button class="cfg-btn solid" onclick={() => wzGo(3)}>continue</button></div>
        </div>
      </section>

      <section class="wz-panel{wz.at === 3 ? ' on' : ''}" id="wzPanel3" aria-label="Step 3: connect a node">
        <div class="prefs-group">
          <div class="cfg-head"><h3 class="pane-label">connect a node</h3></div>
          <div class="cfg">
            <p class="cfg-desc">no nodes yet. connect a remote host below, or run the agent on this machine for an all-in-one install. the node names itself from its <code>KRAKEN_NODE_ID</code>.</p>
            <div class="cfg-row">
              <span>connection</span>
              <div class="opt-set">
                <label class="opt opt-inline"><input type="radio" name="wznconn" class="opt-r-in" checked /><span class="opt-t">node dials the panel</span><span class="opt-n">no inbound ports — works behind nat</span></label>
                <label class="opt opt-inline"><input type="radio" name="wznconn" class="opt-r-in" /><span class="opt-t">panel dials the node</span><span class="opt-n">needs inbound 9090 open on the node</span></label>
              </div>
            </div>
            <div class="wz-log{wzLog.length ? ' shown' : ''}" id="wzLog" role="status" aria-live="polite">
              {#each wzLog as entry}
                <span class={entry.pending ? "pending" : ""}><i>{entry.pending ? "▸" : "✓"}</i><span>{entry.text}</span></span>
              {/each}
            </div>
          </div>
          <div class="cfg-actions">
            <button class="cfg-btn solid" id="wzGenToken" disabled={tokenShown} onclick={genToken}>generate enrollment token</button>
            <span class="cfg-note">the token is valid once, for 15 minutes</span>
          </div>
        </div>

        <div class="prefs-group token-wrap{tokenShown ? ' shown' : ''}" id="wzTokenWrap" aria-label="Enrollment token">
          <div class="cfg-head">
            <h3 class="pane-label">one-time enrollment token</h3>
            <span class="cfg-badge enc">one-time</span>
            <span class="cfg-badge env" id="wzEnrollState">{enrollState}</span>
          </div>
          <div class="cfg">
            <div class="cfg-ro" id="wzToken">{tokenShown ? SAMPLE_TOKEN : "—"}</div>
            <div class="os-tabs">
              <div class="os-tablist" role="tablist">
                <label class="os-tab"><input type="radio" name="wznos" class="os-r-in" checked />linux install</label>
                <label class="os-tab"><input type="radio" name="wznos" class="os-r-in" />windows install</label>
              </div>
              <div class="os-panel os-linux">
                <h4 class="os-step">1 · install + enroll + start (one command)</h4>
                <div class="cmd-wrap">
                  <pre class="cmd" id="wzCmdLinux" bind:this={cmdLinuxEl}>curl -fsSL https://raw.githubusercontent.com/briggleman/kraken/main/deploy/install.sh | \
  sudo bash -s -- --role agent --panel-url https://kraken.zerooneone.io \
  --enroll-token <b class="tok">{tokenShown ? SAMPLE_TOKEN : "<enrollment-token>"}</b> \
  --ca-fingerprint <b>&lt;ca-fingerprint&gt;</b> \
  --tunnel</pre>
                  <button class="cfg-btn ghost cmd-copy" data-copy="wzCmdLinux" onclick={() => cmdCopy(0)}>{copyLabels[0]}</button>
                </div>
              </div>
              <div class="os-panel os-win">
                <h4 class="os-step">1 · install + enroll + start (elevated powershell, one command)</h4>
                <div class="cmd-wrap">
                  <pre class="cmd" id="wzCmdWin" bind:this={cmdWinEl}>iwr -useb https://raw.githubusercontent.com/briggleman/kraken/main/deploy/windows/install.ps1 -OutFile $env:TEMP\kraken-install.ps1
  powershell -ExecutionPolicy Bypass -File $env:TEMP\kraken-install.ps1 `
  -PanelUrl https://kraken.zerooneone.io `
  -Token <b class="tok">{tokenShown ? SAMPLE_TOKEN : "<enrollment-token>"}</b> `
  -CaFingerprint <b>&lt;ca-fingerprint&gt;</b> `
  -Tunnel</pre>
                  <button class="cfg-btn ghost cmd-copy" data-copy="wzCmdWin" onclick={() => cmdCopy(1)}>{copyLabels[1]}</button>
                </div>
              </div>
            </div>
            <p class="cfg-help">the agent dials the panel's tunnel listener on port 9443. if the url above is a proxied domain (cloudflare, nginx), pass <code>--tunnel-addr &lt;panel-lan-ip&gt;:9443</code> instead — proxies cannot carry the raw mtls tunnel.</p>
          </div>
        </div>

        <div class="wz-ok{nodeOk ? ' shown' : ''}" id="wzNodeOk">
          <span class="wz-ok-tick"><svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 2.5 7.2 L 5.4 10 L 11.5 3.6"/></svg></span>
          <span class="wz-ok-txt">
            <b>node online — connection verified</b>
            <span><code>behemoth</code> is connected through its tunnel and ready for deployments.</span>
          </span>
          <button class="cfg-btn ghost" onclick={() => wzGo(4)}>done</button>
        </div>

        <div class="wz-foot">
          <button class="cfg-btn ghost" onclick={() => wzGo(2)}>back</button>
          <div class="wz-fwd"><button class="cfg-btn solid" id="wzNodeNext" disabled={!nodeOk} onclick={() => wzGo(4)}>continue</button></div>
        </div>
      </section>

      <section class="wz-panel{wz.at === 4 ? ' on' : ''}" id="wzPanel4" aria-label="Step 4: add a game">
        <div class="prefs-group">
          <div class="cfg-head">
            <h3 class="pane-label">add a game</h3>
            <span class="imp-count" id="impCount"><b>{impCount}</b> / {IMP_GAMES.length} specs imported</span>
            <button class="cfg-btn ghost" id="impAll" disabled={impCount === IMP_GAMES.length} onclick={importAll}>import all</button>
          </div>
          <div class="imp">
            <div class="imp-head"><span>game</span><span>platform</span><span>status</span></div>
            {#each IMP_GAMES as game, i}
              <div class="imp-row{imported[i] ? ' did' : ''}"><span class="imp-id"><b>{game.name}</b><span>{game.slug}</span></span><span class="imp-plat">{#each game.plats as p}<span class="spec-tag">{p}</span>{/each}</span><span class="imp-st"><svg width="12" height="12" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 2.5 7.2 L 5.4 10 L 11.5 3.6"/></svg><span class="imp-w">{imported[i] ? "imported" : "pending"}</span></span></div>
            {/each}
          </div>
        </div>
        <p class="cfg-help">a spec is the recipe kraken installs from — the app id, the image, the ports and the settings it writes. these are the ones bundled with the panel; you can author your own later.</p>
        <div class="wz-foot">
          <button class="cfg-btn ghost" onclick={() => wzGo(3)}>back</button>
          <div class="wz-fwd">
            <button class="wz-skip" onclick={skip}>skip</button>
            <button class="cfg-btn solid" onclick={() => wzGo(5)}>continue</button>
          </div>
        </div>
      </section>

      <section class="wz-panel{wz.at === 5 ? ' on' : ''}" id="wzPanel5" aria-label="Step 5: deploy">
        <div class="prefs-group">
          <div class="wz-hero">
            <span class="wz-seal" aria-hidden="true"><svg width="30" height="30" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 2.5 7.2 L 5.4 10 L 11.5 3.6"/></svg></span>
            <h3>ready to deploy</h3>
            <p>a node is online and the catalog is imported. the last step is the first server — pick a game, place it, and kraken does the rest.</p>
            <button class="cfg-btn solid" id="wzDeployBtn" onclick={deploy}>deploy your first server</button>
          </div>
        </div>
        <div class="wz-foot">
          <button class="cfg-btn ghost" onclick={() => wzGo(4)}>back</button>
          <div class="wz-fwd"><button class="cfg-btn ghost" onclick={skip}>skip &amp; finish</button></div>
        </div>
      </section>
    </div>
  </div>
</div>
