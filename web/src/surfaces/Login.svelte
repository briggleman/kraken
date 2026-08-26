<script lang="ts">
  import { ui, wz, LG_SUB, loginGo } from "@/lib/state.svelte";

  // mock-state radios: signed out / refused / first run
  let lgState = $state("out");

  function go() {
    const resuming = wz.resumeSetup; // loginGo consumes this flag
    loginGo();
    if (resuming) return;
    if (lgState === "first") ui.rotateOpen = true;
  }
</script>

<div
  class="login"
  class:open={ui.loginOpen}
  id="login"
  role="dialog"
  aria-modal="true"
  aria-labelledby="lgTitle"
>
  <div class="bg-rays lg-rays" aria-hidden="true">
    <div class="bg-ray" style="left: 22%; width: 130px; height: 96vh; --ray-op: 0.13; --ray-dur: 19s; --ray-delay: 0s"></div>
    <div class="bg-ray" style="left: 47%; width: 90px; height: 92vh; --ray-op: 0.10; --ray-dur: 14s; --ray-delay: 1.2s"></div>
    <div class="bg-ray" style="left: 71%; width: 150px; height: 95vh; --ray-op: 0.12; --ray-dur: 23s; --ray-delay: 0.6s"></div>
  </div>
  <form class="lg-card" autocomplete="on">
    <div class="lg-id">
      <span class="lg-mark kr-lock"><i class="kr-glyph" aria-hidden="true"></i><b class="kr-word kr-hollow">KRAKEN</b></span>
    </div>
    <div class="lg-head">
      <h1 class="lg-title" id="lgTitle">Descend to your fleet</h1>
      <p class="lg-sub">{ui.loginSub ?? LG_SUB}</p>
    </div>
    <p class="lg-err" role="alert">
      <b>that username and password do not match.</b>
      <span>the same message either way — naming which half was wrong would answer “does this account exist” for anyone who asked.</span>
    </p>
    <div class="lg-form">
      <label class="cfg-row"><span>username</span><input class="cfg-in" type="text" autocomplete="username" spellcheck="false" autocapitalize="none" autocorrect="off" /></label>
      <label class="cfg-row"><span>password</span><input class="cfg-in" type="password" autocomplete="current-password" /></label>
      <button class="cfg-btn solid lg-go" type="button" onclick={go}>sign in</button>
    </div>
    <div class="lg-foot">
      <span class="lg-note">sessions last 24 hours · every attempt is logged</span>
      <span class="lg-note">kraken 0.25.0 · self-hosted</span>
      <div class="lg-states" role="group" aria-label="Mock state">
        <span class="synthetic">mock states</span>
        <label class="lg-st"><input type="radio" name="lgstate" value="out" bind:group={lgState} />signed out</label>
        <label class="lg-st"><input class="lg-bad" type="radio" name="lgstate" value="bad" bind:group={lgState} />refused</label>
        <label class="lg-st"><input class="lg-first" type="radio" name="lgstate" value="first" bind:group={lgState} />first run</label>
      </div>
    </div>
  </form>
</div>
