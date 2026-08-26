<script lang="ts">
  import { ui, closeSheet, openConfirm } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";

  // the mock's data-confirm-body, passed through to the typed-confirm dialog
  const DELETE_BODY =
    "deleting this spec leaves servers built from it running, but nothing can be reinstalled or redeployed from it. it cannot be undone.";
</script>

<div
  class="sheet"
  class:open={!!ui.open.specEdit}
  id="specEdit"
  role="dialog"
  aria-modal="true"
  aria-labelledby="specEditTitle"
  style="--ox: {ui.open.specEdit?.ox ?? '50%'}; --oy: {ui.open.specEdit?.oy ?? '50%'}"
  use:sheetFocus
>
  <div class="depth-head">
    <button class="surface-btn" onclick={() => closeSheet("specEdit")}>
      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6 10 V 2 M 2 6 L 6 2 L 10 6"/></svg>
      surface
    </button>
    <h2 class="depth-title" id="specEditTitle">edit spec <b class="node-name-inline">abiotic factor</b></h2>
    <div class="prefs-note"><span class="synthetic">values are sample — surface not wired</span></div>
  </div>
  <div class="sheet-body spec-edit-body">
    <div class="audit-bar">
      <div class="os-tablist" role="tablist" aria-label="Editor view">
        <label class="os-tab sv-form"><input class="os-r-in" type="radio" name="sv" checked />form</label>
        <label class="os-tab sv-code"><input class="os-r-in" type="radio" name="sv" />code</label>
      </div>
      <span class="audit-count">spec 38c20f8c · v1 · 2 platforms · 4 variables · 2 ports</span>
      <button class="cfg-btn ghost" onclick={() => closeSheet("specEdit")}>cancel</button>
      <button class="cfg-btn solid">save</button>
    </div>

    <div class="spec-form">
      <section class="prefs-group" aria-label="Identity">
        <div class="cfg-head"><h3 class="pane-label">identity</h3></div>
        <div class="cfg">
          <label class="cfg-row"><span>name</span><input class="cfg-in" type="text" value="Abiotic Factor" /></label>
          <label class="cfg-row"><span>slug</span><input class="cfg-in" type="text" value="abiotic-factor" /></label>
          <label class="cfg-row"><span>steam app id — linux</span><input class="cfg-in" type="text" value="" placeholder="—" /></label>
          <label class="cfg-row"><span>steam app id — windows</span><input class="cfg-in" type="text" value="2857200" /></label>
          <label class="cfg-row"><span>banner url</span><input class="cfg-in" type="text" value="" placeholder="https://… (empty — the list shows &quot;no banner&quot;)" /></label>
          <label class="cfg-row"><span>icon url</span><input class="cfg-in" type="text" value="" placeholder="https://…" /></label>
          <label class="cfg-row spec-wide"><span>description</span><input class="cfg-in" type="text" value="Dedicated server. Ships as a Windows build; runs on a windows node, or on linux under wine." /></label>
        </div>
      </section>

      <section class="prefs-group" aria-label="Platforms">
        <div class="cfg-head"><h3 class="pane-label">platforms</h3><span class="cfg-badge env">2 defined</span><button class="cfg-btn ghost spec-add">add platform</button></div>
        <div class="ns-pad">
          <div class="spec-rows">
            <div class="spec-sub plats">
              <div class="cfg-row"><span>kind</span><select class="cfg-in"><option selected>windows-native</option><option>linux-native</option><option>linux-wine</option></select></div>
              <label class="cfg-row"><span>image</span><input class="cfg-in" type="text" value="ghcr.io/&lt;owner&gt;/kraken-steam-win:ltsc2022" /></label>
              <button class="mini-act del spec-drop">drop</button>
            </div>
            <div class="spec-sub plats">
              <div class="cfg-row"><span>kind</span><select class="cfg-in"><option>windows-native</option><option>linux-native</option><option selected>linux-wine</option></select></div>
              <label class="cfg-row"><span>image</span><input class="cfg-in" type="text" value="ghcr.io/&lt;owner&gt;/kraken-steam-wine:latest" /></label>
              <button class="mini-act del spec-drop">drop</button>
              <label class="cfg-row spec-full"><span>install script override — optional, replaces the spec install for this platform</span><input class="cfg-in" type="text" value="steamcmd +@sSteamCmdForcePlatformType windows +force_install_dir /data +login anonymous +app_update &#123;&#123;APP_ID&#125;&#125; validate +quit" /></label>
              <label class="cfg-row spec-full"><span>startup command override — optional, replaces the spec startup for this platform</span><input class="cfg-in" type="text" value="wine-headless /data/AbioticFactor/Binaries/Win64/AbioticFactorServer-Win64-Shipping.exe -log -PORT=&#123;&#123;PORT_GAME&#125;&#125;" /></label>
            </div>
          </div>
        </div>
      </section>

      <section class="prefs-group" aria-label="Install">
        <div class="cfg-head"><h3 class="pane-label">install</h3></div>
        <div class="cfg">
          <label class="cfg-row spec-wide"><span>script</span><input class="cfg-in" type="text" value="steamcmd.exe +force_install_dir C:\data +login anonymous +app_update &#123;&#123;APP_ID&#125;&#125; validate +quit" /></label>
          <label class="tgl"><input type="checkbox" checked /><i></i>bepinex compatible — unity games with mod support</label>
          <label class="tgl"><input type="checkbox" /><i></i>requires steam login — a real account and 2fa, not anonymous</label>
          <p class="cfg-help">anonymous install is the default. turning on steam login means the node needs credentials of its own; kraken never holds them in the browser.</p>
        </div>
      </section>

      <section class="prefs-group" aria-label="Startup">
        <div class="cfg-head"><h3 class="pane-label">startup</h3></div>
        <div class="cfg">
          <label class="cfg-row spec-wide"><span>command</span><input class="cfg-in" type="text" value="AbioticFactorServer-Win64-Shipping.exe -log -PORT=&#123;&#123;PORT_GAME&#125;&#125; -QueryPort=&#123;&#123;PORT_QUERY&#125;&#125; -MaxServerPlayers=&#123;&#123;MAX_PLAYERS&#125;&#125;" /></label>
          <label class="cfg-row spec-wide"><span>ready regex — optional</span><input class="cfg-in" type="text" value="Server is ready" /></label>
          <div class="cfg-row"><span>stop type</span><select class="cfg-in"><option selected>signal</option><option>command</option></select></div>
          <label class="cfg-row"><span>stop value</span><input class="cfg-in" type="text" value="SIGINT" /></label>
          <p class="cfg-help">the ready regex is how the panel knows "started" means playable rather than merely running — it is what the running chip waits for.</p>
        </div>
      </section>

      <section class="prefs-group" aria-label="Variables">
        <div class="cfg-head"><h3 class="pane-label">variables</h3><span class="cfg-badge env">4 defined</span><button class="cfg-btn ghost spec-add">add variable</button></div>
        <div class="ns-pad">
          <div class="spec-rows">
            <div class="spec-sub vars"><label class="cfg-row"><span>key</span><input class="cfg-in" type="text" value="SERVER_NAME" /></label><label class="cfg-row"><span>label</span><input class="cfg-in" type="text" value="server name" /></label><label class="cfg-row"><span>default</span><input class="cfg-in" type="text" value="Feybreak" /></label><label class="cfg-row"><span>rules</span><input class="cfg-in" type="text" value="required · max 48" /></label><label class="tgl"><input type="checkbox" checked /><i></i>editable</label><button class="mini-act del spec-drop">drop</button></div>
            <div class="spec-sub vars"><label class="cfg-row"><span>key</span><input class="cfg-in" type="text" value="MAX_PLAYERS" /></label><label class="cfg-row"><span>label</span><input class="cfg-in" type="text" value="max players" /></label><label class="cfg-row"><span>default</span><input class="cfg-in" type="text" value="16" /></label><label class="cfg-row"><span>rules</span><input class="cfg-in" type="text" value="1–32" /></label><label class="tgl"><input type="checkbox" checked /><i></i>editable</label><button class="mini-act del spec-drop">drop</button></div>
            <div class="spec-sub vars"><label class="cfg-row"><span>key</span><input class="cfg-in" type="text" value="WORLD_NAME" /></label><label class="cfg-row"><span>label</span><input class="cfg-in" type="text" value="world name" /></label><label class="cfg-row"><span>default</span><input class="cfg-in" type="text" value="feybreak-01" /></label><label class="cfg-row"><span>rules</span><input class="cfg-in" type="text" value="required · slug" /></label><label class="tgl"><input type="checkbox" checked /><i></i>editable</label><button class="mini-act del spec-drop">drop</button></div>
            <div class="spec-sub vars"><label class="cfg-row"><span>key</span><input class="cfg-in" type="text" value="SERVER_PASSWORD" /></label><label class="cfg-row"><span>label</span><input class="cfg-in" type="text" value="join password" /></label><label class="cfg-row"><span>default</span><input class="cfg-in" type="text" value="" /></label><label class="cfg-row"><span>rules</span><input class="cfg-in" type="text" value="optional · min 6" /></label><label class="tgl"><input type="checkbox" checked /><i></i>editable</label><button class="mini-act del spec-drop">drop</button></div>
          </div>
        </div>
      </section>

      <section class="prefs-group" aria-label="Ports">
        <div class="cfg-head"><h3 class="pane-label">ports</h3><span class="cfg-badge env">2 defined</span><button class="cfg-btn ghost spec-add">add port</button></div>
        <div class="ns-pad">
          <div class="spec-rows">
            <div class="spec-sub ports"><label class="cfg-row"><span>name</span><input class="cfg-in" type="text" value="PORT_GAME" /></label><div class="cfg-row"><span>protocol</span><select class="cfg-in"><option selected>udp</option><option>tcp</option></select></div><label class="cfg-row"><span>default</span><input class="cfg-in" type="text" value="8211" /></label><label class="tgl"><input type="checkbox" checked /><i></i>required</label><button class="mini-act del spec-drop">drop</button></div>
            <div class="spec-sub ports"><label class="cfg-row"><span>name</span><input class="cfg-in" type="text" value="PORT_QUERY" /></label><div class="cfg-row"><span>protocol</span><select class="cfg-in"><option selected>udp</option><option>tcp</option></select></div><label class="cfg-row"><span>default</span><input class="cfg-in" type="text" value="27015" /></label><label class="tgl"><input type="checkbox" /><i></i>required</label><button class="mini-act del spec-drop">drop</button></div>
          </div>
        </div>
      </section>

      <section class="prefs-group" aria-label="Resources">
        <div class="cfg-head"><h3 class="pane-label">resources</h3></div>
        <div class="cfg">
          <label class="cfg-row"><span>min memory (mb)</span><input class="cfg-in" type="text" value="4096" /></label>
          <label class="cfg-row"><span>recommended (mb)</span><input class="cfg-in" type="text" value="8192" /></label>
          <p class="cfg-help">the minimum is enforced when a server is created; the recommendation is what the new server sheet fills in.</p>
        </div>
      </section>

      <section class="prefs-group" aria-label="Settings groups">
        <div class="cfg-head"><h3 class="pane-label">settings groups</h3><span class="cfg-badge env">none</span><button class="cfg-btn ghost spec-add">add group</button></div>
        <div class="cfg">
          <label class="tgl"><input type="checkbox" /><i></i>hot reload — the game re-reads its config files live, so saved settings apply without a restart</label>
          <p class="cfg-help">no settings groups yet. these render game options into config files. config file bindings and templates are kept but can only be edited in the code view.</p>
        </div>
      </section>

      <div class="cfg-actions">
        <span class="cfg-note">deleting a spec leaves servers built from it running, but they can no longer be reinstalled</span>
        <button class="cfg-btn danger" data-confirm-open="spec" data-confirm-name="abiotic factor" data-confirm-body={DELETE_BODY} onclick={(e) => openConfirm("abiotic factor", e.currentTarget, { noun: "spec", body: DELETE_BODY })}>delete spec</button>
        <button class="cfg-btn solid">save</button>
      </div>
    </div>

    <div class="spec-code-wrap">
      <span class="cd-sw"><label class="cd-opt"><input class="cd-r" type="radio" name="specfmt" checked />json</label><label class="cd-opt"><input class="cd-r r-y" type="radio" name="specfmt" />yaml</label></span>
      <pre class="spec-code"><span class="doc j"><span class="l">&#123;</span><span class="l">  <b>"name"</b>: <i>"Abiotic Factor"</i>,</span><span class="l">  <b>"slug"</b>: <i>"abiotic-factor"</i>,</span><span class="l">  <b>"steam_app_id_windows"</b>: <i>2857200</i>,</span><span class="l">  <b>"platforms"</b>: [</span><span class="l">    &#123; <b>"kind"</b>: <i>"windows-native"</i>, <b>"image"</b>: <i>"ghcr.io/&lt;owner&gt;/kraken-steam-win:ltsc2022"</i> &#125;,</span><span class="l">    &#123; <b>"kind"</b>: <i>"linux-wine"</i>,     <b>"image"</b>: <i>"ghcr.io/&lt;owner&gt;/kraken-steam-wine:latest"</i> &#125;</span><span class="l">  ],</span><span class="l">  <b>"install"</b>: &#123;</span><span class="l">    <b>"script"</b>: <i>"steamcmd.exe +force_install_dir C:\\data +login anonymous +app_update &#123;&#123;APP_ID&#125;&#125; validate +quit"</i>,</span><span class="l">    <b>"bepinex"</b>: <i>true</i>,            <em>// unity games with mod support</em></span><span class="l">    <b>"requires_steam_login"</b>: <i>false</i> <em>// true would need a real account + 2FA</em></span><span class="l">  &#125;,</span><span class="l">  <b>"startup"</b>: &#123;</span><span class="l">    <b>"command"</b>: <i>"wine-headless /data/AbioticFactor/.../AbioticFactorServer-Win64-Shipping.exe -log -PORT=&#123;&#123;PORT_GAME&#125;&#125; -QueryPort=&#123;&#123;PORT_QUERY&#125;&#125; -MaxServerPlayers=&#123;&#123;MAX_PLAYERS&#125;&#125;"</i>,</span><span class="l">    <b>"ready_regex"</b>: <i>"Server is ready"</i>,</span><span class="l">    <b>"stop"</b>: &#123; <b>"type"</b>: <i>"signal"</i>, <b>"value"</b>: <i>"SIGINT"</i> &#125;</span><span class="l">  &#125;,</span><span class="l">  <b>"variables"</b>: [ <em>4 entries — see the form view</em> ],</span><span class="l">  <b>"ports"</b>: [ <em>2 entries — see the form view</em> ],</span><span class="l">  <b>"resources"</b>: &#123; <b>"min_memory_mb"</b>: <i>4096</i>, <b>"recommended_mb"</b>: <i>8192</i> &#125;,</span><span class="l">  <b>"settings_groups"</b>: [],</span><span class="l">  <b>"config_files"</b>: [ <em>bindings and templates — only editable here</em> ]</span><span class="l">&#125;</span></span><span class="doc y"><span class="l"><b>name</b>: <i>Abiotic Factor</i></span><span class="l"><b>slug</b>: <i>abiotic-factor</i></span><span class="l"><b>steam_app_id_windows</b>: <i>2857200</i></span><span class="l"><b>platforms</b>:</span><span class="l">  - <b>kind</b>: <i>windows-native</i></span><span class="l">    <b>image</b>: <i>ghcr.io/&lt;owner&gt;/kraken-steam-win:ltsc2022</i></span><span class="l">  - <b>kind</b>: <i>linux-wine</i></span><span class="l">    <b>image</b>: <i>ghcr.io/&lt;owner&gt;/kraken-steam-wine:latest</i></span><span class="l"><b>install</b>:</span><span class="l">  <b>script</b>: <i>steamcmd.exe +force_install_dir C:\data +login anonymous +app_update &#123;&#123;APP_ID&#125;&#125; validate +quit</i></span><span class="l">  <b>bepinex</b>: <i>true</i>            <em># unity games with mod support</em></span><span class="l">  <b>requires_steam_login</b>: <i>false</i> <em># true would need a real account + 2FA</em></span><span class="l"><b>startup</b>:</span><span class="l">  <b>command</b>: <i>wine-headless /data/AbioticFactor/.../AbioticFactorServer-Win64-Shipping.exe -log -PORT=&#123;&#123;PORT_GAME&#125;&#125; -QueryPort=&#123;&#123;PORT_QUERY&#125;&#125; -MaxServerPlayers=&#123;&#123;MAX_PLAYERS&#125;&#125;</i></span><span class="l">  <b>ready_regex</b>: <i>Server is ready</i></span><span class="l">  <b>stop</b>:</span><span class="l">    <b>type</b>: <i>signal</i></span><span class="l">    <b>value</b>: <i>SIGINT</i></span><span class="l"><b>variables</b>:      <em># 4 entries — see the form view</em></span><span class="l"><b>ports</b>:          <em># 2 entries — see the form view</em></span><span class="l"><b>resources</b>:</span><span class="l">  <b>min_memory_mb</b>: <i>4096</i></span><span class="l">  <b>recommended_mb</b>: <i>8192</i></span><span class="l"><b>settings_groups</b>: <i>[]</i></span><span class="l"><b>config_files</b>:   <em># bindings and templates — only editable here</em></span></span></pre>
      <p class="cfg-help">the document is the source of truth; the form above is a view onto it. config file bindings and templates only appear here.</p>
    </div>
  </div>
</div>
