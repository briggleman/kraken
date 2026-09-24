package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// #360: the delete button retires. A retired server keeps its id, config,
// schedules (switched off) and backups; loses its containers, world, memory,
// ports, DNS and forwards; can be revived — with a backup restored and the
// server started — or deleted permanently, which is the only thing that
// touches its archives.

type retireView struct {
	ID                string            `json:"id"`
	State             string            `json:"state"`
	NodeID            string            `json:"node_id"`
	Ports             map[string]int    `json:"ports"`
	Vars              map[string]string `json:"vars"`
	MemoryMB          int               `json:"memory_mb"`
	LastError         string            `json:"last_error"`
	RetiredAt         *time.Time        `json:"retired_at"`
	RetireNote        string            `json:"retire_note"`
	RetiredFromNodeID string            `json:"retired_from_node_id"`
	RetiredPorts      map[string]int    `json:"retired_ports"`
	Retire            *struct {
		Phase       string `json:"phase"`
		FinalBackup bool   `json:"final_backup"`
	} `json:"retire"`
	RestoreResult *struct {
		BackupID string `json:"backup_id"`
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
	} `json:"restore_result"`
}

func getRetireView(t *testing.T, h http.Handler, token, id string) retireView {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/v1/servers/"+id, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get server: %d %s", rec.Code, rec.Body.String())
	}
	var v retireView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode server: %v", err)
	}
	return v
}

