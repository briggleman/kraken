<script lang="ts">
  import { istyle } from "@/lib/istyle";
  import { ui, openSheet, closeSheet, sheetZ } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";
  import { api, errMsg } from "@/api/client";
  import { fleet } from "@/lib/fleet.svelte";
  import { auth } from "@/lib/auth.svelte";
  import type { AdminUser, DatabaseConfig, PanelSettings } from "@/api/types";

  type Res = { cls: "ok" | "bad"; text: string } | null;
  type SettingsPatch = Parameters<typeof api.updatePanelSettings>[0];

  let settings = $state<PanelSettings | null>(null);
  let db = $state<DatabaseConfig | null>(null);
  let users = $state<AdminUser[]>([]);
  let loadErr = $state<string | null>(null);

  // cloudflare — the token is write-only; the input never echoes a stored value
  let cfToken = $state("");
  let cfBusy = $state(false);
  let cfRes = $state<Res>(null);

  // unifi — url/site/tls are readable; the api key is write-only
  let uUrl = $state("");
  let uSite = $state("");
  let uKey = $state("");
  let uVerify = $state(false);
  let uBusy = $state(false);
  let uRes = $state<Res>(null);

  // email (smtp) — the password is write-only; everything else is readable
  type Security = PanelSettings["smtp_security"];
  let mHost = $state("");
  let mPort = $state("587");
  let mSec = $state<Security>("starttls");
  let mUser = $state("");
  let mPass = $state("");
  let mFrom = $state("");
  let mFromName = $state("");
  let mTestTo = $state("");
  let mBusy = $state(false);
  let mRes = $state<Res>(null);

  // sessions & security
  let sessTtl = $state("");
  let origins = $state("");
  let pubUrl = $state("");
  let bootstrapEnabled = $state(true);
  let secBusy = $state(false);
  let secRes = $state<Res>(null);

  function seed(s: PanelSettings) {
    settings = s;
    uUrl = s.unifi_url ?? "";
    uSite = s.unifi_site ?? "";
    uVerify = s.unifi_verify_tls;
    mHost = s.smtp_host ?? "";
    mPort = String(s.smtp_port || 587);
    mSec = s.smtp_security || "starttls";
    mUser = s.smtp_username ?? "";
    mFrom = s.smtp_from ?? "";
    mFromName = s.smtp_from_name ?? "";
    pubUrl = s.public_url ?? "";
    sessTtl = String(s.session_ttl_seconds);
    origins = (s.allowed_origins ?? []).join(", ");
    bootstrapEnabled = !s.bootstrap_disabled;
  }

  async function load() {
    loadErr = null;
    const [s, d, u] = await Promise.allSettled([
      api.getPanelSettings(),
      api.getDatabaseConfig(),
      api.listUsers(),
    ]);
    if (s.status === "fulfilled") seed(s.value);
    else loadErr = errMsg(s.reason);
    if (d.status === "fulfilled") db = d.value;
    if (u.status === "fulfilled") users = u.value.users ?? [];
  }

  // load real values every time the sheet opens (each open mints a new origin object)
  $effect(() => {
    if (!ui.open.prefs) return;
    cfToken = "";
    uKey = "";
    mPass = "";
    cfRes = null;
    uRes = null;
    mRes = null;
    secRes = null;
    void load();
  });

  async function cfSave() {
    const tok = cfToken.trim();
    if (!tok || cfBusy) return;
    cfBusy = true;
    cfRes = null;
    try {
      const s = await api.updatePanelSettings({ cloudflare_api_token: tok });
      seed(s);
      cfToken = "";
      cfRes = { cls: "ok", text: s.cloudflare_configured ? "token saved — stored encrypted, never shown again." : "token cleared." };
    } catch (e) {
      cfRes = { cls: "bad", text: errMsg(e) };
    }
    cfBusy = false;
  }

  async function cfTest() {
    if (cfBusy) return;
    cfBusy = true;
    cfRes = null;
    try {
      const r = await api.testCloudflare();
      const zones = r.zones ?? [];
      cfRes = {
        cls: "ok",
        text: zones.length
          ? `connected — ${zones.length} zone${zones.length === 1 ? "" : "s"}: ${zones.join(", ")}`
          : "connected — no zones reachable with this token.",
      };
    } catch (e) {
      cfRes = { cls: "bad", text: errMsg(e) };
    }
    cfBusy = false;
  }

  async function uSave() {
    if (uBusy || !settings) return;
    uBusy = true;
    uRes = null;
    const patch: SettingsPatch = {};
    if (uUrl.trim() !== (settings.unifi_url ?? "")) patch.unifi_url = uUrl.trim();
    if (uSite.trim() !== (settings.unifi_site ?? "")) patch.unifi_site = uSite.trim();
    if (uVerify !== settings.unifi_verify_tls) patch.unifi_verify_tls = uVerify;
    if (uKey.trim()) patch.unifi_api_key = uKey.trim();
    if (!Object.keys(patch).length) {
      uRes = { cls: "ok", text: "nothing changed." };
      uBusy = false;
      return;
    }
    try {
      const s = await api.updatePanelSettings(patch);
      seed(s);
      uKey = "";
      uRes = { cls: "ok", text: s.unifi_configured ? "unifi settings saved." : "saved — no api key stored yet." };
    } catch (e) {
      uRes = { cls: "bad", text: errMsg(e) };
    }
    uBusy = false;
  }

  async function uTest() {
    if (uBusy) return;
    uBusy = true;
    uRes = null;
    try {
      const r = await api.testUnifi();
      uRes = {
        cls: "ok",
        text: `connected — ${r.forward_count} forward${r.forward_count === 1 ? "" : "s"}${r.wan_ip ? ` · wan ${r.wan_ip}` : ""}`,
      };
    } catch (e) {
      uRes = { cls: "bad", text: errMsg(e) };
    }
    uBusy = false;
  }

  async function mSave() {
    if (mBusy || !settings) return;
    mBusy = true;
    mRes = null;
    const patch: SettingsPatch = {};
    const port = Number(mPort.trim() || "587");
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      mRes = { cls: "bad", text: "the port must be a whole number between 1 and 65535." };
      mBusy = false;
      return;
    }
    if (mHost.trim() !== (settings.smtp_host ?? "")) patch.smtp_host = mHost.trim();
    if (port !== settings.smtp_port) patch.smtp_port = port;
    if (mSec !== settings.smtp_security) patch.smtp_security = mSec;
    if (mUser.trim() !== (settings.smtp_username ?? "")) patch.smtp_username = mUser.trim();
    if (mPass) patch.smtp_password = mPass;
    if (mFrom.trim() !== (settings.smtp_from ?? "")) patch.smtp_from = mFrom.trim();
    if (mFromName.trim() !== (settings.smtp_from_name ?? "")) patch.smtp_from_name = mFromName.trim();
    if (!Object.keys(patch).length) {
      mRes = { cls: "ok", text: "nothing changed." };
      mBusy = false;
      return;
    }
    try {
      const s = await api.updatePanelSettings(patch);
      seed(s);
      mPass = "";
      mRes = {
        cls: "ok",
        text: s.smtp_configured
          ? "email settings saved — the password is stored encrypted and never shown again."
          : "saved — a relay host and a from address are both needed before anything can be sent.",
      };
    } catch (e) {
      mRes = { cls: "bad", text: errMsg(e) };
    }
    mBusy = false;
  }

  async function mTest() {
    if (mBusy) return;
    mBusy = true;
    mRes = null;
    try {
      const r = await api.testSmtp(mTestTo.trim() || undefined);
      mRes = { cls: "ok", text: `sent to ${r.sent_to} — the relay said "${r.server_reply}" · message id ${r.message_id}` };
    } catch (e) {
      mRes = { cls: "bad", text: errMsg(e) };
    }
    mBusy = false;
  }

  // a public address the allowed-origins list can suggest: exactly one entry, no wildcard
  const pubSuggest = $derived.by(() => {
    if (pubUrl.trim() || settings?.public_url_locked) return "";
    const list = (settings?.allowed_origins ?? []).map((o) => o.trim()).filter(Boolean);
    if (list.length !== 1 || list[0].includes("*")) return "";
    return list[0].startsWith("http") ? list[0] : "https://" + list[0];
  });

  async function secSave() {
    if (secBusy || !settings) return;
    secBusy = true;
    secRes = null;
    const patch: SettingsPatch = {};
    if (!settings.public_url_locked && pubUrl.trim() !== (settings.public_url ?? "")) {
      patch.public_url = pubUrl.trim();
    }
    if (!settings.session_ttl_locked) {
      const n = Number(sessTtl.trim());
      if (!Number.isInteger(n) || n <= 0) {
        secRes = { cls: "bad", text: "session lifetime must be a positive number of seconds." };
        secBusy = false;
        return;
      }
      if (n !== settings.session_ttl_seconds) patch.session_ttl_seconds = n;
    }
    if (!settings.allowed_origins_locked) {
      const list = origins.split(",").map((o) => o.trim()).filter(Boolean);
      if (list.join(", ") !== (settings.allowed_origins ?? []).join(", ")) patch.allowed_origins = list;
    }
    if (!settings.bootstrap_locked && bootstrapEnabled !== !settings.bootstrap_disabled) {
      patch.bootstrap_disabled = !bootstrapEnabled;
    }
    if (!Object.keys(patch).length) {
      secRes = { cls: "ok", text: "nothing changed." };
      secBusy = false;
      return;
    }
    try {
      const s = await api.updatePanelSettings(patch);
      seed(s);
      secRes = { cls: "ok", text: "security settings saved." };
    } catch (e) {
      secRes = { cls: "bad", text: errMsg(e) };
    }
    secBusy = false;
  }

  // readouts derived from live values
  const ttlHours = $derived.by(() => {
    const n = Number(sessTtl.trim());
    return Number.isFinite(n) && n > 0 ? (n / 3600).toFixed(1) : "?";
  });

  const dbLine = $derived.by(() => {
    if (!db) return "—";
    if (db.using_memory) return "in-memory — resets on restart";
    return `${db.user ?? "?"}@${db.host ?? "?"}${db.port ? ":" + db.port : ""}/${db.dbname ?? "?"} · sslmode=${db.sslmode ?? "?"}`;
  });

  const userCount = $derived(users.length);
  const disabledCount = $derived(users.filter((u) => u.disabled).length);
  const rolesInUse = $derived.by(() => {
    const counts = new Map<string, number>();
    for (const u of users) counts.set(u.role_id, (counts.get(u.role_id) ?? 0) + 1);
    // built-ins get their reading-order and friendly plural; unknown role ids trail verbatim
    const parts: string[] = [];
    const take = (id: string, one: string, many: string) => {
      const n = counts.get(id);
      if (n) parts.push(`${n} ${n === 1 ? one : many}`);
      counts.delete(id);
    };
    take("owner", "owner", "owners");
    take("admin", "admin", "admins");
    take("operator", "operator", "operators");
    take("readonly", "viewer", "viewers");
    for (const [id, n] of counts) parts.push(`${n} ${id}`);
    return parts.join(" · ") || "no accounts yet";
  });

  const specCount = $derived(fleet.specs.length);
  const platformsCovered = $derived.by(() => {
    let linux = 0;
    let windows = 0;
    let wine = 0;
    for (const sp of fleet.specs) {
      for (const p of sp.platforms) {
        if (p.kind === "linux-native") linux++;
        else if (p.kind === "windows-native") windows++;
        else if (p.kind === "linux-wine") wine++;
      }
    }
    return `${linux} linux · ${windows} windows · ${wine} wine`;
  });
