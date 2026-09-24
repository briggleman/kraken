<script lang="ts">
  import { ui, closeConfirm, confirmGo, confirmWord } from "@/lib/state.svelte";

  // Anything irreversible is gated by the typed confirmation: the dialog
  // names the thing, states what is lost, and keeps its confirm disabled
  // until the word is typed (case-insensitively). The word is the verb on the
  // button — "retire" for a server (#360), "delete" for everything else.

  let typed = $state("");
  let confirmEl: HTMLDivElement;
  let inputEl: HTMLInputElement | undefined = $state();
  let cancelEl: HTMLButtonElement | undefined = $state();

  // An opener whose consequence destroys nothing (retiring an untracked
  // container) asks for a plain confirm: no word to type, and focus starts on
  // cancel so a stray Enter is the safe answer.
  const mustType = $derived(ui.confirm?.typed ?? true);
  const verb = $derived(ui.confirm?.verb ?? "delete");
  const word = $derived(confirmWord(ui.confirm));
  const ok = $derived(!mustType || typed.trim().toLowerCase() === word);
  const hint = $derived(mustType && typed.length && !ok ? "type " + word + " to enable" : "");

  $effect(() => {
    if (ui.confirm) {
      typed = "";
      if (ui.confirm.typed) inputEl?.focus();
      else cancelEl?.focus();
    }
  });

  // keep tabbing inside the dialog while it owns the screen
  function trapTab(e: KeyboardEvent) {
    // The Enter shortcut belongs to the typed word. Without one, Enter is
    // whatever the focused button does — which starts as cancel.
    if (e.key === "Enter" && mustType && ok) {
      e.preventDefault();
      confirmGo();
      return;
    }
    if (e.key !== "Tab") return;
    const f = [...confirmEl.querySelectorAll<HTMLElement>("input, button")].filter(
      (el) => !(el as HTMLButtonElement).disabled && el.offsetParent !== null,
    );
    if (!f.length) return;
    const first = f[0],
      last = f[f.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }
</script>

<!-- svelte-ignore a11y_interactive_supports_focus -->
<!-- keydown here is the focus trap; focus lives on the dialog's own controls -->
<div
  class="confirm"
  class:open={!!ui.confirm}
  id="confirmDelete"
  role="dialog"
  aria-modal="true"
  aria-labelledby="cdTitle"
  aria-describedby="cdBody"
  bind:this={confirmEl}
  onkeydown={trapTab}
>
  <div class="confirm-veil" onclick={closeConfirm} aria-hidden="true"></div>
  <div class="confirm-card">
    <h2 class="confirm-title" id="cdTitle">
      <svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 8 1.5 L 15 14 H 1 Z M 8 6 V 9.5 M 8 11.5 V 11.6"/></svg>
      {verb} <b id="cdName">{ui.confirm?.name ?? "server"}</b>
    </h2>
    <p class="confirm-body" id="cdBody">{ui.confirm?.body ?? ""}</p>
    {#if mustType}
      <label class="confirm-label" for="cdInput">type <b>{word}</b> to confirm</label>
      <input
        class="confirm-input"
        id="cdInput"
        type="text"
        autocomplete="off"
        autocapitalize="off"
        spellcheck="false"
        aria-describedby="cdHint"
        bind:this={inputEl}
        bind:value={typed}
      />
      <p class="confirm-hint" id="cdHint" role="status" aria-live="polite">{hint}</p>
    {/if}
    <div class="confirm-acts">
      <button class="cfg-btn ghost" bind:this={cancelEl} onclick={closeConfirm}>cancel</button>
      <button class="ctl ctl-delete" id="cdGo" disabled={!ok} onclick={confirmGo}
        >{verb} {ui.confirm?.noun ?? "server"}</button
      >
    </div>
  </div>
</div>