// retireAndWait retires id and waits until the retire has ended — the row
// retired, or back without its retire block (abandoned).
func retireAndWait(t *testing.T, h http.Handler, token, id string, finalBackup bool) retireView {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+id+"/retire", token, map[string]bool{"final_backup": finalBackup})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("retire: %d %s, want 202", rec.Code, rec.Body.String())
	}
	var started retireView
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode retire: %v", err)
	}
	if started.Retire == nil || started.Retire.FinalBackup != finalBackup {
		t.Fatalf("the 202 carries retire %+v, want the job with final_backup=%v", started.Retire, finalBackup)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		v := getRetireView(t, h, token, id)
		if v.State == string(store.StateRetired) || v.Retire == nil {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("the retire never ended: %+v", v)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// retireServer retires without a final backup and requires it to land.
func retireServer(t *testing.T, h http.Handler, token, id string) retireView {
	t.Helper()
	v := retireAndWait(t, h, token, id, false)
	if v.State != string(store.StateRetired) {
		t.Fatalf("retire did not land: %+v", v)
	}
	return v
}

// placedServerOn is placedServer on an explicit host port, so a test can tell
// the port a revive prefers from the spec's default (27015).
func placedServerOn(t *testing.T, st *flakyStore, id, nodeID, specID string, port int) *store.Server {
	t.Helper()
	ctx := context.Background()
	n, err := st.Store.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	ports, err := n.Reserve(1024, []cluster.PortRequest{{Name: "game", Preferred: port}})
	if err != nil || ports["game"] != port {
		t.Fatalf("reserve %d: %v %v", port, ports, err)
	}
	if err := st.Store.UpdateNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	return seedOfflineServer(t, st.Store, id, nodeID, specID, func(s *store.Server) {
		s.Ports = ports
		s.MemoryMB = 1024
		s.Vars["PORT_GAME"] = "27050"
		now := time.Now()
		s.ProvisionedAt = &now
	})
}

func schedulesOf(t *testing.T, st *flakyStore, serverID string) map[string]*store.ScheduledTask {
	t.Helper()
	list, err := st.ListSchedulesByServer(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*store.ScheduledTask{}
	for _, s := range list {
		out[s.ID] = s
	}
	return out
}

// The headline: a running server on a live node with the final backup on. It
// is stopped, backed up, removed with its world, released, its schedules
// switched off (and flagged), and the row stays — retired, on no node,
// remembering where it was. Its backups are still listed.
func TestRetire_LiveNodeWithFinalBackup(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-retire")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-live", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-retire", nodeID, specID)
	seedSchedule(t, st, "sched-on", sv.ID)
	sv.State = store.StateRunning
	if err := st.UpdateServer(ctx, sv); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var actions []agentpb.PowerAction
	rt.SetPowerHook(func(_ string, a agentpb.PowerAction) {
		mu.Lock()
		defer mu.Unlock()
		actions = append(actions, a)
	})

	v := retireAndWait(t, h, token, sv.ID, true)

	if v.State != "retired" || v.RetiredAt == nil || v.NodeID != "" || v.RetiredFromNodeID != nodeID {
		t.Fatalf("retired row = %+v, want retired, on no node, from %s", v, nodeID)
	}
	if v.RetiredPorts["game"] != 27015 || len(v.Ports) != 0 || v.RetireNote != "" || v.Retire != nil {
		t.Fatalf("retired row = %+v, want its old port remembered, none held, no note, no job", v)
	}
	mu.Lock()
	if len(actions) != 1 || actions[0] != agentpb.PowerAction_POWER_ACTION_STOP {
		t.Errorf("power actions = %v, want one STOP before the backup", actions)
	}
	mu.Unlock()
	backups := rt.Backups(sv.ID)
	if len(backups) != 1 || backups[0].Name != "final-before-retire" {
		t.Fatalf("archives = %+v, want the final backup", backups)
	}
	if got := rt.Removals(); len(got) != 1 || got[0].ServerID != sv.ID || !got[0].DeleteData {
		t.Fatalf("removals = %+v, want %s with delete_data", got, sv.ID)
	}
	if len(rt.Purges()) != 0 {
		t.Fatal("a retire purged archives; only a permanent delete may")
	}
	if held(t, st, nodeID) || len(pendingRemovals(t, st, nodeID)) != 0 {
		t.Fatal("a confirmed retire left the allocation held or a removal owed")
	}
	sched := schedulesOf(t, st, sv.ID)["sched-on"]
	if sched == nil || sched.Enabled || !sched.DisabledByRetire {
		t.Fatalf("schedule after retire = %+v, want kept, switched off and flagged", sched)
	}

	// The point of retiring: the backups are still there to be listed.
	rec := do(t, h, http.MethodGet, "/api/v1/servers/"+sv.ID+"/backups", token, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "final-before-retire") {
		t.Fatalf("backups of a retired server: %d %s, want the final backup listed", rec.Code, rec.Body.String())
	}
	// And a second retire is refused.
	rec = do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/retire", token, map[string]bool{"final_backup": true})
	if rec.Code != http.StatusConflict || codedBody(t, rec.Body.Bytes()).Code != "server_retired" {
		t.Fatalf("retire again: %d %s, want 409 server_retired", rec.Code, rec.Body.String())
	}
}

// A final backup that fails does not stop the retire; the row says so.
func TestRetire_FinalBackupFailureIsNoted(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-bfail")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-bfail", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-bfail", nodeID, specID)
	rt.SetBackupFailure("no space left on device")

	v := retireAndWait(t, h, token, sv.ID, true)
	if v.State != "retired" {
		t.Fatalf("state = %q, want retired despite the failed backup", v.State)
	}
	if !strings.Contains(v.RetireNote, "final backup failed: no space left on device") {
		t.Fatalf("retire_note = %q, want the backup's failure", v.RetireNote)
	}
	if got := rt.Removals(); len(got) != 1 {
		t.Fatalf("removals = %+v, want the retire's", got)
	}
}

// An unreachable node: the retire still happens, the backup is skipped and
// said so, and the removal is owed to the node holding the allocation.
func TestRetire_UnreachableNodeSkipsTheBackupAndQueuesTheRemoval(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, _, stop := startStoppableAgent(t, "node-gone")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-gone", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-gone", nodeID, specID)
	stop()

	v := retireAndWait(t, h, token, sv.ID, true)
	if v.State != "retired" || v.RetiredFromNodeID != nodeID {
		t.Fatalf("row = %+v, want retired from %s", v, nodeID)
	}
	if !strings.Contains(v.RetireNote, "final backup skipped: node unreachable") || !strings.Contains(v.RetireNote, "queued") {
		t.Fatalf("retire_note = %q, want the skipped backup and the queued removal", v.RetireNote)
	}
	owed := pendingRemovals(t, st, nodeID)
	if len(owed) != 1 || owed[0].ServerID != sv.ID || !owed[0].DeleteData || owed[0].DeleteBackups {
		t.Fatalf("pending = %+v, want the retire's removal (data, not backups)", owed)
	}
	if !held(t, st, nodeID) {
		t.Fatal("the allocation was released while the container may still hold it")
	}
}

// A retire whose removal cannot be recorded does not happen: the row keeps its
// state and says why.
func TestRetire_AbandonedWhenTheRemovalCannotBeRecorded(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, _, stop := startStoppableAgent(t, "node-unrec")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-unrec", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-unrec", nodeID, specID)
	stop()
	st.failUpdateNode.Store(true)
	v := retireAndWait(t, h, token, sv.ID, false)
	st.failUpdateNode.Store(false)
	if v.State != "offline" || v.Retire != nil || !strings.HasPrefix(v.RetireNote, "retire abandoned: ") {
		t.Fatalf("row = %+v, want offline, no job, and the reason", v)
	}
	if len(pendingRemovals(t, st, nodeID)) != 0 {
		t.Fatal("an abandoned retire left a removal owed")
	}
}

// Refusals before anything starts.
func TestRetire_Refusals(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, _ := startFakeAgentRuntime(t, "node-refuse")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-refuse", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-installing", nodeID, specID)
	sv.State = store.StateInstalling
	if err := st.UpdateServer(context.Background(), sv); err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/retire", token, nil)
	if rec.Code != http.StatusConflict || codedBody(t, rec.Body.Bytes()).Code != "server_busy" {
		t.Fatalf("retire while installing: %d %s, want 409 server_busy", rec.Code, rec.Body.String())
	}
	if v := getRetireView(t, h, token, sv.ID); v.Retire != nil {
		t.Fatalf("a refused retire left a job on the row: %+v", v.Retire)
	}
	// Retire needs server.delete.
	viewer := asUser(t, st, "viewer-retire", nil)
	if rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/retire", viewer, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("retire without server.delete: %d, want 403", rec.Code)
	}
}

