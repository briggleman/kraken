<script lang="ts">
  import { untrack } from "svelte";
  import { istyle } from "@/lib/istyle";
  import { hasPerm } from "@/lib/auth.svelte";
  import { fleet, specOf } from "@/lib/fleet.svelte";
  import { serverArt } from "@/lib/views.svelte";
  import {
    keepLabel,
    keepStale,
    loadKeep,
    openPurge,
    openRevive,
    retired,
    retiredNode,
    retiredNote,
    retiredServers,
    retiredSlug,
    retiredWhen,
  } from "@/lib/retired.svelte";

  // The fleet's last section (DESIGN.md, Retired Row): a ledger row per
  // retired server in the specs sheet's voice — art, name over `game · was on
  // node`, when it went, what it kept, and two controls in read-then-destroy
  // order. No lamp and no status chip: a retired server is neither running nor
  // stopped, it is off its node. With nothing retired, the section is absent.
  const rows = $derived(retiredServers(fleet.servers));

  // What each row kept is read from the node it left, once per row (and again
  // when that node comes back) — never per fleet poll. The staleness check is
  // untracked so a reading landing does not re-run this effect.
  $effect(() => {
    for (const s of rows) {
      if (untrack(() => keepStale(s))) void loadKeep(s);
    }
  });

  const mayRevive = $derived(hasPerm("server.create"));
  const mayPurge = $derived(hasPerm("server.delete"));
</script>

<!-- Kept on screen, head only, while the answer to deleting the last retired
     row is still being spoken — otherwise that answer would vanish with it. -->
{#if rows.length > 0 || retired.note}
  <section class="retired" aria-labelledby="retiredTitle">
    <div class="retired-head">
      <h2 class="pane-label" id="retiredTitle">retired · {rows.length}</h2>
      <!-- The group's note, or — until the next action — what the last delete
           for good answered: the Panel's note (archives a shared target kept,
           a node owed the delete) or its refusal, in Caution. -->
      {#if retired.note && retired.noteFailed}
        <span class="retired-note" role="alert" use:istyle={"color: var(--caution)"}>{retired.note}</span>
      {:else if retired.note}
        <span class="retired-note" role="status">{retired.note}</span>
      {:else}
        <span class="retired-note">taken off their nodes; their backups are kept until they are deleted for good</span>
      {/if}
    </div>
    {#if rows.length > 0}<div class="spec-list retired-list">
      {#each rows as s (s.id)}
        {@const art = serverArt(s)}
        {@const note = retiredNote(s)}
        {@const keep = retired.keep[s.id]?.reading}
        {@const busy = !!retired.busy[s.id]}
        <div class="spec-row retired-row">
          {#if art}<span class="spec-art" use:istyle={`background-image: url('${art}')`} aria-hidden="true"></span>{/if}<span class="spec-shade" aria-hidden="true"></span>
          <span class="spec-id"><span class="spec-name">{s.name}</span><span class="spec-slug">{retiredSlug(s, specOf(s)?.name, retiredNode(s))}</span></span>
          <span class="rt-when" title={s.retired_at}>{retiredWhen(s)}</span>
          <!-- A queued removal is said once, on the node band's removals line;
               only a final backup the retire could not take is said here. -->
          <span class="rt-keep" title={keep?.kind === "unread" ? keep.title : undefined}
            >{#if note}<span class="rt-note" title={note.title}>{note.word}</span> · {/if}{keepLabel(keep)}</span
          >
          <span class="spec-act">
            {#if mayRevive}
              <button class="cfg-btn ghost spec-go" disabled={busy} onclick={(e) => openRevive(s, e)}>revive</button>
            {/if}
            {#if mayPurge}
              <button class="cfg-btn danger" disabled={busy} onclick={(e) => openPurge(s, e.currentTarget)}
                >{busy ? "deleting…" : "delete for good"}</button
              >
            {/if}
          </span>
        </div>
      {/each}
    </div>{/if}
  </section>
{/if}
