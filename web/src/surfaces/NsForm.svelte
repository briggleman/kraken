<script lang="ts">
  import { ui, closeSheet, openSheet } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";

  // the spec link names the game this form is building, so it tracks the game select rather
  // than sitting on whichever option happened to be selected when the markup was written.
  // options carry no value attribute, so the option text is the value (mock: nsGame.value
  // || options[selectedIndex].text). seeded to the mock's `selected` option.
  let nsGame = $state("palworld");
</script>

<div
  class="sheet"
  class:open={!!ui.open.nsForm}
  id="nsForm"
  role="dialog"
  aria-modal="true"
  aria-labelledby="nsFormTitle"
  style="--ox: {ui.open.nsForm?.ox ?? '50%'}; --oy: {ui.open.nsForm?.oy ?? '50%'}"
  use:sheetFocus
>
  <div class="depth-head">
    <button class="surface-btn" onclick={() => closeSheet("nsForm")}>
      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6 10 V 2 M 2 6 L 6 2 L 10 6"/></svg>
      surface
    </button>
    <h2 class="depth-title" id="nsFormTitle">new server on <b class="node-name-inline">behemoth</b></h2>
    <div class="prefs-note"><span class="synthetic">values are sample — surface not wired</span></div>
  </div>
  <div class="sheet-body ns-body">
    <section class="prefs-group" aria-label="New server settings">
      <div class="cfg-head">
        <h3 class="pane-label">every setting, one screen</h3>
        <span class="cfg-badge env">defaults from the game template</span>
        <span class="cfg-badge ok">ports free</span>
      </div>
      <div class="ns-pad ns-grid">
        <div class="ns-legend"><h4>game</h4><i></i><small>decides install method and port defaults</small></div>
        <div class="cfg-row">
          <span>game — from the specs you have</span>
          <select class="cfg-in" id="nsGame" aria-label="Game" bind:value={nsGame}><option>abiotic factor</option><option>enshrouded</option><option>factorio</option><option>palworld</option><option>runescape dragonwilds</option><option>v rising</option><option>valheim</option><option>windrose</option></select>
        </div>
        <div class="cfg-row">
          <span>operating system</span>
          <select class="cfg-in has-badge" aria-label="Operating system"><button><selectedcontent></selectedcontent><span class="cfg-badge env">min 8192 mb</span></button><option value="linux-native" selected>linux</option><option value="windows-native">windows</option><option value="linux-wine">wine</option></select>
        </div>
        <div class="cfg-row">
          <span>spec</span>
          <button class="cfg-btn ghost spec-link" id="specLink" onclick={(e) => openSheet("specs", e.clientX, e.clientY, e.currentTarget)}><span class="spec-link-name" id="specLinkName">{nsGame}</span>&nbsp;spec <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 3.5 8.5 L 8.5 3.5 M 5 3.5 H 8.5 V 7"/></svg></button>
        </div>

        <div class="ns-legend"><h4>identity</h4><i></i><small>what backups and moves carry</small></div>
        <div class="cfg-row">
          <span>server name</span>
          <input type="text" class="cfg-in" value="palworld-02" aria-label="Server name" />
        </div>
        <div class="cfg-row">
          <span>world name</span>
          <input type="text" class="cfg-in" value="feybreak-02" aria-label="World name" />
        </div>
        <div class="cfg-row">
          <span>data directory</span>
          <input type="text" class="cfg-in ns-path" value="/srv/kraken/palworld-02" aria-label="Data directory" />
        </div>

        <div class="ns-legend"><h4>network</h4><i></i><small>next free ports above the running servers</small></div>
        <div class="cfg-row">
          <span>game port</span>
          <input type="text" class="cfg-in" value="8212" aria-label="Game port" />
        </div>
        <div class="cfg-row">
          <span>query port</span>
          <input type="text" class="cfg-in" value="27016" aria-label="Query port" />
        </div>
        <div class="cfg-row">
          <span>rcon port</span>
          <input type="text" class="cfg-in" value="25576" aria-label="Rcon port" />
        </div>

        <div class="ns-legend"><h4>limits</h4><i></i><small>behemoth has 42.6G memory and 812G disk unclaimed</small></div>
        <div class="cfg-row">
          <span>memory cap</span>
          <input type="text" class="cfg-in" value="8G" aria-label="Memory cap" />
        </div>
        <div class="cfg-row">
          <span>cpu share</span>
          <select class="cfg-in"><option>4 of 16 cores</option><option>6 of 16 cores</option><option>8 of 16 cores</option><option>no limit</option></select>
        </div>
        <div class="cfg-row">
          <span>disk quota</span>
          <input type="text" class="cfg-in" value="40G" aria-label="Disk quota" />
        </div>

        <div class="ns-legend"><h4>operations</h4><i></i><small>changeable later in the server's own settings</small></div>
        <label class="tgl"><input type="checkbox" checked /><i></i>start once the install finishes</label>
        <label class="tgl"><input type="checkbox" checked /><i></i>restart if it exits unexpectedly</label>
        <label class="tgl"><input type="checkbox" checked /><i></i>nightly backup at 04:00</label>
        <label class="tgl"><input type="checkbox" /><i></i>update to latest on every start</label>
      </div>
      <div class="ns-alloc">
        <div class="ns-cost"><span>memory after</span><b>29.4<em>/64G</em></b></div>
        <div class="ns-cost"><span>cores claimed</span><b>10<em>/16</em></b></div>
        <div class="ns-cost"><span>disk after</span><b>40<em>/812G free</em></b></div>
        <div class="ns-cost"><span>download</span><b>~9<em>G</em></b></div>
        <div class="ns-acts">
          <button class="cfg-btn ghost" onclick={() => closeSheet("nsForm")}>cancel</button>
          <button class="cfg-btn solid">create server</button>
        </div>
      </div>
    </section>
  </div>
</div>
