<script lang="ts">
  import { istyle } from "@/lib/istyle";
  import { ui, closeSheet, openConfirm, sheetZ } from "@/lib/state.svelte";
  import { refreshFleet } from "@/lib/fleet.svelte";
  import { api } from "@/api/client";
  import { sheetFocus } from "@/lib/sheetFocus";
  import type { Spec } from "@/api/types";
  import YAML from "yaml";

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
  let view = $state<"form" | "code">("form");
  // The code view's serialisation (the mock's json|yaml switch). The document
  // is one buffer; the switch CONVERTS it rather than swapping views, so
  // whatever was typed survives the flip — provided it parses.
  let fmt = $state<"json" | "yaml">("json");

  // lineWidth 0 keeps long values (a startup command) on one line instead of
  // YAML's folded wrapping, which reads as edits nobody made.
  const serialize = (doc: unknown) =>
    fmt === "yaml" ? YAML.stringify(doc, { lineWidth: 0 }) : JSON.stringify(doc, null, 2);

  // Tolerant on purpose: the buffer may hold either serialisation mid-edit
  // (JSON is valid YAML, but JSON.parse gives the better error for json mode).
  function parseDoc(text: string): SpecDoc {
    try {
      return JSON.parse(text) as SpecDoc;
    } catch {
      return YAML.parse(text) as SpecDoc;
    }
  }

  // load the spec the editor is about whenever the sheet opens (or is retargeted)
  $effect(() => {
    if (!ui.open.specEdit) return;
    const id = ui.specEditId;
    spec = null;
    codeText = "";
    note = null;
    view = "form";
    fmt = "json";
    if (!id) return;
    api
      .getSpec(id)
      .then((sp) => {
        if (ui.specEditId !== id || !ui.open.specEdit) return; // retargeted meanwhile
        spec = sp as SpecDoc;
        codeText = serialize(sp);
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

  // The form and the code view are two editors over the same document, and
  // only one is live at a time: switching away from the one you were typing
  // in writes it into the other. Code → form must parse first — a document
  // the form cannot render (yaml, or json mid-edit) keeps you in the code
  // view instead of silently discarding what you typed.
  function setView(v: "form" | "code") {
    if (v === view) return;
    if (v === "code") {
      if (spec) codeText = serialize(spec);
      view = "code";
      return;
    }
    try {
      spec = parseDoc(codeText);
      note = null;
      view = "form";
    } catch {
      note = "the code view does not parse as json or yaml — fix it here before switching to the form";
    }
  }

  // Switching serialisation converts the live buffer — a buffer that doesn't
  // parse stays in the format it was typed in rather than being discarded.
  function setFmt(f: "json" | "yaml") {
    if (f === fmt) return;
    try {
      const doc = parseDoc(codeText);
      fmt = f;
      codeText = serialize(doc);
      note = null;
    } catch {
      note = "the code view does not parse — fix it before switching formats";
    }
  }

  // Save writes whichever editor is live: the form serializes the document it
  // has been editing; the code view posts its text verbatim (the panel accepts
  // JSON or YAML). Both go through the raw endpoint so the two paths differ
  // only in who did the serializing.
  async function save() {
    if (!spec || saving) return;
    saving = true;
    note = null;
    try {
      const body = view === "code" ? codeText : JSON.stringify(spec);
      const updated = await api.updateSpecRaw(spec.id, body);
      await refreshFleet();
      spec = updated as SpecDoc;
      codeText = serialize(updated);
      closeSheet("specEdit");
    } catch (e) {
      note = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

  // -- form bindings that cannot be a plain bind: optional keys, numbers, and
  //    nested objects the document may not carry yet

  function toInt(v: string): number | undefined {
    if (v.trim() === "") return undefined;
    const n = Math.trunc(Number(v));
    return Number.isFinite(n) ? n : undefined;
  }

  const getDescription = () => spec?.description ?? "";
  const setDescription = (v: string) => { if (spec) spec.description = v || undefined; };
  const getBanner = () => spec?.banner_url ?? "";
  const setBanner = (v: string) => { if (spec) spec.banner_url = v.trim() || undefined; };
  const getIcon = () => spec?.icon_url ?? "";
  const setIcon = (v: string) => { if (spec) spec.icon_url = v.trim() || undefined; };

  function getSteamAppID(os: "linux" | "windows"): string {
    const n = spec?.steam_app_ids?.[os];
    return n === undefined ? "" : String(n);
  }
  function setSteamAppID(os: "linux" | "windows", v: string) {
    if (!spec) return;
    const ids = { ...(spec.steam_app_ids ?? {}) };
    const n = toInt(v);
    if (n === undefined || n <= 0) delete ids[os];
    else ids[os] = n;
    spec.steam_app_ids = Object.keys(ids).length ? ids : undefined;
  }

  function setInstallFlag(k: "bepinex_compatible" | "requires_steam_login", on: boolean) {
    if (spec) spec.install = { ...(spec.install ?? {}), [k]: on };
  }

  const getMinMem = () => String(spec?.resources.min_memory_mb ?? "");
  const setMinMem = (v: string) => { if (spec) spec.resources.min_memory_mb = toInt(v) ?? 0; };
  const getRecMem = () => {
    const n = spec?.resources.recommended_memory_mb;
    return n === undefined ? "" : String(n);
  };
  const setRecMem = (v: string) => { if (spec) spec.resources.recommended_memory_mb = toInt(v); };

  const getHotReload = () => !!spec?.settings?.hot_reload;
  const setHotReload = (on: boolean) => { if (spec) spec.settings = { ...(spec.settings ?? {}), hot_reload: on }; };

  function addPlatform() {
    if (spec) spec.platforms = [...spec.platforms, { kind: "linux-native", image: "" }];
  }
  function dropPlatform(i: number) {
    spec?.platforms.splice(i, 1);
  }
  function addVariable() {
    if (spec) spec.variables = [...(spec.variables ?? []), { key: "", label: "", default: "", user_editable: true }];
  }
  function dropVariable(i: number) {
    spec?.variables?.splice(i, 1);
  }
  function addPort() {
    if (spec) spec.ports = [...(spec.ports ?? []), { name: "", protocol: "udp", default: 0, required: true }];
  }
  function dropPort(i: number) {
    spec?.ports?.splice(i, 1);
  }

  function del(e: MouseEvent & { currentTarget: HTMLElement }) {
    if (!spec) return;
    openConfirm(spec.name, e.currentTarget, { noun: "spec", body: DELETE_BODY });
  }

  // The tabs are radios so the house CSS (:has on .sv-form/.sv-code) keeps
  // working — but the browser flips a radio before setView can refuse the
  // switch, so after every toggle the pair is re-asserted from state.
  let formTab = $state<HTMLInputElement | null>(null);
  let codeTab = $state<HTMLInputElement | null>(null);

  function onTab(v: "form" | "code") {
    setView(v);
    if (formTab) formTab.checked = view === "form";
    if (codeTab) codeTab.checked = view === "code";
  }

  $effect(() => {
    const v = view;
    if (formTab) formTab.checked = v === "form";
    if (codeTab) codeTab.checked = v === "code";
  });

  // The json|yaml switch is the same radios-the-browser-flips-first mechanism
  // as the view tabs: setFmt can refuse (unparseable buffer), so the pair is
  // re-asserted from state after every toggle.
  let jsonFmt = $state<HTMLInputElement | null>(null);
  let yamlFmt = $state<HTMLInputElement | null>(null);

  function onFmt(f: "json" | "yaml") {
    setFmt(f);
    if (jsonFmt) jsonFmt.checked = fmt === "json";
    if (yamlFmt) yamlFmt.checked = fmt === "yaml";
  }

  $effect(() => {
    const f = fmt;
    if (jsonFmt) jsonFmt.checked = f === "json";
    if (yamlFmt) yamlFmt.checked = f === "yaml";
  });
</script>

<div
  class="sheet"
  class:open={!!ui.open.specEdit}
  id="specEdit"
  role="dialog"
  aria-modal="true"
  aria-labelledby="specEditTitle"
  use:istyle={`--ox: ${ui.open.specEdit?.ox ?? '50%'}; --oy: ${ui.open.specEdit?.oy ?? '50%'}; z-index: ${sheetZ("specEdit")}`}
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
        <label class="os-tab sv-form"><input class="os-r-in" type="radio" name="sv" bind:this={formTab} checked onchange={() => onTab("form")} />form</label>
        <label class="os-tab sv-code"><input class="os-r-in" type="radio" name="sv" bind:this={codeTab} onchange={() => onTab("code")} />code</label>
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
            <label class="cfg-row"><span>name</span><input class="cfg-in" type="text" bind:value={spec.name} /></label>
            <label class="cfg-row"><span>slug</span><input class="cfg-in" type="text" bind:value={spec.slug} /></label>
            <label class="cfg-row"><span>steam app id — linux</span><input class="cfg-in" type="text" bind:value={() => getSteamAppID("linux"), (v) => setSteamAppID("linux", v)} placeholder="—" /></label>
            <label class="cfg-row"><span>steam app id — windows</span><input class="cfg-in" type="text" bind:value={() => getSteamAppID("windows"), (v) => setSteamAppID("windows", v)} placeholder="—" /></label>
            <label class="cfg-row"><span>banner url</span><input class="cfg-in" type="text" bind:value={getBanner, setBanner} placeholder="https://… (empty — the list shows &quot;no banner&quot;)" /></label>
            <label class="cfg-row"><span>icon url</span><input class="cfg-in" type="text" bind:value={getIcon, setIcon} placeholder="https://…" /></label>
            <label class="cfg-row spec-wide"><span>description</span><input class="cfg-in" type="text" bind:value={getDescription, setDescription} /></label>
          </div>
        </section>

        <section class="prefs-group" aria-label="Platforms">
          <div class="cfg-head"><h3 class="pane-label">platforms</h3><span class="cfg-badge env">{plats.length === 0 ? "none" : `${plats.length} defined`}</span><button class="cfg-btn ghost spec-add" onclick={addPlatform}>add platform</button></div>
          <div class="ns-pad">
            <div class="spec-rows">
              {#each plats as p, i (i)}
                <div class="spec-sub plats">
                  <div class="cfg-row"><span>kind</span><select class="cfg-in" bind:value={p.kind}><option value="linux-native">linux-native</option><option value="linux-wine">linux-wine</option><option value="windows-native">windows-native</option></select></div>
                  <label class="cfg-row"><span>image</span><input class="cfg-in" type="text" bind:value={p.image} /></label>
                  <button class="mini-act del spec-drop" onclick={() => dropPlatform(i)}>drop</button>
                </div>
              {/each}
            </div>
          </div>
        </section>

        <section class="prefs-group" aria-label="Install">
          <div class="cfg-head"><h3 class="pane-label">install</h3></div>
          <div class="cfg">
            <label class="tgl"><input type="checkbox" checked={!!spec.install?.bepinex_compatible} onchange={(e) => setInstallFlag("bepinex_compatible", e.currentTarget.checked)} /><i></i>bepinex compatible — unity games with mod support</label>
            <label class="tgl"><input type="checkbox" checked={!!spec.install?.requires_steam_login} onchange={(e) => setInstallFlag("requires_steam_login", e.currentTarget.checked)} /><i></i>requires steam login — a real account and 2fa, not anonymous</label>
            <p class="cfg-help">anonymous install is the default. turning on steam login means the node needs credentials of its own; kraken never holds them in the browser. the install and startup commands live in the document — edit them in the code view.</p>
          </div>
        </section>

        <section class="prefs-group" aria-label="Variables">
          <div class="cfg-head"><h3 class="pane-label">variables</h3><span class="cfg-badge env">{vars.length === 0 ? "none" : `${vars.length} defined`}</span><button class="cfg-btn ghost spec-add" onclick={addVariable}>add variable</button></div>
          <div class="ns-pad">
            <div class="spec-rows">
              {#each vars as v, i (i)}
                <div class="spec-sub vars"><label class="cfg-row"><span>key</span><input class="cfg-in" type="text" bind:value={v.key} /></label><label class="cfg-row"><span>label</span><input class="cfg-in" type="text" bind:value={v.label} /></label><label class="cfg-row"><span>default</span><input class="cfg-in" type="text" bind:value={v.default} /></label><label class="cfg-row"><span>rules</span><input class="cfg-in" type="text" bind:value={v.rules} /></label><label class="tgl"><input type="checkbox" bind:checked={v.user_editable} /><i></i>editable</label><button class="mini-act del spec-drop" onclick={() => dropVariable(i)}>drop</button></div>
              {/each}
            </div>
          </div>
        </section>

        <section class="prefs-group" aria-label="Ports">
          <div class="cfg-head"><h3 class="pane-label">ports</h3><span class="cfg-badge env">{ports.length === 0 ? "none" : `${ports.length} defined`}</span><button class="cfg-btn ghost spec-add" onclick={addPort}>add port</button></div>
          <div class="ns-pad">
            <div class="spec-rows">
              {#each ports as p, i (i)}
                <div class="spec-sub ports"><label class="cfg-row"><span>name</span><input class="cfg-in" type="text" bind:value={p.name} /></label><div class="cfg-row"><span>protocol</span><select class="cfg-in" bind:value={p.protocol}><option value="udp">udp</option><option value="tcp">tcp</option></select></div><label class="cfg-row"><span>default</span><input class="cfg-in" type="text" bind:value={() => String(p.default), (v) => (p.default = toInt(v) ?? 0)} /></label><label class="tgl"><input type="checkbox" bind:checked={p.required} /><i></i>required</label><button class="mini-act del spec-drop" onclick={() => dropPort(i)}>drop</button></div>
              {/each}
            </div>
          </div>
        </section>

        <section class="prefs-group" aria-label="Resources">
          <div class="cfg-head"><h3 class="pane-label">resources</h3></div>
          <div class="cfg">
            <label class="cfg-row"><span>min memory (mb)</span><input class="cfg-in" type="text" bind:value={getMinMem, setMinMem} /></label>
            <label class="cfg-row"><span>recommended (mb)</span><input class="cfg-in" type="text" bind:value={getRecMem, setRecMem} placeholder="—" /></label>
            <p class="cfg-help">a new server is reserved the recommendation when one is set, and the minimum otherwise — so the recommendation is the figure that actually gets allocated, and the minimum is the fallback for specs that state nothing.</p>
          </div>
        </section>

        <section class="prefs-group" aria-label="Settings groups">
          <div class="cfg-head"><h3 class="pane-label">settings groups</h3><span class="cfg-badge env">{groupCount === 0 ? "none" : `${groupCount} defined`}</span><button class="cfg-btn ghost spec-add">add group</button></div>
          <div class="cfg">
            <label class="tgl"><input type="checkbox" checked={!!spec.settings?.hot_reload} onchange={(e) => setHotReload(e.currentTarget.checked)} /><i></i>hot reload — the game re-reads its config files live, so saved settings apply without a restart</label>
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
      <span class="cd-sw">
        <label class="cd-opt"><input class="cd-r" type="radio" name="specfmt" bind:this={jsonFmt} checked onchange={() => onFmt("json")} />json</label>
        <label class="cd-opt"><input class="cd-r r-y" type="radio" name="specfmt" bind:this={yamlFmt} onchange={() => onFmt("yaml")} />yaml</label>
      </span>
      <textarea class="spec-code" spellcheck="false" aria-label="Spec document" bind:value={codeText}></textarea>
      <p class="cfg-help">{note ?? "the document is the source of truth; the form above is a view onto it. config file bindings and templates only appear here. saving posts this document verbatim — json and yaml both save."}</p>
    </div>
  </div>
</div>
