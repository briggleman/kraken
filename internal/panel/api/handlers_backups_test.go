package api_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// backupRecorder is the fake agent's NodeService with CreateBackup tapped, so a
// test can assert on the globs the PANEL resolved — that resolution is the whole
// of #218 on this side of the wire.
type backupRecorder struct {
	agentpb.NodeServiceServer
	mu  sync.Mutex
	req *agentpb.CreateBackupRequest
}

func (r *backupRecorder) CreateBackup(ctx context.Context, req *agentpb.CreateBackupRequest) (*agentpb.BackupInfo, error) {
	r.mu.Lock()
	r.req = req
	r.mu.Unlock()
	return r.NodeServiceServer.CreateBackup(ctx, req)
}

func (r *backupRecorder) last(t *testing.T) *agentpb.CreateBackupRequest {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.req == nil {
		t.Fatal("the agent never received a CreateBackup request")
	}
	return r.req
}

// startRecordingAgent is startFakeAgent with the CreateBackup tap installed.
func startRecordingAgent(t *testing.T, nodeID string) (string, *backupRecorder) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	rec := &backupRecorder{NodeServiceServer: agent.NewService(agent.NewFakeRuntime(nodeID, "linux", true, "test"))}
	srv := grpc.NewServer()
	agentpb.RegisterNodeServiceServer(srv, rec)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String(), rec
}

// createSpecWithBackup posts a spec carrying the given backup block (pass nil
// for a spec that declares none).
func createSpecWithBackup(t *testing.T, h http.Handler, token, slug string, backup map[string]any) string {
	t.Helper()
	body := map[string]any{
		"name": "Backup Spec " + slug, "slug": slug,
		"steam_app_ids": map[string]int{"linux": 730},
		"platforms":     []map[string]string{{"kind": "linux-native", "image": "registry/kraken/steam-base:latest"}},
		"install":       map[string]any{"script": "true"},
		"startup": map[string]any{
			"command": "./run -port {{PORT_GAME}}",
			"stop":    map[string]string{"type": "signal", "value": "SIGINT"},
		},
		"ports":     []map[string]any{{"name": "game", "protocol": "udp", "default": 27015, "required": true}},
		"resources": map[string]int{"min_memory_mb": 1024},
	}
	if backup != nil {
		body["backup"] = backup
	}
	rec := do(t, h, http.MethodPost, "/api/v1/specs", token, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create spec %s: status %d, body %s", slug, rec.Code, rec.Body.String())
	}
	var sp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sp)
	if sp.ID == "" {
		t.Fatalf("create spec %s: empty id", slug)
	}
	return sp.ID
}

func createServerFromSpec(t *testing.T, h http.Handler, token, specID, name string) string {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{"spec_id": specID, "name": name})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create server %s: status %d, body %s", name, rec.Code, rec.Body.String())
	}
	var sv struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sv)
	if sv.ID == "" {
		t.Fatalf("create server %s: empty id", name)
	}
	return sv.ID
}

// The manual backup path must carry the spec's own globs to the agent. Without
// them the agent tars the whole install tree, which is the bug #218 exists for.
func TestCreateBackupCarriesTheSpecsGlobs(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, rec := startRecordingAgent(t, "node-backup-globs")
	nodeID := registerNode(t, h, token, addr)
	pollNode(t, h, token, nodeID)

	specID := createSpecWithBackup(t, h, token, "globbed", map[string]any{
		"include": []string{"Pal/Saved/**"},
		"exclude": []string{"Pal/Saved/Logs/**"},
	})
	serverID := createServerFromSpec(t, h, token, specID, "globbed-01")

	res := do(t, h, http.MethodPost, "/api/v1/servers/"+serverID+"/backups", token, map[string]any{"name": "manual"})
	if res.Code != http.StatusAccepted {
		t.Fatalf("create backup: status %d, body %s", res.Code, res.Body.String())
	}
	got := rec.last(t)
	if !slices.Equal(got.BackupInclude, []string{"Pal/Saved/**"}) {
		t.Errorf("backup_include = %v, want [Pal/Saved/**]", got.BackupInclude)
	}
	if !slices.Equal(got.BackupExclude, []string{"Pal/Saved/Logs/**"}) {
		t.Errorf("backup_exclude = %v, want [Pal/Saved/Logs/**]", got.BackupExclude)
	}
	// The slug still rides along — it expands the node's backup-path tokens.
	if got.Slug != "globbed" {
		t.Errorf("slug = %q, want globbed", got.Slug)
	}
}

