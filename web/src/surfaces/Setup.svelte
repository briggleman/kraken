<script lang="ts">
  import { istyle } from "@/lib/istyle";
  import { ui, wz, wzGo, wzReachable, closeSheet, openSheet, TICK_MS } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";
  import { api, clearToken } from "@/api/client";
  import { auth } from "@/lib/auth.svelte";
  import { fleet, refreshFleet } from "@/lib/fleet.svelte";
  import { Enrollment, type EnrollMode } from "@/lib/enroll.svelte";
  import type { CatalogItem, DatabaseInput } from "@/api/types";

  const STEPS = [
    { n: 1, label: "database", aria: "Step 1: database" },
    { n: 2, label: "secure", aria: "Step 2: secure" },
    { n: 3, label: "connect a node", aria: "Step 3: connect a node" },
    { n: 4, label: "deploy", aria: "Step 4: deploy" },
  ];

  const open = $derived(!!ui.open.setup);

  // -- step 1: the database (real config; connect restarts the panel)
  let db = $state<DatabaseInput>({ host: "localhost", port: 5432, user: "kraken", password: "", dbname: "kraken", sslmode: "disable" });
  let dbLocked = $state(false);
  let dbLoaded = false; // plain: read+written in one effect
  $effect(() => {
    if (!open || dbLoaded) return;
    dbLoaded = true;
    void api
      .getDatabaseConfig()
      .then((c) => {
        wz.dbMode = c.using_memory ? "in-memory" : "postgres";
        dbLocked = c.env_locked;
        db = {
          host: c.host || "localhost",
          port: c.port || 5432,
          user: c.user || "kraken",
          password: "",
          dbname: c.dbname || "kraken",
          sslmode: c.sslmode || "disable",
        };
      })
      .catch(() => {});
  });

  async function dbTest() {
    if (!db.dbname?.trim()) {
      wz.dbRes = { cls: "bad", text: "name the database first." };
      return;
    }
    try {
      const r = await api.testDatabase(db);
      if (!r.ok) wz.dbRes = { cls: "bad", text: "could not reach postgres with those values." };
      else if (r.db_exists) wz.dbRes = { cls: "ok", text: "connected — " + db.dbname + " exists." };
      else if (r.can_create_db)
        wz.dbRes = { cls: "ok", text: "connected — " + db.dbname + " will be created." };
      else
        wz.dbRes = {
          cls: "bad",
          text: "connected, but this user cannot create " + db.dbname + ".",
        };
    } catch (e) {
      wz.dbRes = { cls: "bad", text: firstLine(e, "connection failed") };
    }
  }

  // driver errors arrive as multi-line dumps; the result line is one line, so
  // show the first sentence and let the audit log keep the rest
  function firstLine(e: unknown, fallback: string): string {
    const msg = e instanceof Error ? e.message : "";
    if (!msg) return fallback;
    const line = msg.split(/\r?\n/)[0].trim();
    return line.length > 140 ? line.slice(0, 137) + "…" : line;
  }

  // The panel really does drop the session here: it restarts onto the new
  // store. ORDER MATTERS: the interstitial sits above the login (z 55 over
  // 50) so it closes LAST — the login opens underneath, hidden, and the
  // interstitial wipes away to reveal a screen that is already there.
  async function dbConnect() {
    try {
      await api.connectDatabase(db);
    } catch (e) {
      wz.dbRes = { cls: "bad", text: firstLine(e, "connect failed") };
      return;
    }
    ui.dbRestartOpen = true;
    delete ui.open.setup;
    const t0 = Date.now();
    const poll = setInterval(async () => {
      try {
        await fetch("/api/v1/version");
        if (Date.now() - t0 < TICK_MS) return; // at least one beat of the wipe
        clearInterval(poll);
        clearToken();
        auth.user = null; // the restarted panel holds no sessions
        wz.resumeSetup = true;
        ui.loginSub = "the panel is back on postgres. log in again and setup continues.";
        setTimeout(() => (ui.dbRestartOpen = false), 700);
      } catch {
        /* still restarting — keep polling */
      }
    }, 1000);
  }

  // -- step 3: enrollment (shared lifecycle with the add-node sheet)
  const enroll = new Enrollment();
  let mode = $state<EnrollMode>("tunnel");
  let os = $state<"linux" | "windows">("linux");
  const panelOrigin = location.origin;
  const tok = $derived(enroll.token || "<enrollment-token>");
  const fp = $derived(enroll.caFingerprint || "<ca-fingerprint>");
  const nodeOk = $derived(enroll.status === "online");

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

  // -- the catalog imports itself. The wizard has no manual import step: the
  // bundled specs land quietly when setup opens, and the deploy step's
  // footnote says so. A spec that fails to import stays pending and the
  // specs sheet still lists it.
  let catalog = $state<CatalogItem[]>([]);
  let catLoaded = false;
  $effect(() => {
    if (!open || catLoaded) return;
    catLoaded = true;
    void autoImport();
  });
  async function autoImport() {
    try {
      catalog = (await api.listCatalog()).catalog ?? [];
    } catch {
      return;
    }
    let imported = false;
    for (const item of catalog) {
      if (item.already_imported) continue;
      try {
        await api.importCatalog(item.id);
        item.already_imported = true;
        imported = true;
      } catch {
        /* stays pending; the specs sheet can still import it */
      }
    }
    if (imported) void refreshFleet();
  }

  // -- step 4: hand off into the create-server sheet
  function deploy() {
    closeSheet("setup");
    ui.nsFormNodeId = fleet.nodes[0]?.id ?? null;
    openSheet("nsForm", innerWidth / 2, innerHeight / 2, null);
  }

  // leaving the wizard is leaving it; "skip & finish" on the last step also
  // tells the panel to stop offering setup
  function skip() {
    closeSheet("setup");
  }
  function skipAndFinish() {
    void api.dismissSetup().catch(() => {});
    closeSheet("setup");
  }

  $effect(() => {
    if (!open) enroll.stop();
  });
