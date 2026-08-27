<script lang="ts">
  import { ui, closeSheet, openConfirm } from "@/lib/state.svelte";
  import { refreshFleet } from "@/lib/fleet.svelte";
  import { api } from "@/api/client";
  import { sheetFocus } from "@/lib/sheetFocus";
  import type { Spec } from "@/api/types";

  // the mock's data-confirm-body, passed through to the typed-confirm dialog
  const DELETE_BODY =
    "deleting this spec leaves servers built from it running, but nothing can be reinstalled or redeployed from it. it cannot be undone.";

  // The lean Spec type omits the settings block, but the document itself
  // carries it: the code view round-trips it verbatim, and the form shows
  // its count and the hot-reload flag.
  type SpecDoc = Spec & { settings?: { hot_reload?: boolean; groups?: unknown[] } };

  let spec = $state<SpecDoc | null>(null);
  let codeText = $state("");
  let note = $state<string | null>(null);
  let saving = $state(false);

  // load the spec the editor is about whenever the sheet opens (or is retargeted)
  $effect(() => {
    if (!ui.open.specEdit) return;
    const id = ui.specEditId;
    spec = null;
    codeText = "";
    note = null;
    if (!id) return;
    api
      .getSpec(id)
      .then((sp) => {
        if (ui.specEditId !== id || !ui.open.specEdit) return; // retargeted meanwhile
        spec = sp as SpecDoc;
        codeText = JSON.stringify(sp, null, 2);
      })
      .catch((e) => {
        if (ui.specEditId !== id) return;
        note = e instanceof Error ? e.message : String(e);
      });
  });

  const plats = $derived(spec?.platforms ?? []);
  const vars = $derived(spec?.variables ?? []);
  const ports = $derived(spec?.ports ?? []);
  const groupCount = $derived(spec?.settings?.groups?.length ?? 0);

  // the document is the source of truth: save posts the code view's text
  // verbatim (the panel accepts raw JSON) and the fleet list catches up
  async function save() {
    if (!spec || saving) return;
    saving = true;
    note = null;
    try {
      await api.updateSpecRaw(spec.id, codeText);
      await refreshFleet();
      closeSheet("specEdit");
    } catch (e) {
      note = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

  function del(e: MouseEvent & { currentTarget: HTMLElement }) {
    if (!spec) return;
    openConfirm(spec.name, e.currentTarget, { noun: "spec", body: DELETE_BODY });
  }
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
    <h2 class="depth-title" id="specEditTitle">edit spec <b class="node-name-inline">{spec?.name.toLowerCase() ?? "…"}</b></h2>
  </div>
  <div class="sheet-body spec-edit-body">
    <div class="audit-bar">
      <div class="os-tablist" role="tablist" aria-label="Editor view">
        <label class="os-tab sv-form"><input class="os-r-in" type="radio" name="sv" checked />form</label>
        <label class="os-tab sv-code"><input class="os-r-in" type="radio" name="sv" />code</label>
      </div>
      {#if spec}
        <span class="audit-count">spec {spec.id.slice(0, 8)} · v{spec.version} · {plats.length} platform{plats.length === 1 ? "" : "s"} · {vars.length} variable{vars.length === 1 ? "" : "s"} · {ports.length} port{ports.length === 1 ? "" : "s"}</span>
      {:else}
        <span class="audit-count">{note ?? "loading…"}</span>
      {/if}
      <button class="cfg-btn ghost" onclick={() => closeSheet("specEdit")}>cancel</button>
      <button class="cfg-btn solid" disabled={!spec || saving} onclick={() => void save()}>{saving ? "saving…" : "save"}</button>
    </div>

    <div class="spec-form">
      {#if spec}
        <section class="prefs-group" aria-label="Identity">
          <div class="cfg-head"><h3 class="pane-label">identity</h3></div>
          <div class="cfg">
            <label class="cfg-row"><span>name</span><input class="cfg-in" type="text" value={spec.name} /></label>
            <label class="cfg-row"><span>slug</span><input class="cfg-in" type="text" value={spec.slug} /></label>
            <label class="cfg-row"><span>steam app id — linux</span><input class="cfg-in" type="text" value={spec.steam_app_ids?.["linux"] ?? ""} placeholder="—" /></label>
            <label class="cfg-row"><span>steam app id — windows</span><input class="cfg-in" type="text" value={spec.steam_app_ids?.["windows"] ?? ""} placeholder="—" /></label>
            <label class="cfg-row"><span>banner url</span><input class="cfg-in" type="text" value={spec.banner_url ?? ""} placeholder="https://… (empty — the list shows &quot;no banner&quot;)" /></label>
            <label class="cfg-row"><span>icon url</span><input class="cfg-in" type="text" value={spec.icon_url ?? ""} placeholder="https://…" /></label>
            <label class="cfg-row spec-wide"><span>description</span><input class="cfg-in" type="text" value={spec.description ?? ""} /></label>
          </div>
        </section>

        <section class="prefs-group" aria-label="Platforms">
          <div class="cfg-head"><h3 class="pane-label">platforms</h3><span class="cfg-badge env">{plats.length === 0 ? "none" : `${plats.length} defined`}</span><button class="cfg-btn ghost spec-add">add platform</button></div>
          <div class="ns-pad">
            <div class="spec-rows">
              {#each plats as p, i (i)}
                <div class="spec-sub plats">
                  <div class="cfg-row"><span>kind</span><select class="cfg-in" value={p.kind}><option value="linux-native">linux-native</option><option value="linux-wine">linux-wine</option><option value="windows-native">windows-native</option></select></div>
                  <label class="cfg-row"><span>image</span><input class="cfg-in" type="text" value={p.image} /></label>
                  <button class="mini-act del spec-drop">drop</button>
                </div>
              {/each}
            </div>
          </div>
        </section>

        <section class="prefs-group" aria-label="Install">
          <div class="cfg-head"><h3 class="pane-label">install</h3></div>
          <div class="cfg">
            <label class="tgl"><input type="checkbox" checked={!!spec.install?.bepinex_compatible} /><i></i>bepinex compatible — unity games with mod support</label>
            <label class="tgl"><input type="checkbox" checked={!!spec.install?.requires_steam_login} /><i></i>requires steam login — a real account and 2fa, not anonymous</label>
            <p class="cfg-help">anonymous install is the default. turning on steam login means the node needs credentials of its own; kraken never holds them in the browser. the install and startup commands live in the document — edit them in the code view.</p>
          </div>
        </section>

        <section class="prefs-group" aria-label="Variables">
          <div class="cfg-head"><h3 class="pane-label">variables</h3><span class="cfg-badge env">{vars.length === 0 ? "none" : `${vars.length} defined`}</span><button class="cfg-btn ghost spec-add">add variable</button></div>
          <div class="ns-pad">
            <div class="spec-rows">
              {#each vars as v (v.key)}
                <div class="spec-sub vars"><label class="cfg-row"><span>key</span><input class="cfg-in" type="text" value={v.key} /></label><label class="cfg-row"><span>label</span><input class="cfg-in" type="text" value={v.label ?? ""} /></label><label class="cfg-row"><span>default</span><input class="cfg-in" type="text" value={v.default} /></label><label class="cfg-row"><span>rules</span><input class="cfg-in" type="text" value={v.rules ?? ""} /></label><label class="tgl"><input type="checkbox" checked={v.user_editable} /><i></i>editable</label><button class="mini-act del spec-drop">drop</button></div>
              {/each}
            </div>
          </div>
        </section>

        <section class="prefs-group" aria-label="Ports">
          <div class="cfg-head"><h3 class="pane-label">ports</h3><span class="cfg-badge env">{ports.length === 0 ? "none" : `${ports.length} defined`}</span><button class="cfg-btn ghost spec-add">add port</button></div>
          <div class="ns-pad">
            <div class="spec-rows">
              {#each ports as p (p.name)}
                <div class="spec-sub ports"><label class="cfg-row"><span>name</span><input class="cfg-in" type="text" value={p.name} /></label><div class="cfg-row"><span>protocol</span><select class="cfg-in" value={p.protocol}><option value="udp">udp</option><option value="tcp">tcp</option></select></div><label class="cfg-row"><span>default</span><input class="cfg-in" type="text" value={p.default} /></label><label class="tgl"><input type="checkbox" checked={!!p.required} /><i></i>required</label><button class="mini-act del spec-drop">drop</button></div>
              {/each}
            </div>
          </div>
        </section>

        <section class="prefs-group" aria-label="Resources">
          <div class="cfg-head"><h3 class="pane-label">resources</h3></div>
          <div class="cfg">
            <label class="cfg-row"><span>min memory (mb)</span><input class="cfg-in" type="text" value={spec.resources.min_memory_mb} /></label>
            <label class="cfg-row"><span>recommended (mb)</span><input class="cfg-in" type="text" value={spec.resources.recommended_memory_mb ?? ""} placeholder="—" /></label>
            <p class="cfg-help">the minimum is enforced when a server is created; the recommendation is what the new server sheet fills in.</p>
          </div>
        </section>

        <section class="prefs-group" aria-label="Settings groups">
          <div class="cfg-head"><h3 class="pane-label">settings groups</h3><span class="cfg-badge env">{groupCount === 0 ? "none" : `${groupCount} defined`}</span><button class="cfg-btn ghost spec-add">add group</button></div>
          <div class="cfg">
            <label class="tgl"><input type="checkbox" checked={!!spec.settings?.hot_reload} /><i></i>hot reload — the game re-reads its config files live, so saved settings apply without a restart</label>
            <p class="cfg-help">{groupCount === 0 ? "no settings groups yet. these" : `${groupCount} group${groupCount === 1 ? "" : "s"} — they`} render game options into config files. config file bindings and templates are kept but can only be edited in the code view.</p>
          </div>
        </section>

        <div class="cfg-actions">
          <span class="cfg-note">{note ?? "deleting a spec leaves servers built from it running, but they can no longer be reinstalled"}</span>
          <button class="cfg-btn danger" data-confirm-open="spec" data-confirm-name={spec.name} data-confirm-body={DELETE_BODY} onclick={del}>delete spec</button>
          <button class="cfg-btn solid" disabled={saving} onclick={() => void save()}>{saving ? "saving…" : "save"}</button>
        </div>
      {/if}
    </div>

    <div class="spec-code-wrap">
      <textarea class="spec-code" spellcheck="false" aria-label="Spec document (json)" bind:value={codeText}></textarea>
      <p class="cfg-help">{note ?? "the document is the source of truth; the form above is a view onto it. config file bindings and templates only appear here. saving posts this json verbatim."}</p>
    </div>
  </div>
</div>
