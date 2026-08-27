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
  import { startSim, ui, closeSheet, type SheetId } from "@/lib/state.svelte";
  import { auth, bootAuth, mustChangePassword } from "@/lib/auth.svelte";
  import { fleet, startFleetPolling, stopFleetPolling } from "@/lib/fleet.svelte";
  import { depth, surface, syncDepthFromFleet } from "@/lib/depth.svelte";
  import { api } from "@/api/client";

  startSim();
  void bootAuth();

  const authed = $derived(!auth.loading && !!auth.user);

  // fleet data flows only for a session past the rotation gate
  $effect(() => {
    if (authed && !mustChangePassword()) {
      startFleetPolling();
      void api
        .version()
        .then((v) => {
          if (!fleet.panelVersion) fleet.panelVersion = v.version;
        })
        .catch(() => {});
    } else {
      stopFleetPolling();
    }
  });

  // the 10s poll keeps the drilled server's chip/controls/stream in sync
  $effect(() => {
    fleet.servers;
    syncDepthFromFleet();
  });

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
    if (depth.open) surface();
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
