<script lang="ts">
  import { ui } from "@/lib/state.svelte";
</script>

<div
  class="dbr"
  class:open={ui.dbRestartOpen}
  id="dbRestart"
  role="dialog"
  aria-modal="true"
  aria-labelledby="dbrTitle"
  aria-describedby="dbrBody"
>
  <div class="dbr-card">
    <div class="dbr-pulse" aria-hidden="true"><i></i><i></i><i></i></div>
    <h2 class="dbr-title" id="dbrTitle">connecting to postgres</h2>
    <p class="dbr-body" id="dbrBody">the panel is restarting onto your database. you will come back to sign-in in a moment — log in again and setup picks up where it left off.</p>
  </div>
</div>