// ---- revive ----

// Revive defaults to the node the server was retired from and asks for its
// old ports; free, it gets them. The install runs, the schedules the retire
// switched off come back on (and only those), and the server lands offline.
func TestRevive_DefaultNodeAndOldPorts(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, _ := startFakeAgentRuntime(t, "node-revive")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "revive-default", map[string]any{"script": "install.sh"})
	sv := placedServerOn(t, st, "sv-revive", nodeID, specID, 27050)
	seedSchedule(t, st, "sched-on", sv.ID)
	seedSchedule(t, st, "sched-off", sv.ID)
	off := schedulesOf(t, st, sv.ID)["sched-off"]
	off.Enabled = false
	if err := st.UpdateSchedule(ctx, off); err != nil {
		t.Fatal(err)
	}
	retireServer(t, h, token, sv.ID)

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/revive", token, map[string]any{})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("revive: %d %s, want 202", rec.Code, rec.Body.String())
	}
	var v retireView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.State != "installing" || v.NodeID != nodeID || v.Ports["game"] != 27050 || v.Vars["PORT_GAME"] != "27050" {
		t.Fatalf("revive answer = %+v, want installing on %s with its old port 27050", v, nodeID)
	}
	if v.RetiredAt != nil || v.RetiredFromNodeID != "" || len(v.RetiredPorts) != 0 {
		t.Fatalf("revive answer still reads retired: %+v", v)
	}
	waitForState(t, h, token, sv.ID, "offline")
	if n, err := st.Store.GetNode(ctx, nodeID); err != nil || n.Ports.IsFree(27050) || n.AllocatedMemoryMB != 1024 {
		t.Fatalf("node after revive = %+v (%v), want port 27050 and 1024 MB reserved again", n, err)
	}
	scheds := schedulesOf(t, st, sv.ID)
	if on := scheds["sched-on"]; !on.Enabled || on.DisabledByRetire {
		t.Fatalf("schedule the retire switched off = %+v, want back on and unflagged", on)
	}
	if off := scheds["sched-off"]; off.Enabled {
		t.Fatal("a schedule the operator had switched off was switched on by the revive")
	}
}

