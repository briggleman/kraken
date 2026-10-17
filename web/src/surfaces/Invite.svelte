<script lang="ts">
  import { istyle } from "@/lib/istyle";
  import { invite, inviteActive, inviteDone } from "@/lib/invite.svelte";
  import { inviteLeft } from "@/lib/invite";
  import { acceptInvite } from "@/lib/auth.svelte";
  import { errMsg } from "@/api/client";

  // The same floor and meter as the rotation sheet: twelve characters is the
  // only hard rule, the four segments above it are advice.
  let next = $state("");
  let conf = $state("");
  let nextEl: HTMLInputElement | undefined = $state();
  let busy = $state(false);
  let submitErr = $state<string | null>(null);

  const open = $derived(inviteActive());

  function pwScore(v: string): number {
    let s = 0;
    if (v.length >= 12) s++;
    if (/[a-z]/.test(v) && /[A-Z]/.test(v)) s++;
    if (/\d/.test(v)) s++;
    if (/[^A-Za-z0-9]/.test(v) || v.length >= 16) s++;
    return s;
  }

  const score = $derived(pwScore(next));
  const long = $derived(next.length >= 12);
  const match = $derived(conf.length > 0 && conf === next);
  const canGo = $derived(long && match && invite.status === "ready");

  const reading = $derived.by(() => {
    if (!next) return { text: "", cls: "" };
    if (!long) {
      const n = 12 - next.length;
      return { text: n + " more character" + (n === 1 ? "" : "s"), cls: "no" };
    }
    if (conf && !match) return { text: "the two do not match", cls: "no" };
    return { text: ["", "weak", "fair", "good", "strong"][score], cls: score >= 3 ? "ok" : "" };
  });

  const left = $derived(invite.info ? inviteLeft(invite.info.expires_at) : "");

  async function go() {
    if (busy || !canGo || !invite.token) return;
    busy = true;
    submitErr = null;
    try {
      await acceptInvite(invite.token, next);
      next = "";
      conf = "";
      inviteDone();
    } catch (e) {
      // The link can die between the sheet opening and the click: used
      // elsewhere, expired, revoked. The Panel says the same thing for all
      // of them, and so does this.
      submitErr = errMsg(e);
    } finally {
      busy = false;
    }
  }

  $effect(() => {
    if (invite.status === "ready") setTimeout(() => nextEl?.focus(), 700);
  });
</script>

<div
  class="login"
  class:open
  id="invite"
  role="dialog"
  aria-modal="true"
  aria-labelledby="ivTitle"
>
  <div class="bg-rays lg-rays" aria-hidden="true">
    <div class="bg-ray" use:istyle={"left: 22%; width: 130px; height: 96vh; --ray-op: 0.13; --ray-dur: 19s; --ray-delay: 0s"}></div>
    <div class="bg-ray" use:istyle={"left: 47%; width: 90px; height: 92vh; --ray-op: 0.10; --ray-dur: 14s; --ray-delay: 1.2s"}></div>
    <div class="bg-ray" use:istyle={"left: 71%; width: 150px; height: 95vh; --ray-op: 0.12; --ray-dur: 23s; --ray-delay: 0.6s"}></div>
  </div>
  <form
    class="lg-card"
    autocomplete="on"
    onsubmit={(e) => {
      e.preventDefault();
      void go();
    }}
  >
    <div class="lg-id">
      <span class="lg-mark kr-lock"><i class="kr-glyph" aria-hidden="true"></i><b class="kr-word kr-hollow">KRAKEN</b></span>
    </div>
    {#if invite.status === "loading"}
      <div class="lg-head">
        <h1 class="lg-title" id="ivTitle">Reading your invite</h1>
        <p class="lg-sub">one moment.</p>
      </div>
    {:else if invite.status === "invalid"}
      <div class="lg-head">
        <h1 class="lg-title" id="ivTitle">This invite link is not valid any more</h1>
        <p class="lg-sub">it may have been used already, run out after its 24 hours, or been replaced by a newer one. ask the person who invited you to send a new link.</p>
      </div>
      <div class="lg-form">
        <button class="cfg-btn ghost lg-go" type="button" onclick={() => inviteDone()}>go to sign in</button>
      </div>
    {:else if invite.info}
      <div class="lg-head">
        <h1 class="lg-title" id="ivTitle">Welcome aboard, {invite.info.username}</h1>
        <p class="lg-sub">
          an account on <b>{invite.info.panel_host}</b> with the {invite.info.role_name.toLowerCase()} role is waiting for a password.
          the link is good for another {left}.
        </p>
      </div>
      <div class="lg-form">
        <label class="cfg-row"><span>username</span><input class="cfg-in" type="text" autocomplete="username" value={invite.info.username} readonly /></label>
        <label class="cfg-row"><span>choose a password</span><input class="cfg-in" id="ivNew" type="password" autocomplete="new-password" bind:this={nextEl} bind:value={next} /></label>
        <label class="cfg-row"><span>confirm it</span><input class="cfg-in" id="ivConf" type="password" autocomplete="new-password" bind:value={conf} /></label>
        <div class="pw-meter" id="ivMeter" role="presentation">
          {#each [0, 1, 2, 3] as i}
            <span class="pw-seg{i < score ? ' lit' : ''}"></span>
          {/each}
        </div>
        <p class="pw-read {submitErr ? 'no' : reading.cls}" id="ivRead" role="status" aria-live="polite">{submitErr ?? reading.text}</p>
        <button class="cfg-btn solid lg-go" id="ivGo" type="submit" disabled={!canGo || busy}>{busy ? "setting…" : "set password & sign in"}</button>
      </div>
    {/if}
    <div class="lg-foot">
      <span class="lg-note">twelve characters or more · nothing else is required</span>
      <span class="lg-note">the link works once · accepting it is logged as a security event</span>
    </div>
  </form>
</div>
