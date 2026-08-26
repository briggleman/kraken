<script lang="ts">
  import { ui, wz, LG_SUB, loginGo } from "@/lib/state.svelte";
  import { auth, login } from "@/lib/auth.svelte";
  import { fleet } from "@/lib/fleet.svelte";

  // The login is real: it shows whenever there is no session (and during the
  // wizard's restart choreography), takes actual credentials, and shows the
  // same refusal either way. The house CSS keys the error styling off a
  // .lg-bad:checked input, so a hidden input carries the error state.
  let username = $state("");
  let password = $state("");
  let failed = $state(false);
  let busy = $state(false);

  const open = $derived(ui.loginOpen || (!auth.loading && !auth.user));

  async function go() {
    if (busy) return;
    busy = true;
    failed = false;
    try {
      await login(username, password);
      password = "";
      const resuming = wz.resumeSetup; // loginGo consumes this flag
      loginGo();
      if (!resuming) ui.loginOpen = false;
    } catch {
      failed = true;
    } finally {
      busy = false;
    }
  }
</script>

<div
  class="login"
  class:open
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
  <form
    class="lg-card"
    autocomplete="on"
    onsubmit={(e) => {
      e.preventDefault();
      void go();
    }}
  >
    <input class="lg-bad" type="checkbox" checked={failed} hidden aria-hidden="true" tabindex="-1" />
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
      <label class="cfg-row"><span>username</span><input class="cfg-in" type="text" autocomplete="username" spellcheck="false" autocapitalize="none" autocorrect="off" bind:value={username} /></label>
      <label class="cfg-row"><span>password</span><input class="cfg-in" type="password" autocomplete="current-password" bind:value={password} /></label>
      <button class="cfg-btn solid lg-go" type="submit" disabled={busy}>sign in</button>
    </div>
    <div class="lg-foot">
      <span class="lg-note">sessions last 24 hours · every attempt is logged</span>
      <span class="lg-note">kraken {fleet.panelVersion || "—"} · self-hosted</span>
    </div>
  </form>
</div>