// A port taken since the retire falls back to the normal allocation.
func TestRevive_FallsBackWhenTheOldPortIsTaken(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, _ := startFakeAgentRuntime(t, "node-taken")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "revive-taken", map[string]any{"script": "install.sh"})
	sv := placedServerOn(t, st, "sv-taken", nodeID, specID, 27050)
	retireServer(t, h, token, sv.ID)
	n, err := st.Store.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.Reserve(512, []cluster.PortRequest{{Name: "other", Preferred: 27050}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Store.UpdateNode(ctx, n); err != nil {
		t.Fatal(err)
	}

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/revive", token, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("revive: %d %s", rec.Code, rec.Body.String())
	}
	var v retireView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.Ports["game"] != 27015 {
		t.Fatalf("port = %d, want the spec default 27015 with the old one taken", v.Ports["game"])
	}
	waitForState(t, h, token, sv.ID, "offline")
}

// Revive restores the chosen backup after the install and then starts the
// server — through the same restore job an operator's restore runs.
func TestRevive_RestoresThenStarts(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-restore-start")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "revive-restore", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-rs", nodeID, specID)
	retireAndWait(t, h, token, sv.ID, true)
	backups := rt.Backups(sv.ID)
	if len(backups) != 1 {
		t.Fatalf("setup: archives %+v", backups)
	}

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/revive", token,
		map[string]any{"restore_backup_id": backups[0].Id, "start": true})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("revive: %d %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "running")
	v := getRetireView(t, h, token, sv.ID)
	if v.RestoreResult == nil || !v.RestoreResult.OK || v.RestoreResult.BackupID != backups[0].Id {
		t.Fatalf("restore_result = %+v, want the chosen backup restored", v.RestoreResult)
	}
	if got := rt.Restores(sv.ID); len(got) != 1 || got[0] != "stream:"+backups[0].Id {
		t.Fatalf("restores = %v, want the streamed restore of the chosen backup", got)
	}
	if n := len(rt.InstallScripts(sv.ID)); n != 1 {
		t.Fatalf("%d install passes, want 1: the start inside the fresh-install window skips the update pass", n)
	}
}

// A backup that is not on the node the server lands on is refused up front,
// before anything is reserved.
func TestRevive_RefusesABackupTheNodeDoesNotHave(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, _ := startFakeAgentRuntime(t, "node-nobackup")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "revive-nobackup", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-nb", nodeID, specID)
	retireServer(t, h, token, sv.ID)
	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/revive", token, map[string]any{"restore_backup_id": "nope"})
	if rec.Code != http.StatusConflict || codedBody(t, rec.Body.Bytes()).Code != "backup_not_found" {
		t.Fatalf("revive with a missing backup: %d %s, want 409 backup_not_found", rec.Code, rec.Body.String())
	}
	if v := getRetireView(t, h, token, sv.ID); v.State != "retired" || held(t, st, nodeID) {
		t.Fatalf("a refused revive changed something: %+v", v)
	}
}

// Revive is refused on a server that is not retired, and while the retire's
// removal is still owed to its node — it would delete the revived world the
// moment the node answered.
func TestRevive_Refusals(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-rr")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "revive-refuse", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-rr", nodeID, specID)

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/revive", token, nil)
	if rec.Code != http.StatusConflict || codedBody(t, rec.Body.Bytes()).Code != "server_not_retired" {
		t.Fatalf("revive a live server: %d %s, want 409 server_not_retired", rec.Code, rec.Body.String())
	}

	rt.SetRemoveFailure("docker daemon is restarting")
	retireServer(t, h, token, sv.ID)
	rec = do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/revive", token, nil)
	if rec.Code != http.StatusConflict || codedBody(t, rec.Body.Bytes()).Code != "removal_pending" {
		t.Fatalf("revive with the removal owed: %d %s, want 409 removal_pending", rec.Code, rec.Body.String())
	}
	// Once the node confirms, the revive goes ahead.
	rt.SetRemoveFailure("")
	dueNow(t, st, nodeID)
	srv.ReconcileNodesOnceForTest(context.Background())
	if rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/revive", token, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("revive after the removal landed: %d %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "offline")
}

// ---- permanent delete ----

