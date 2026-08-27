<script lang="ts">
  import { istyle } from "@/lib/istyle";
  import { ui, closeSheet, openSheet } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";
  import { fleet, refreshFleet } from "@/lib/fleet.svelte";
  import { api } from "@/api/client";
  import { fmtGb } from "@/lib/fmt";
  import type { Spec } from "@/api/types";

  // The form is real: games come from the specs you have, settings are the
  // spec's user-editable variables, ports are allocated by the panel from the
  // node's pool, and the cost strip does the actual memory arithmetic.

  const open = $derived(!!ui.open.nsForm);

  let specId = $state("");
  let name = $state("");
  let vars = $state<Record<string, string>>({});
  let steamGuard = $state("");
  let bepinex = $state(false);
  let startAfter = $state(true);
  let nightlyBackup = $state(true);
  let busy = $state(false);
  let err = $state<string | null>(null);

  const spec = $derived<Spec | undefined>(fleet.specs.find((s) => s.id === specId));
  const node = $derived(
    fleet.nodes.find((n) => n.id === ui.nsFormNodeId) ?? fleet.nodes[0],
  );

  // (re)seed when the sheet opens or the game changes
  $effect(() => {
    if (!open) return;
    if (!specId && fleet.specs.length) specId = fleet.specs[0].id;
  });
  $effect(() => {
    const s = spec;
    if (!s) return;
    name = s.slug + "-" + String(fleet.servers.filter((x) => x.spec_id === s.id).length + 1).padStart(2, "0");
    const v: Record<string, string> = {};
    for (const sv of s.variables ?? []) if (sv.user_editable) v[sv.key] = sv.default;
    vars = v;
    steamGuard = "";
    bepinex = false;
    err = null;
  });

  const platformWords = $derived(
    (spec?.platforms ?? []).map((p) =>
      p.kind === "linux-native" ? "linux" : p.kind === "windows-native" ? "windows" : "wine",
    ),
  );
  // What the panel will actually reserve: the recommended figure when the spec
  // states one, else the minimum (scheduler.Place → Resources.AllocMemoryMB).
  // The strip reports that number, not the floor — a cost readout that quotes
  // a smaller figure than the one about to be taken is misinformation.
  const allocMb = $derived(
    spec?.resources.recommended_memory_mb || spec?.resources.min_memory_mb || 0,
  );
  const memAfter = $derived(node ? node.allocated_memory_mb + allocMb : allocMb);
  // Recommended figures are larger than the old minimums, so over-commit is a
  // live case now: the panel will refuse the placement, and the strip should
  // say so before the button is pressed rather than printing 34/32G mutely.
  const overCapacity = $derived(!!node && memAfter > node.total_memory_mb);

  async function create() {
    if (!spec || busy) return;
    busy = true;
    err = null;
    try {
      const created = await api.createServer({
        spec_id: spec.id,
        name: name.trim() || spec.slug,
        variables: Object.keys(vars).length ? vars : undefined,
        node_id: node?.id,
        steam_guard_code: steamGuard.trim() || undefined,
        install_bepinex: bepinex || undefined,
      });
      if (nightlyBackup) {
        await api
          .createSchedule(created.id, {
            name: "nightly backup",
            action: "backup",
            cron: "0 4 * * *",
            enabled: true,
          })
          .catch(() => {});
      }
      closeSheet("nsForm");
      await refreshFleet();
      if (startAfter) {
        // start once the install finishes: watch until the container exists
        const poll = setInterval(async () => {
          try {
            const s = await api.getServer(created.id);
            if (s.state === "offline") {
              clearInterval(poll);
              await api.powerServer(created.id, "start");
              await refreshFleet();
            } else if (s.state !== "installing") {
              clearInterval(poll); // failed or already running — leave it be
            }
          } catch {
            clearInterval(poll);
          }
        }, 3000);
      }
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }
</script>

<div
  class="sheet"
  class:open
  id="nsForm"
  role="dialog"
  aria-modal="true"
  aria-labelledby="nsFormTitle"
  use:istyle={`--ox: ${ui.open.nsForm?.ox ?? '50%'}; --oy: ${ui.open.nsForm?.oy ?? '50%'}`}
  use:sheetFocus
>
  <div class="depth-head">
    <button class="surface-btn" onclick={() => closeSheet("nsForm")}>
      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6 10 V 2 M 2 6 L 6 2 L 10 6"/></svg>
      surface
    </button>
    <h2 class="depth-title" id="nsFormTitle">new server on <b class="node-name-inline">{node?.name ?? "—"}</b></h2>
    <div class="prefs-note"></div>
  </div>
  <div class="sheet-body ns-body">
    <section class="prefs-group" aria-label="New server settings">
      <div class="cfg-head">
        <h3 class="pane-label">every setting, one screen</h3>
        <span class="cfg-badge env">defaults from the game template</span>
        <span class="cfg-badge ok">ports allocated from the node's pool</span>
      </div>
      <div class="ns-pad ns-grid">
        <div class="ns-legend"><h4>game</h4><i></i><small>decides install method and port defaults</small></div>
        <div class="cfg-row">
          <span>game — from the specs you have</span>
          <select class="cfg-in" id="nsGame" aria-label="Game" bind:value={specId}>
            {#each fleet.specs as s (s.id)}
              <option value={s.id}>{s.name.toLowerCase()}</option>
            {/each}
          </select>
        </div>
        <div class="cfg-row">
          <span>platforms — the scheduler places by node</span>
          <input class="cfg-in" type="text" value={platformWords.join(" / ") || "—"} disabled aria-label="Platforms" />
        </div>
        <div class="cfg-row">
          <span>spec</span>
          <button class="cfg-btn ghost spec-link" id="specLink" onclick={(e) => openSheet("specs", e.clientX, e.clientY, e.currentTarget)}><span class="spec-link-name" id="specLinkName">{spec?.slug ?? "—"}</span>&nbsp;spec <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 3.5 8.5 L 8.5 3.5 M 5 3.5 H 8.5 V 7"/></svg></button>
        </div>

        <div class="ns-legend"><h4>identity</h4><i></i><small>what backups and moves carry</small></div>
        <div class="cfg-row">
          <span>server name</span>
          <input type="text" class="cfg-in" bind:value={name} aria-label="Server name" />
        </div>

        {#if spec?.variables?.some((v) => v.user_editable)}
          <div class="ns-legend"><h4>settings</h4><i></i><small>the spec's launch variables — changeable later</small></div>
          {#each spec.variables ?? [] as v (v.key)}
            {#if v.user_editable}
              <div class="cfg-row">
                <span>{v.label || v.key}</span>
                <input type="text" class="cfg-in" bind:value={vars[v.key]} aria-label={v.label || v.key} />
              </div>
            {/if}
          {/each}
        {/if}

        {#if spec?.ports?.length}
          <div class="ns-legend"><h4>network</h4><i></i><small>allocated by the panel from the node's port pool</small></div>
          {#each spec.ports as p (p.name)}
            <div class="cfg-row">
              <span>{p.name} port · {p.protocol}</span>
              <input type="text" class="cfg-in" value="allocated on create (default :{p.default})" disabled aria-label="{p.name} port" />
            </div>
          {/each}
        {/if}

        <div class="ns-legend"><h4>operations</h4><i></i><small>changeable later in the server's own settings</small></div>
        <label class="tgl"><input type="checkbox" bind:checked={startAfter} /><i></i>start once the install finishes</label>
        <label class="tgl"><input type="checkbox" bind:checked={nightlyBackup} /><i></i>nightly backup at 04:00</label>
        {#if spec?.install?.bepinex_compatible}
          <label class="tgl"><input type="checkbox" bind:checked={bepinex} /><i></i>install bepinex (mod loader)</label>
        {/if}
        {#if spec?.install?.requires_steam_login}
          <div class="cfg-row">
            <span>steam guard code — this game needs a steam login to install</span>
            <input type="text" class="cfg-in" bind:value={steamGuard} autocomplete="off" spellcheck="false" aria-label="Steam guard code" />
          </div>
        {/if}
      </div>
      <div class="ns-alloc">
        <div class="ns-cost" class:over={overCapacity}><span>memory after</span><b>{fmtGb(memAfter)}<em>/{node ? Math.round(node.total_memory_mb / 1024) : "—"}G</em></b></div>
        <div class="ns-cost"><span>rec memory</span><b>{fmtGb(allocMb)}<em>G</em></b></div>
        <div class="ns-cost"><span>ports needed</span><b>{spec?.ports?.length ?? 0}<em></em></b></div>
        <div class="ns-acts">
          {#if err}<span class="cfg-note" use:istyle={"color: var(--crisis)"}>{err}</span>
          {:else if overCapacity}<span class="cfg-note" use:istyle={"color: var(--caution)"}
            >{node?.name} has {fmtGb(node.total_memory_mb - node.allocated_memory_mb)}G free — this
            needs {fmtGb(allocMb)}G</span
          >{/if}
          <button class="cfg-btn ghost" onclick={() => closeSheet("nsForm")}>cancel</button>
          <button class="cfg-btn solid" disabled={busy || !spec} onclick={() => void create()}>create server</button>
        </div>
      </div>
    </section>
  </div>
</div>