</script>

<div
  class="prefs"
  class:open={!!ui.open.prefs}
  id="prefs"
  role="dialog"
  aria-modal="true"
  aria-labelledby="prefsTitle"
  use:istyle={`--ox: ${ui.open.prefs?.ox ?? '50%'}; --oy: ${ui.open.prefs?.oy ?? '50%'}; z-index: ${sheetZ("prefs")}`}
  use:sheetFocus
>
  <div class="depth-head">
    <button class="surface-btn" id="prefsSurfaceBtn" onclick={() => closeSheet("prefs")}>
      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6 10 V 2 M 2 6 L 6 2 L 10 6"/></svg>
      surface
    </button>
    <h2 class="depth-title" id="prefsTitle">console settings</h2>
    {#if loadErr}
      <div class="prefs-note"><span class="synthetic">settings failed to load — {loadErr}</span></div>
    {/if}
  </div>
  <div class="prefs-body pb-split">
    <div class="pb-col">
      <div class="pb-col-head"><h3 class="pane-label">integrations</h3><span class="pb-col-note">things you configure here</span></div>
      <section class="prefs-group" aria-label="Cloudflare DNS">
        <div class="cfg-head"><h3 class="pane-label">cloudflare dns</h3>{#if settings?.cloudflare_configured}<span class="cfg-badge enc">encrypted</span><span class="cfg-badge ok">configured</span>{:else}<span class="cfg-badge env">not set</span>{/if}</div>
        <div class="cfg"><p class="cfg-desc">a scoped cloudflare api token (dns edit) lets servers publish a dns name to your domains.</p><label class="cfg-row"><span>replace api token</span><input class="cfg-in" type="password" placeholder="•••••••••• (leave blank to keep)" autocomplete="off" bind:value={cfToken} /><p class="cfg-help">stored server-side and never shown again.</p></label><p class="wz-res{cfRes ? ' shown ' + cfRes.cls : ''}" role="status" aria-live="polite">{#if cfRes}<svg width="13" height="13" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 2.5 7.2 L 5.4 10 L 11.5 3.6"/></svg><span>{cfRes.text}</span>{/if}</p></div>
        <div class="cfg-actions"><button class="cfg-btn ghost" disabled={cfBusy || !settings?.cloudflare_configured} onclick={() => void cfTest()}>{cfBusy ? "testing…" : "test connection"}</button><button class="cfg-btn solid" disabled={cfBusy || !cfToken.trim()} onclick={() => void cfSave()}>save token</button></div>
      </section>
      <section class="prefs-group" aria-label="UniFi gateway">
        <div class="cfg-head"><h3 class="pane-label">unifi gateway</h3>{#if settings?.unifi_configured}<span class="cfg-badge enc">encrypted</span><span class="cfg-badge ok">configured</span>{:else}<span class="cfg-badge env">not set</span>{/if}</div>
        <div class="cfg"><p class="cfg-desc">a unifi os api key lets servers open port forwards on your gateway.</p><label class="cfg-row"><span>controller url</span><input class="cfg-in" type="text" placeholder="https://192.168.1.1" autocomplete="off" bind:value={uUrl} /></label><label class="cfg-row"><span>replace api key</span><input class="cfg-in" type="password" placeholder="•••••••••• (leave blank to keep)" autocomplete="off" bind:value={uKey} /></label><label class="cfg-row"><span>site</span><input class="cfg-in" type="text" placeholder="default" autocomplete="off" bind:value={uSite} /></label><label class="tgl "><input type="checkbox" bind:checked={uVerify} /><i></i>verify tls certificate</label><p class="cfg-help">off by default — unifi gateways ship with a self-signed cert on the lan. turn on once a trusted certificate is installed on the controller.</p><p class="wz-res{uRes ? ' shown ' + uRes.cls : ''}" role="status" aria-live="polite">{#if uRes}<svg width="13" height="13" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 2.5 7.2 L 5.4 10 L 11.5 3.6"/></svg><span>{uRes.text}</span>{/if}</p></div>
        <div class="cfg-actions"><button class="cfg-btn ghost" disabled={uBusy || !settings?.unifi_configured} onclick={() => void uTest()}>{uBusy ? "testing…" : "test connection"}</button><button class="cfg-btn solid" disabled={uBusy} onclick={() => void uSave()}>save</button></div>
      </section>
      <section class="prefs-group" aria-label="Outbound email">
        <div class="cfg-head"><h3 class="pane-label">email (smtp)</h3>{#if settings?.smtp_configured}<span class="cfg-badge enc">encrypted</span><span class="cfg-badge ok">configured</span>{:else}<span class="cfg-badge env">not set</span>{/if}</div>
        <div class="cfg">
          <p class="cfg-desc">a relay the panel can send through lets you invite people by email instead of handing them a password. nothing is sent until a host and a from address are set.</p>
          <label class="cfg-row"><span>relay host</span><input class="cfg-in" type="text" placeholder="smtp.example.com" autocomplete="off" bind:value={mHost} /></label>
          <label class="cfg-row"><span>port</span><input class="cfg-in" type="text" inputmode="numeric" placeholder="587" autocomplete="off" bind:value={mPort} /></label>
          <label class="cfg-row"><span>security</span>
            <select class="cfg-in" bind:value={mSec}>
              <option value="starttls">starttls — upgrade the connection first (port 587, the usual)</option>
              <option value="tls">tls — encrypted from the first byte (port 465)</option>
              <option value="none">none — a relay on your own network only</option>
            </select>
          </label>
          <label class="cfg-row"><span>username</span><input class="cfg-in" type="text" placeholder="leave empty for a relay without a login" autocomplete="off" bind:value={mUser} /></label>
          <label class="cfg-row"><span>replace password</span><input class="cfg-in" type="password" placeholder="•••••••••• (leave blank to keep)" autocomplete="new-password" bind:value={mPass} /><p class="cfg-help">stored server-side, encrypted, and never shown again.</p></label>
          <label class="cfg-row"><span>from address</span><input class="cfg-in" type="email" placeholder="kraken@example.com" autocomplete="off" bind:value={mFrom} /></label>
          <label class="cfg-row"><span>from name</span><input class="cfg-in" type="text" placeholder="Kraken" autocomplete="off" bind:value={mFromName} /></label>
          {#if mSec === "none"}<p class="wz-res shown bad" role="note"><span>none sends the relay login and every message in the clear. a password is refused on that path unless the relay is this very host.</span></p>{/if}
          <p class="cfg-help">deliverability: the from address's domain needs spf, dkim and dmarc records published and aligned, and the relay should be a provider's submission port (587) rather than a home ip. the test below tells you what the relay said; the wiki page <b>outbound email</b> has the checklist.</p>
          <label class="cfg-row"><span>send a test to</span><input class="cfg-in" type="email" placeholder={auth.user?.email || "you@example.com"} autocomplete="off" bind:value={mTestTo} /></label>
          <p class="wz-res{mRes ? ' shown ' + mRes.cls : ''}" role="status" aria-live="polite">{#if mRes}<svg width="13" height="13" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 2.5 7.2 L 5.4 10 L 11.5 3.6"/></svg><span>{mRes.text}</span>{/if}</p>
        </div>
        <div class="cfg-actions"><button class="cfg-btn ghost" disabled={mBusy || !settings?.smtp_configured} onclick={() => void mTest()}>{mBusy ? "working…" : "send test email"}</button><button class="cfg-btn solid" disabled={mBusy} onclick={() => void mSave()}>save</button></div>
      </section>
    </div>
    <div class="pb-col">
      <div class="pb-col-head"><h3 class="pane-label">platform</h3><span class="pb-col-note">mostly pinned by the environment</span></div>
      <section class="prefs-group" aria-label="Database">
        <div class="cfg-head"><h3 class="pane-label">database</h3>{#if db?.env_locked}<span class="cfg-badge env">env-managed</span>{:else if db?.using_memory}<span class="cfg-badge env">in-memory</span>{:else if db}<span class="cfg-badge ok">postgres</span>{/if}</div>
        <div class="cfg"><div class="cfg-row"><div class="cfg-ro">{dbLine}</div><p class="cfg-help">{db?.env_locked ? "managed via KRAKEN_DATABASE_URL." : db?.using_memory ? "not persisted — connect postgres during setup to keep anything." : "configured during first-run setup."}</p></div></div>
      </section>
      <section class="prefs-group" aria-label="Sessions and security">
        <div class="cfg-head"><h3 class="pane-label">sessions &amp; security</h3></div>
        <div class="cfg"><label class="cfg-row"><span>session lifetime {#if settings?.session_ttl_locked}<span class="cfg-badge env">env-managed</span>{/if}</span><input class="cfg-in" type="text" bind:value={sessTtl} disabled={settings?.session_ttl_locked ?? false} /><p class="cfg-help">{settings?.session_ttl_locked ? "managed via KRAKEN_SESSION_TTL — " : "duration in seconds — "}≈ {ttlHours}h a login stays valid.</p></label><label class="cfg-row"><span>allowed websocket origins {#if settings?.allowed_origins_locked}<span class="cfg-badge env">env-managed</span>{/if}</span><input class="cfg-in" type="text" bind:value={origins} disabled={settings?.allowed_origins_locked ?? false} placeholder="panel.example.com, *.example.com" /><p class="cfg-help">{settings?.allowed_origins_locked ? "managed via KRAKEN_ALLOWED_ORIGINS." : "comma-separated. empty falls back to localhost dev origins; same-origin is always allowed."}</p></label><label class="cfg-row"><span>public address {#if settings?.public_url_locked}<span class="cfg-badge env">env-managed</span>{/if}</span><input class="cfg-in" type="url" bind:value={pubUrl} disabled={settings?.public_url_locked ?? false} placeholder="https://panel.example.com" autocomplete="off" /><p class="cfg-help">{settings?.public_url_locked ? "managed via KRAKEN_PUBLIC_URL — " : ""}the address people reach this panel at. it is the only thing links in email are built from; the address a request arrived on is never used.{#if pubSuggest} <button type="button" class="mini-act" onclick={() => (pubUrl = pubSuggest)}>use {pubSuggest}</button>{/if}</p>{#if pubUrl.trim().startsWith("http://")}<p class="wz-res shown bad" role="note"><span>an http:// address means an invite link's one-time token travels in the clear.</span></p>{/if}</label><label class="tgl{settings?.bootstrap_locked ? ' is-locked' : ''}"><input type="checkbox" bind:checked={bootstrapEnabled} disabled={settings?.bootstrap_locked ?? false} /><i></i>auto-create the bootstrap admin ({settings?.bootstrap_user || "admin"}) when no users exist</label><p class="cfg-help">{settings?.bootstrap_locked ? "read-only — pinned via the KRAKEN_BOOTSTRAP_ADMIN_* env vars." : `the bootstrap admin (${settings?.bootstrap_user || "admin"}) is created at first start when the instance has no users.`}</p></div>
        <div class="cfg-actions">{#if secRes}<span class="cfg-note wz-res shown {secRes.cls}">{secRes.text}</span>{:else}<span class="cfg-note">env-managed rows cannot be edited here</span>{/if}<button class="cfg-btn solid" disabled={secBusy || !settings || (settings.session_ttl_locked && settings.allowed_origins_locked && settings.bootstrap_locked && settings.public_url_locked)} onclick={() => void secSave()}>{secBusy ? "saving…" : "save"}</button></div>
      </section>
      <section class="prefs-group" aria-label="Users and access">
        <div class="cfg-head"><h3 class="pane-label">users &amp; access</h3><span class="cfg-badge ok">{userCount} account{userCount === 1 ? "" : "s"}</span></div>
        <div class="cfg"><p class="cfg-desc">sessions above decide how long a login lasts; this decides who gets one, and what they may do once they are in.</p><div class="cfg-row"><span>roles in use</span><div class="cfg-ro">{rolesInUse}</div></div><p class="cfg-help">the bootstrap admin above holds the owner role until another account is promoted. every sign-in, invite and role change lands in the audit log.</p></div>
        <div class="cfg-actions"><span class="cfg-note">{disabledCount ? `${disabledCount} account${disabledCount === 1 ? " is" : "s are"} disabled` : "all accounts are enabled"}</span><button class="cfg-btn solid" onclick={(e) => openSheet("users", e.clientX, e.clientY, e.currentTarget)}>manage users</button></div>
      </section>
      <section class="prefs-group" aria-label="Game specs">
        <div class="cfg-head"><h3 class="pane-label">game specs</h3><span class="cfg-badge ok">{specCount} spec{specCount === 1 ? "" : "s"}</span></div>
        <div class="cfg"><p class="cfg-desc">the recipe for each game: which image runs it, how it installs, how it starts and stops, and which ports and variables a server may set.</p><div class="cfg-row"><span>platforms covered</span><div class="cfg-ro">{platformsCovered}</div></div><p class="cfg-help">the new server sheet reads its game list from here, so a game missing there is a spec missing here. rarely touched once a game works.</p></div>
        <div class="cfg-actions"><span class="cfg-note">a spec is shared by every server of that game, so it is edited here rather than per server</span><button class="cfg-btn solid" onclick={(e) => openSheet("specs", e.clientX, e.clientY, e.currentTarget)}>manage specs</button></div>
      </section>
    </div>
  </div>
</div>