</script>

<div
  class="sheet wz"
  class:open
  id="setup"
  role="dialog"
  aria-modal="true"
  aria-labelledby="wzTitle"
  use:istyle={`--ox: ${ui.open.setup?.ox ?? '50%'}; --oy: ${ui.open.setup?.oy ?? '50%'}`}
  use:sheetFocus
>
  <div class="depth-head">
    <div class="wz-head">
      <span class="wz-eyebrow">first run</span>
      <h2 class="depth-title" id="wzTitle">Get Kraken ready</h2>
    </div>
    <div class="prefs-note"></div>
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
            {#if step.n < 4}<span class="wz-line" aria-hidden="true"></span>{/if}
          </div>
        {/each}
      </div>

      <section class="wz-panel{wz.at === 1 ? ' on' : ''}" id="wzPanel1" aria-label="Step 1: database">
        <div class="prefs-group">
          <div class="cfg-head">
            <h3 class="pane-label">database</h3>
            <span class="cfg-badge {wz.dbMode === 'postgres' ? 'enc' : 'env'}" id="dbMode">{wz.dbMode}</span>
            {#if dbLocked}<span class="cfg-badge env">env-managed</span>{/if}
          </div>
          <div class="cfg cfg-2">
            <p class="cfg-desc cfg-wide">kraken is on the built-in <b>in-memory</b> store — everything resets when the panel restarts. connect postgres to persist. the database is created if it does not exist, migrations run, then the panel restarts and you sign in again.</p>
            <label class="cfg-row"><span>host</span><input class="cfg-in" id="dbHost" type="text" bind:value={db.host} disabled={dbLocked} spellcheck="false" autocapitalize="none" autocorrect="off" /></label>
            <label class="cfg-row"><span>port</span><input class="cfg-in" id="dbPort" type="text" value={String(db.port ?? 5432)} oninput={(e) => (db.port = +e.currentTarget.value || 5432)} disabled={dbLocked} inputmode="numeric" spellcheck="false" /></label>
            <label class="cfg-row"><span>user</span><input class="cfg-in" id="dbUser" type="text" bind:value={db.user} disabled={dbLocked} spellcheck="false" autocapitalize="none" autocorrect="off" /></label>
            <label class="cfg-row"><span>password</span><input class="cfg-in" id="dbPass" type="password" bind:value={db.password} disabled={dbLocked} autocomplete="off" /></label>
            <label class="cfg-row"><span>database</span><input class="cfg-in" id="dbName" type="text" bind:value={db.dbname} disabled={dbLocked} spellcheck="false" autocapitalize="none" autocorrect="off" /></label>
            <div class="cfg-row">
              <span>ssl mode</span>
              <select class="cfg-in" id="dbSsl" aria-label="SSL mode" bind:value={db.sslmode} disabled={dbLocked}>
                <button><selectedcontent></selectedcontent></button>
                <option value="disable">disable</option>
                <option value="require">require</option>
                <option value="verify-full">verify-full</option>
              </select>
            </div>
            <p class="wz-res cfg-wide{wz.dbRes ? ' shown ' + wz.dbRes.cls : ''}" id="dbRes" role="status" aria-live="polite">{#if wz.dbRes}<svg width="13" height="13" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 2.5 7.2 L 5.4 10 L 11.5 3.6"/></svg><span>{wz.dbRes.text}</span>{/if}</p>
          </div>
          <div class="cfg-actions">
            <button class="cfg-btn ghost" id="dbTest" disabled={dbLocked} onclick={() => void dbTest()}>test connection</button>
            <button class="cfg-btn solid" id="dbConnect" disabled={dbLocked || wz.dbMode === "postgres"} onclick={() => void dbConnect()}>connect &amp; restart</button>
            <span class="cfg-note">{wz.dbMode === "postgres" ? "already on postgres" : "the bundled postgres is already listening on 5432"}</span>
          </div>
        </div>
        <p class="cfg-help">or continue on the in-memory store for now — fine for trying things out, not for anything you would miss.</p>
        <div class="wz-foot">
          <button class="cfg-btn ghost" onclick={skip}>skip for now</button>
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
            <p class="cfg-desc">{fleet.nodes.length ? "connect another host, or skip ahead — a node is already online." : "no nodes yet. connect a remote host below, or run the agent on this machine for an all-in-one install."} the node names itself from its <code>KRAKEN_NODE_ID</code>.</p>
            <div class="cfg-row">
              <span>connection</span>
              <div class="opt-set">
                <label class="opt opt-inline"><input type="radio" name="wznconn" class="opt-r-in" value="tunnel" bind:group={mode} /><span class="opt-t">node dials the panel</span><span class="opt-n">no inbound ports — works behind nat</span></label>
                <label class="opt opt-inline"><input type="radio" name="wznconn" class="opt-r-in" value="direct" bind:group={mode} /><span class="opt-t">panel dials the node</span><span class="opt-n">needs inbound 9090 open on the node</span></label>
              </div>
            </div>
            <div class="wz-log{enroll.lines.length ? ' shown' : ''}" id="wzLog" role="status" aria-live="polite">
              {#each enroll.lines as entry}
                <span class={entry.pending ? "pending" : ""}><i>{entry.pending ? "▸" : "✓"}</i><span>{entry.text}</span></span>
              {/each}
            </div>
            {#if enroll.error}
              <p class="cfg-help" use:istyle={"color: var(--crisis)"}>{enroll.error}</p>
            {/if}
          </div>
          <div class="cfg-actions">
            <button class="cfg-btn solid" id="wzGenToken" disabled={enroll.status === "waiting" || enroll.status === "redeemed" || enroll.status === "registered"} onclick={() => void enroll.generate(mode, os)}>generate enrollment token</button>
            <span class="cfg-note">the token is valid once, for 15 minutes</span>
          </div>
        </div>

        <div class="prefs-group token-wrap{enroll.status !== 'idle' ? ' shown' : ''}" id="wzTokenWrap" aria-label="Enrollment token">
          <div class="cfg-head">
            <h3 class="pane-label">one-time enrollment token</h3>
            <span class="cfg-badge enc">one-time</span>
            <span class="cfg-badge env" id="wzEnrollState">{enroll.badge}</span>
          </div>
          <div class="cfg">
            <div class="cfg-ro" id="wzToken">{enroll.token || "—"}</div>
            <div class="os-tabs">
              <div class="os-tablist" role="tablist">
                <label class="os-tab"><input type="radio" name="wznos" class="os-r-in" value="linux" bind:group={os} />linux install</label>
                <label class="os-tab"><input type="radio" name="wznos" class="os-r-in" value="windows" bind:group={os} />windows install</label>
              </div>
              <div class="os-panel os-linux">
                <h4 class="os-step">1 · install + enroll + start (one command)</h4>
                <div class="cmd-wrap">
                  <pre class="cmd" id="wzCmdLinux" bind:this={cmdLinuxEl}>curl -fsSL https://raw.githubusercontent.com/briggleman/kraken/main/deploy/install.sh | \
  sudo bash -s -- --role agent --panel-url {panelOrigin} \
  --enroll-token <b class="tok">{tok}</b> \
  --ca-fingerprint <b>{fp}</b>{mode === "tunnel" ? " \\\n  --tunnel" : ""}</pre>
                  <button class="cfg-btn ghost cmd-copy" data-copy="wzCmdLinux" onclick={() => cmdCopy(0)}>{copyLabels[0]}</button>
                </div>
              </div>
              <div class="os-panel os-win">
                <h4 class="os-step">1 · install + enroll + start (elevated powershell, one command)</h4>
                <div class="cmd-wrap">
                  <pre class="cmd" id="wzCmdWin" bind:this={cmdWinEl}>iwr -useb https://raw.githubusercontent.com/briggleman/kraken/main/deploy/windows/install.ps1 -OutFile $env:TEMP\kraken-install.ps1
  powershell -ExecutionPolicy Bypass -File $env:TEMP\kraken-install.ps1 `
  -PanelUrl {panelOrigin} `
  -Token <b class="tok">{tok}</b> `
  -CaFingerprint <b>{fp}</b>{mode === "tunnel" ? " `\n  -Tunnel" : ""}</pre>
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
            <span><code>{enroll.nodeName || "the node"}</code> is connected and ready for deployments.</span>
          </span>
          <button class="cfg-btn ghost" onclick={() => wzGo(4)}>done</button>
        </div>

        <div class="wz-foot">
          <button class="cfg-btn ghost" onclick={() => wzGo(2)}>back</button>
          <div class="wz-fwd">
            <button class="cfg-btn ghost" onclick={skip}>skip</button>
            <button class="cfg-btn solid" id="wzNodeNext" disabled={!nodeOk && !fleet.nodes.some((n) => n.status === "online")} onclick={() => wzGo(4)}>continue</button>
          </div>
        </div>
      </section>

      <section class="wz-panel{wz.at === 4 ? ' on' : ''}" id="wzPanel4" aria-label="Step 4: deploy">
        <div class="prefs-group">
          <div class="wz-hero">
            <span class="wz-seal" aria-hidden="true"><svg width="30" height="30" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 2.5 7.2 L 5.4 10 L 11.5 3.6"/></svg></span>
            <h3>ready to deploy</h3>
            <p>a node is online and the catalog is imported. the last step is the first server — pick a game, place it, and kraken does the rest.</p>
            <button class="cfg-btn solid" id="wzDeployBtn" onclick={deploy}>deploy your first server</button>
          </div>
        </div>
        <p class="cfg-help">game specs import automatically at setup — the {catalog.length ? catalog.length + " " : ""}bundled games are ready in setup &amp; catalog, and you can author your own any time.</p>
        <div class="wz-foot">
          <button class="cfg-btn ghost" onclick={() => wzGo(3)}>back</button>
          <div class="wz-fwd"><button class="cfg-btn ghost" onclick={skipAndFinish}>skip &amp; finish</button></div>
        </div>
      </section>
    </div>
  </div>
</div>
