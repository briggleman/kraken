<script lang="ts">
  import NodeBand from "./NodeBand.svelte";
  import ServerCard from "./ServerCard.svelte";
  import { ui, openSheet } from "@/lib/state.svelte";
  import { fleet } from "@/lib/fleet.svelte";
  import { logout } from "@/lib/auth.svelte";
  import { fmtHm } from "@/lib/fmt";

  // the events floor shows the audit tail — the four most recent entries
  const recent = $derived(fleet.audit.slice(0, 4));

  function footerOpen(e: MouseEvent | KeyboardEvent) {
    const el = e.currentTarget as HTMLElement;
    if (e instanceof KeyboardEvent) {
      const r = el.getBoundingClientRect();
      openSheet("auditLog", r.left + r.width / 2, r.top + r.height / 2, el);
    } else {
      openSheet("auditLog", (e as MouseEvent).clientX, (e as MouseEvent).clientY, el);
    }
  }

  function pingLabel(ms: number): string {
    if (!ms) return "—";
    return ms < 1000 ? ms + "ms" : (ms / 1000).toFixed(1) + "s";
  }
</script>

<div class="pane">
  <header class="top">
    <span class="wordmark kr-lock"
      ><i class="kr-glyph" aria-hidden="true"></i><b class="kr-word">KRAKEN</b></span
    >
    <span class="top-sub">single pane · all systems</span>
    <div class="top-right">
      <span><span class="live-dot">●</span> live · ping <span>{pingLabel(fleet.pingMs)}</span></span>
      <span id="clock">{ui.clock}</span>
      <button
        class="prefs-open io"
        title="api reference"
        aria-label="Open the api reference"
        onclick={(e) => openSheet("apiDocs", e.clientX, e.clientY, e.currentTarget)}
        ><svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6.4 2.4 C 4.9 2.4 5.7 7.1 3.4 8 C 5.7 8.9 4.9 13.6 6.4 13.6"/><path d="M 9.6 2.4 C 11.1 2.4 10.3 7.1 12.6 8 C 10.3 8.9 11.1 13.6 9.6 13.6"/></svg></button
      >
      <button
        class="prefs-open"
        id="addNodeBtn"
        aria-label="Add a node"
        onclick={(e) => openSheet("nodeAdd", e.clientX || innerWidth / 2, e.clientY || 0, e.currentTarget)}
        ><svg width="13" height="13" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 7 2.5 V 11.5 M 2.5 7 H 11.5"/></svg> add node</button
      >
      <button
        class="prefs-open"
        id="prefsBtn"
        aria-label="Open console settings"
        onclick={(e) => openSheet("prefs", e.clientX || innerWidth, e.clientY || 0, e.currentTarget)}
        ><svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="8" cy="8" r="2.4"/><path d="M8 1.4v1.8M8 12.8v1.8M1.4 8h1.8M12.8 8h1.8M3.3 3.3l1.3 1.3M11.4 11.4l1.3 1.3M12.7 3.3l-1.3 1.3M4.6 11.4l-1.3 1.3"/></svg> settings</button
      >
      <button
        class="prefs-open io"
        title="sign out"
        aria-label="Sign out"
        onclick={() => void logout()}
        ><svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6.2 2.6 H 3.4 A 1 1 0 0 0 2.4 3.6 V 12.4 A 1 1 0 0 0 3.4 13.4 H 6.2"/><path d="M 9.6 5.2 L 12.6 8 L 9.6 10.8"/><path d="M 12.6 8 H 6"/></svg></button
      >
    </div>
  </header>

  <main class="deck">
    {#each fleet.nodes as node, index (node.id)}
      <NodeBand {node} {index} />
    {/each}

    <div class="servers">
      {#each fleet.servers as server (server.id)}
        <ServerCard {server} />
      {:else}
        {#if fleet.loaded}
          <p class="roster-empty" style="grid-column: 1 / -1">
            no servers yet — deploy one from a node band's New Server
          </p>
        {/if}
      {/each}
    </div>
  </main>

  <!-- svelte-ignore a11y_no_noninteractive_element_to_interactive_role -->
  <!-- the whole footer IS the audit-log door, verbatim from the mock -->
  <footer
    class="floor fl-door"
    role="button"
    tabindex="0"
    aria-label="Open the full audit log"
    onclick={footerOpen}
    onkeydown={(e) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        footerOpen(e);
      }
    }}
  >
    <span class="floor-label">events</span>
    <div class="events">
      {#each recent as ev (ev.id)}
        <span class="ev{ev.status >= 400 ? ' warn' : ''}"
          ><span class="t">{fmtHm(ev.time)}</span><span class="who">{ev.actor}</span>
          — {ev.action} · {ev.status}</span
        >
      {:else}
        <span class="ev"><span class="t">—</span>no events yet</span>
      {/each}
    </div>
    <span class="fl-cue"
      >full audit log <svg width="12" height="8" viewBox="0 0 12 8" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M1 4h9M7 1l3 3-3 3"/></svg></span
    >
  </footer>
</div>
