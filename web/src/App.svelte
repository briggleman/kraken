<script lang="ts">
  import Backdrop from "@/components/Backdrop.svelte";
  import Pane from "@/surfaces/Pane.svelte";
  import Depth from "@/surfaces/Depth.svelte";
  import ConfirmDelete from "@/surfaces/ConfirmDelete.svelte";
  import { startSim, ui, surface, closeSheet, type SheetId } from "@/lib/state.svelte";

  startSim();

  // Escape routing, top layer first (The Topmost Closes Last Rule):
  // confirm → open sheet → prefs → drill-in. Login/rotate/interstitial are
  // deliberately not members of the Escape family.
  function onKeydown(e: KeyboardEvent) {
    if (e.key !== "Escape") return;
    if (ui.confirm) {
      ui.confirm = null;
      return;
    }
    const openIds = Object.keys(ui.open) as SheetId[];
    const sheetId = openIds.find((id) => id !== "prefs");
    if (sheetId) {
      closeSheet(sheetId);
      return;
    }
    if (ui.open.prefs) {
      closeSheet("prefs");
      return;
    }
    if (ui.depthOpen) surface();
  }
</script>

<svelte:window onkeydown={onKeydown} />

<Backdrop />
<Pane />
<Depth />
<ConfirmDelete />
