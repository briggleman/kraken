<script lang="ts">
  import { istyle } from "@/lib/istyle";
  import { ui, rotateDone } from "@/lib/state.svelte";
  import { api } from "@/api/client";
  import { auth, refreshMe, mustChangePassword } from "@/lib/auth.svelte";

  // Twelve characters is the only hard floor; the four segments above it are
  // advice, so the gate and the meter are deliberately two different questions.
  let cur = $state("");
  let next = $state("");
  let conf = $state("");
  let curEl: HTMLInputElement | undefined = $state();

  function pwScore(v: string): number {
    let s = 0;
    if (v.length >= 12) s++;
    if (/[a-z]/.test(v) && /[A-Z]/.test(v)) s++;
    if (/\d/.test(v)) s++;
    if (/[^A-Za-z0-9]/.test(v) || v.length >= 16) s++;
    return s;
  }

  let submitErr = $state<string | null>(null);
  let busy = $state(false);
  const open = $derived(ui.rotateOpen || (!!auth.user && mustChangePassword()));

  async function go() {
    if (busy) return;
    busy = true;
    submitErr = null;
    try {
      await api.changePassword(cur, next);
      await refreshMe(); // must_change_password flips off; the session rotated
      rotateDone();
    } catch (e) {
      submitErr = e instanceof Error ? e.message : "could not set the password";
    } finally {
      busy = false;
    }
  }

  const score = $derived(pwScore(next));
  const long = $derived(next.length >= 12);
  const match = $derived(conf.length > 0 && conf === next);
  const canGo = $derived(cur.length > 0 && long && match);

  // one word per segment, and no || fallback: an empty string for a real
  // score is falsy, so a fallback reported the BEST reading for the worst
  // passwords that cleared the floor
  const reading = $derived.by(() => {
    if (!next) return { text: "", cls: "" };
    if (!long) {
      const n = 12 - next.length;
      return { text: n + " more character" + (n === 1 ? "" : "s"), cls: "no" };
    }
    if (conf && !match) return { text: "the two do not match", cls: "no" };
    return { text: ["", "weak", "fair", "good", "strong"][score], cls: score >= 3 ? "ok" : "" };
  });

  $effect(() => {
    if (open) {
      cur = "";
      next = "";
      conf = "";
      setTimeout(() => curEl?.focus(), 700);
    }
  });
</script>

<div
  class="login"
  class:open={open}
  id="rotate"
  role="dialog"
  aria-modal="true"
  aria-labelledby="rtTitle"
>
  <div class="bg-rays lg-rays" aria-hidden="true">
    <div class="bg-ray" use:istyle={"left: 22%; width: 130px; height: 96vh; --ray-op: 0.13; --ray-dur: 19s; --ray-delay: 0s"}></div>
    <div class="bg-ray" use:istyle={"left: 47%; width: 90px; height: 92vh; --ray-op: 0.10; --ray-dur: 14s; --ray-delay: 1.2s"}></div>
    <div class="bg-ray" use:istyle={"left: 71%; width: 150px; height: 95vh; --ray-op: 0.12; --ray-dur: 23s; --ray-delay: 0.6s"}></div>
  </div>
  <form class="lg-card" autocomplete="on">
    <div class="lg-id">
      <span class="lg-mark kr-lock"><i class="kr-glyph" aria-hidden="true"></i><b class="kr-word kr-hollow">KRAKEN</b></span>
    </div>
    <div class="lg-head">
      <h1 class="lg-title" id="rtTitle">Set a new password</h1>
      <p class="lg-sub">you are signed in with the temporary password. choose a new one to continue.</p>
    </div>
    <p class="pw-note">
      <b>admin / admin</b> is what every kraken ships with, and it is printed in the install
      guide — so it is not a secret on any panel that can be reached. this is the last
      screen before the fleet, on purpose.
    </p>
    <div class="lg-form">
      <label class="cfg-row"><span>current password</span><input class="cfg-in" id="rtCur" type="password" autocomplete="current-password" bind:this={curEl} bind:value={cur} /></label>
      <label class="cfg-row"><span>new password</span><input class="cfg-in" id="rtNew" type="password" autocomplete="new-password" bind:value={next} /></label>
      <label class="cfg-row"><span>confirm new password</span><input class="cfg-in" id="rtConf" type="password" autocomplete="new-password" bind:value={conf} /></label>
      <div class="pw-meter" id="rtMeter" role="presentation">
        {#each [0, 1, 2, 3] as i}
          <span class="pw-seg{i < score ? ' lit' : ''}"></span>
        {/each}
      </div>
      <p class="pw-read {submitErr ? 'no' : reading.cls}" id="rtRead" role="status" aria-live="polite">{submitErr ?? reading.text}</p>
      <button class="cfg-btn solid lg-go" id="rtGo" type="button" disabled={!canGo || busy} onclick={() => void go()}>set password &amp; continue</button>
    </div>
    <div class="lg-foot">
      <span class="lg-note">twelve characters or more · nothing else is required</span>
      <span class="lg-note">this rotation is logged as a security event</span>
    </div>
  </form>
</div>