func TestPermanentDelete_RefusedOnALiveServer(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-live-del")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "delete-live-refused", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-ld", nodeID, specID)
	rec := do(t, h, http.MethodDelete, "/api/v1/servers/"+sv.ID, token, nil)
	if rec.Code != http.StatusConflict || codedBody(t, rec.Body.Bytes()).Code != "server_not_retired" {
		t.Fatalf("delete a live server: %d %s, want 409 server_not_retired", rec.Code, rec.Body.String())
	}
	if len(rt.Removals()) != 0 {
		t.Fatal("a refused delete reached the node")
	}
}

// On the zero-config layout the node deletes the archives; the row and its
// schedules go.
func TestPermanentDelete_DeletesTheArchivesOnTheNamespacedLayout(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-purge")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "delete-purge", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-purge", nodeID, specID)
	seedSchedule(t, st, "sched-purge", sv.ID)
	retireAndWait(t, h, token, sv.ID, true)

	rec := do(t, h, http.MethodDelete, "/api/v1/servers/"+sv.ID, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("permanent delete: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Note           string `json:"note"`
		RemovalPending bool   `json:"removal_pending"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Note != "" || body.RemovalPending {
		t.Fatalf("delete answer = %+v, want nothing to say", body)
	}
	if got := rt.Purges(); len(got) != 1 || got[0] != sv.ID || len(rt.Backups(sv.ID)) != 0 {
		t.Fatalf("purges = %v, archives left %d; want the archives deleted", got, len(rt.Backups(sv.ID)))
	}
	if _, err := st.GetServer(ctx, sv.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("row after permanent delete: %v", err)
	}
	if left := schedulesOf(t, st, sv.ID); len(left) != 0 {
		t.Fatalf("%d schedules outlived their server", len(left))
	}
}

// On a shared target the archives cannot be attributed, so they are kept and
// the answer says so.
func TestPermanentDelete_KeepsArchivesOnASharedTarget(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-shared")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "delete-shared", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-shared", nodeID, specID)
	retireAndWait(t, h, token, sv.ID, true)
	rt.SetSharedBackupTarget("the network share")

	rec := do(t, h, http.MethodDelete, "/api/v1/servers/"+sv.ID, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("permanent delete: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "archives on a shared backup target were kept (the network share)") {
		t.Fatalf("delete answer %s, want the kept-archives note", rec.Body.String())
	}
	if len(rt.Backups(sv.ID)) != 1 {
		t.Fatal("an archive on a shared target was deleted")
	}
}

// An unreachable node is owed the permanent delete — delete_backups and all —
// holding nothing (the retire released it); the next replay delivers it.
func TestPermanentDelete_UnreachableNodeIsOwedTheArchives(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, _, stop := startStoppableAgent(t, "node-owe")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "delete-owe", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-owe", nodeID, specID)
	retireServer(t, h, token, sv.ID)
	stop()

	rec := do(t, h, http.MethodDelete, "/api/v1/servers/"+sv.ID, token, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"removal_pending":true`) {
		t.Fatalf("permanent delete, node away: %d %s", rec.Code, rec.Body.String())
	}
	owed := pendingRemovals(t, st, nodeID)
	if len(owed) != 1 || !owed[0].DeleteBackups || !owed[0].DeleteData || owed[0].MemoryMB != 0 || len(owed[0].Ports) != 0 {
		t.Fatalf("pending = %+v, want delete_backups owed and no allocation held", owed)
	}

	addr2, rt2, _ := startStoppableAgent(t, "node-owe")
	n, err := st.Store.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	n.Address = addr2
	if err := st.Store.UpdateNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	dueNow(t, st, nodeID)
	srv.ReconcileNodesOnceForTest(ctx)
	if got := rt2.Purges(); len(got) != 1 || got[0] != sv.ID {
		t.Fatalf("purges on the returning node = %v, want the owed one", got)
	}
	if len(pendingRemovals(t, st, nodeID)) != 0 {
		t.Fatal("the owed permanent delete was not finished")
	}
}

// ---- the #370 interaction ----