// A spec with NO backup block falls back to the built-in policy: no include list
// (so nothing can be dropped by a wrong guess) plus ephemeral-only excludes.
func TestCreateBackupFallsBackToBuiltinExcludes(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, rec := startRecordingAgent(t, "node-backup-builtin")
	nodeID := registerNode(t, h, token, addr)
	pollNode(t, h, token, nodeID)

	specID := createSpecWithBackup(t, h, token, "unglobbed", nil)
	serverID := createServerFromSpec(t, h, token, specID, "unglobbed-01")

	res := do(t, h, http.MethodPost, "/api/v1/servers/"+serverID+"/backups", token, nil)
	if res.Code != http.StatusAccepted {
		t.Fatalf("create backup: status %d, body %s", res.Code, res.Body.String())
	}
	got := rec.last(t)
	if len(got.BackupInclude) != 0 {
		t.Errorf("backup_include = %v, want empty for a spec with no block", got.BackupInclude)
	}
	if len(got.BackupExclude) == 0 {
		t.Fatal("backup_exclude is empty; the built-in ephemeral excludes should apply")
	}
	// Spot-check the entries the enshrouded-01 failures turned on.
	for _, want := range []string{"**/*.log", "steamapps/downloading/**"} {
		if !slices.Contains(got.BackupExclude, want) {
			t.Errorf("built-in exclude %q missing from %v", want, got.BackupExclude)
		}
	}
}

// A restore is unaffected by any of this: an archive only contains what was
// included, so the request carries no globs to get out of sync with.
func TestRestoreBackupCarriesNoGlobs(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rec := startRecordingAgent(t, "node-backup-restore")
	nodeID := registerNode(t, h, token, addr)
	pollNode(t, h, token, nodeID)

	specID := createSpecWithBackup(t, h, token, "restorable", map[string]any{"include": []string{"save/**"}})
	serverID := createServerFromSpec(t, h, token, specID, "restorable-01")
	// Restore is stop-guarded. The async install lands this server on offline
	// anyway, but not necessarily before the request below.
	stopServer(t, st, serverID)

	res := do(t, h, http.MethodPost, "/api/v1/servers/"+serverID+"/backups", token, map[string]any{"name": "snap"})
	if res.Code != http.StatusAccepted {
		t.Fatalf("create backup: status %d, body %s", res.Code, res.Body.String())
	}
	var view struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &view)
	if rr := do(t, h, http.MethodPost, "/api/v1/servers/"+serverID+"/backups/"+view.ID+"/restore", token, nil); rr.Code != http.StatusOK {
		t.Fatalf("restore: status %d, body %s", rr.Code, rr.Body.String())
	}
	if got := rec.last(t); !slices.Equal(got.BackupInclude, []string{"save/**"}) {
		t.Errorf("the create request should still be the last one recorded, got %v", got.BackupInclude)
	}
}

// stopServer forces a server to the offline state, standing in for the operator
// stopping it before a restore.
func stopServer(t *testing.T, st *memory.Store, serverID string) {
	t.Helper()
	sv, err := st.GetServer(context.Background(), serverID)
	if err != nil {
		t.Fatalf("load server %s: %v", serverID, err)
	}
	sv.State = store.StateOffline
	if err := st.UpdateServer(context.Background(), sv); err != nil {
		t.Fatalf("stop server %s: %v", serverID, err)
	}
}

// A restore replaces exactly the files a running game holds open — on Windows
// the swap hits a sharing violation partway, and on Linux the running process
// saves over what was just restored. The Panel is the only side that can see
// the state, so it refuses the restore rather than handing the Agent a job that
// corrupts the save.
func TestRestoreBackupRequiresAStoppedServer(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr := startFakeAgent(t, "node-restore-guard")
	nodeID := registerNode(t, h, token, addr)

	cases := []struct {
		state store.ServerState
		want  int
	}{
		{store.StateRunning, http.StatusConflict},
		{store.StateStarting, http.StatusConflict},
		{store.StateStopping, http.StatusConflict},
		{store.StateInstalling, http.StatusConflict},
		{store.StateOffline, http.StatusOK},
		{store.StateCrashed, http.StatusOK},
		{store.StateInstallFailed, http.StatusOK},
	}
	for _, tc := range cases {
		id := "sv-" + string(tc.state)
		sv := &store.Server{ID: id, Name: id, NodeID: nodeID, State: tc.state, CreatedAt: time.Now()}
		if err := st.CreateServer(context.Background(), sv); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		rec := do(t, h, http.MethodPost, "/api/v1/servers/"+id+"/backups/1700000000000__snap/restore", token, nil)
		if rec.Code != tc.want {
			t.Errorf("restore from %s: got %d, want %d; body: %s", tc.state, rec.Code, tc.want, rec.Body.String())
			continue
		}
		if tc.want == http.StatusConflict && !strings.Contains(rec.Body.String(), "stop the server") {
			t.Errorf("restore from %s should say to stop the server; body: %s", tc.state, rec.Body.String())
		}
	}
}
