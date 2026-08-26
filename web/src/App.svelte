<script lang="ts">
  import Backdrop from "@/components/Backdrop.svelte";
  import Pane from "@/surfaces/Pane.svelte";
  import Depth from "@/surfaces/Depth.svelte";
  import ConfirmDelete from "@/surfaces/ConfirmDelete.svelte";
  import Prefs from "@/surfaces/Prefs.svelte";
  import Users from "@/surfaces/Users.svelte";
  import Specs from "@/surfaces/Specs.svelte";
  import SpecEdit from "@/surfaces/SpecEdit.svelte";
  import NodeAdd from "@/surfaces/NodeAdd.svelte";
  import NodeCfg from "@/surfaces/NodeCfg.svelte";
  import NsForm from "@/surfaces/NsForm.svelte";
  import AuditLog from "@/surfaces/AuditLog.svelte";
  import ApiDocs from "@/surfaces/ApiDocs.svelte";
  import Login from "@/surfaces/Login.svelte";
  import Rotate from "@/surfaces/Rotate.svelte";
  import DbRestart from "@/surfaces/DbRestart.svelte";
  import Setup from "@/surfaces/Setup.svelte";
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
<Prefs />
<Users />
<Specs />
<SpecEdit />
<NodeAdd />
<NodeCfg />
<NsForm />
<AuditLog />
<ApiDocs />
<Login />
<Rotate />
<DbRestart />
<Setup />
<ConfirmDelete />