// A replay waits while a live row answers to the id on its node — and
// proceeds once that row is retired: the removal is the retire's own.
func TestPendingRemoval_ProceedsForARetiredRowAndWaitsForALiveOne(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-claims")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "claims", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-claims", nodeID, specID)
	n, err := st.Store.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	n.AddPendingRemoval(cluster.PendingRemoval{ServerID: sv.ID, DeleteData: true, RequestedAt: time.Now()})
	if err := st.Store.UpdateNode(ctx, n); err != nil {
		t.Fatal(err)
	}

	srv.ReconcileNodesOnceForTest(ctx)
	if got := rt.Removals(); len(got) != 0 {
		t.Fatalf("removals = %+v: a replay destroyed a live server's world", got)
	}

	sv.State, sv.NodeID, sv.RetiredFromNodeID = store.StateRetired, "", nodeID
	if err := st.UpdateServer(ctx, sv); err != nil {
		t.Fatal(err)
	}
	srv.ReconcileNodesOnceForTest(ctx)
	if got := rt.Removals(); len(got) != 1 || got[0].ServerID != sv.ID || !got[0].DeleteData {
		t.Fatalf("removals = %+v, want the retired row's removal delivered", got)
	}
	if len(pendingRemovals(t, st, nodeID)) != 0 {
		t.Fatal("the delivered removal was not finished")
	}
}

// ---- gates ----

func seedRetiredServer(t *testing.T, st *flakyStore, id, nodeID, specID string) {
	t.Helper()
	now := time.Now()
	seedOfflineServer(t, st.Store, id, "", specID, func(s *store.Server) {
		s.State = store.StateRetired
		s.RetiredAt = &now
		s.RetiredFromNodeID = nodeID
		s.RetiredPorts = s.Ports
		s.Ports = map[string]int{}
	})
}

// Everything that would change a retired server — or read files it no longer
// has — answers 409 server_retired; its backup list still answers.
func TestRetiredServer_Gates(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-gates")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "gates", map[string]any{"script": "install.sh"})
	seedRetiredServer(t, st, "sv-gated", nodeID, specID)
	if _, err := rt.CreateBackup(ctx, "sv-gated", "", "kept", nil, nil); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/servers/sv-gated"
	gated := []struct {
		name, method, path string
		body               any
	}{
		{"start", http.MethodPost, base + "/power", map[string]string{"action": "start"}},
		{"restart", http.MethodPost, base + "/power", map[string]string{"action": "restart"}},
		{"stop", http.MethodPost, base + "/power", map[string]string{"action": "stop"}},
		{"reinstall", http.MethodPost, base + "/reinstall", nil},
		{"settings save", http.MethodPut, base + "/settings", map[string]any{"values": map[string]string{}}},
		{"file list", http.MethodGet, base + "/files?path=.", nil},
		{"file write", http.MethodPost, base + "/files/write", map[string]string{"path": "a.txt", "content": "x"}},
		{"backup create", http.MethodPost, base + "/backups", map[string]string{"name": "snap"}},
		{"backup delete", http.MethodDelete, base + "/backups/whatever", nil},
		{"restore", http.MethodPost, base + "/backups/whatever/restore", nil},
		{"retire", http.MethodPost, base + "/retire", nil},
		{"sftp password", http.MethodPost, base + "/sftp/password", nil},
	}
	for _, tc := range gated {
		rec := do(t, h, tc.method, tc.path, token, tc.body)
		if rec.Code != http.StatusConflict || codedBody(t, rec.Body.Bytes()).Code != "server_retired" {
			t.Errorf("%s on a retired server: %d %s; want 409 server_retired", tc.name, rec.Code, rec.Body.String())
		}
	}
	rec := do(t, h, http.MethodGet, base+"/backups", token, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"kept"`) {
		t.Fatalf("backup list of a retired server: %d %s, want its archives", rec.Code, rec.Body.String())
	}
	if len(rt.Removals()) != 0 || len(rt.Restores("sv-gated")) != 0 || len(rt.Backups("sv-gated")) != 1 {
		t.Fatal("a refused request reached the node")
	}
}

// The reconciler never touches a retired row — not even when the node reports
// a container running under its id (an orphan whose removal is owed, say).
func TestReconciler_SkipsRetiredServers(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-rec")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "reconcile-retired", map[string]any{"script": "install.sh"})
	seedRetiredServer(t, st, "sv-rec", nodeID, specID)
	if _, err := rt.Power(ctx, "sv-rec", agentpb.PowerAction_POWER_ACTION_START); err != nil {
		t.Fatal(err)
	}
	srv.ReconcileOnceForTest(ctx)
	if got := getRetireView(t, h, token, "sv-rec").State; got != "retired" {
		t.Fatalf("after a reconcile pass the retired server reads %q", got)
	}
}

// A scheduled task switched back on by hand still does nothing to a retired
// server, and says so.
func TestSchedule_SkipsARetiredServer(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-sched")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "sched-retired", map[string]any{"script": "install.sh"})
	seedRetiredServer(t, st, "sv-sched", nodeID, specID)
	past := time.Now().Add(-time.Minute)
	if err := st.CreateSchedule(ctx, &store.ScheduledTask{
		ID: "sched-bk", ServerID: "sv-sched", Name: "nightly", Action: store.ScheduleBackup,
		Cron: "0 4 * * *", Enabled: true, NextRunAt: &past, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	srv.RunDueSchedulesForTest(ctx)
	task := schedulesOf(t, st, "sv-sched")["sched-bk"]
	if !strings.Contains(task.LastError, "retired") || len(rt.Backups("sv-sched")) != 0 {
		t.Fatalf("schedule last_error %q, archives %d; want it skipped as retired", task.LastError, len(rt.Backups("sv-sched")))
	}
}

// The state, not the empty node_id, is what lets the replay through: a
// retired row that still names the node (hand-edited, or written by a Panel
// before node_id was cleared on retire) claims nothing either.
func TestPendingRemoval_ARetiredRowNeverClaimsTheID(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-named")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "claims-named", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-named", nodeID, specID)
	sv.State, sv.RetiredFromNodeID = store.StateRetired, nodeID // NodeID left naming the node
	if err := st.UpdateServer(ctx, sv); err != nil {
		t.Fatal(err)
	}
	n, err := st.Store.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	n.AddPendingRemoval(cluster.PendingRemoval{ServerID: sv.ID, DeleteData: true, RequestedAt: time.Now()})
	if err := st.Store.UpdateNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	srv.ReconcileNodesOnceForTest(ctx)
	if got := rt.Removals(); len(got) != 1 || got[0].ServerID != sv.ID {
		t.Fatalf("removals = %+v: a retired row held back its own removal", got)
	}
}

// A Panel that restarts mid-retire leaves the row's retire block and no job.
// Before the removal the retire is abandoned and says so; during it, the
// removal is queued again (the Agent may never have heard) holding the
// allocation the node provably still has, and the retire completes.
func TestReconciler_SettlesAnOrphanedRetire(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, _ := startFakeAgentRuntime(t, "node-orphan")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "orphan-retire", map[string]any{"script": "install.sh"})

	early := placedServerOn(t, st, "sv-early", nodeID, specID, 27050)
	early.Retire = &store.ServerRetire{Phase: store.RetirePhaseBackingUp, FinalBackup: true, StartedAt: time.Now()}
	if err := st.UpdateServer(ctx, early); err != nil {
		t.Fatal(err)
	}
	late := placedServer(t, st, "sv-late", nodeID, specID)
	late.Retire = &store.ServerRetire{Phase: store.RetirePhaseRemoving, StartedAt: time.Now()}
	if err := st.UpdateServer(ctx, late); err != nil {
		t.Fatal(err)
	}

	srv.ReconcileOnceForTest(ctx)

	e := getRetireView(t, h, token, early.ID)
	if e.State != "offline" || e.Retire != nil || !strings.Contains(e.RetireNote, "restarted while this retire was backing up") {
		t.Fatalf("early orphan = %+v, want it abandoned and saying why", e)
	}
	l := getRetireView(t, h, token, late.ID)
	if l.State != "retired" || !strings.Contains(l.RetireNote, "queued") {
		t.Fatalf("late orphan = %+v, want it retired with its removal queued", l)
	}
	owed := pendingRemovals(t, st, nodeID)
	if len(owed) != 1 || owed[0].ServerID != late.ID || owed[0].MemoryMB != 1024 || len(owed[0].Ports) != 1 || owed[0].Ports[0] != 27015 {
		t.Fatalf("pending = %+v, want the late orphan's removal holding its 1024 MB and port 27015", owed)
	}
}
