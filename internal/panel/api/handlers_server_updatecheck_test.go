package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// updateView is the `update` block a server carries (#392).
type updateView struct {
	Status           string     `json:"status"`
	InstalledBuild   string     `json:"installed_build"`
	AvailableBuild   string     `json:"available_build"`
	AvailableBuildAt *time.Time `json:"available_build_at"`
	CheckedAt        *time.Time `json:"checked_at"`
	Error            string     `json:"error"`
}

func getUpdate(t *testing.T, h http.Handler, token, id string) updateView {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/v1/servers/"+id, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get server: status %d, body %s", rec.Code, rec.Body.String())
	}
	var sv struct {
		Update *updateView `json:"update"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sv); err != nil {
		t.Fatalf("decode server: %v", err)
	}
	if sv.Update == nil {
		t.Fatalf("server has no update block: %s", rec.Body.String())
	}
	return *sv.Update
}

func postUpdateCheck(t *testing.T, h http.Handler, token, id string) updateView {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+id+"/update-check", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("update-check: status %d, body %s", rec.Code, rec.Body.String())
	}
	var u updateView
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatalf("decode update-check: %v", err)
	}
	return u
}

// buildCheckNode is a fake Agent whose SteamCMD reports build 100 for the
// test specs' app (730), registered and probed online.
func buildCheckNode(t *testing.T, h http.Handler, token, name string) (string, *agent.FakeRuntime) {
	t.Helper()
	addr, rt := startFakeAgentRuntime(t, name,
		agent.WithFakeAppBuilds(map[string]agent.FakeAppBuild{"730": {BuildID: "100", TimeUpdated: 1759921187}}))
	nodeID := registerNode(t, h, token, addr)
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
	return nodeID, rt
}

// The whole round of #392's Panel half against one server: the create's
// install records the build it pulled (no check needed — it is current by
// definition), an on-demand check confirms it, and Steam shipping a build
// turns it into `available` with both build ids — on the get, the list and
// the check's own answer. Both POSTs are audited.
func TestUpdateCheck_CreateIsCurrentThenSteamShipsABuild(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	_, rt := buildCheckNode(t, h, token, "node-bc")
	specID := createSpec(t, h, token, "build-check")

	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{"spec_id": specID, "name": "bc-1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create server: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	waitForState(t, h, token, created.ID, "offline")

	// The install recorded its build before the row left `installing`.
	u := getUpdate(t, h, token, created.ID)
	if u.Status != "current" || u.InstalledBuild != "100" || u.AvailableBuild != "100" || u.CheckedAt == nil || u.Error != "" {
		t.Fatalf("after the create's install: %+v", u)
	}
	found := false
	for _, l := range getInstallLog(t, h, token, created.ID).Lines {
		if l.Text == "[panel] installed build 100" && l.Stream == "system" {
			found = true
		}
	}
	if !found {
		t.Error("the install console should say which build the install left")
	}

	u = postUpdateCheck(t, h, token, created.ID)
	if u.Status != "current" || u.InstalledBuild != "100" || u.AvailableBuild != "100" {
		t.Fatalf("on-demand check of a fresh install: %+v", u)
	}
	if u.AvailableBuildAt == nil || u.AvailableBuildAt.Unix() != 1759921187 {
		t.Fatalf("available_build_at should be Steam's timeupdated: %+v", u.AvailableBuildAt)
	}

	rt.SetAppBuild("730", agent.FakeAppBuild{BuildID: "101"})
	u = postUpdateCheck(t, h, token, created.ID)
	if u.Status != "available" || u.InstalledBuild != "100" || u.AvailableBuild != "101" {
		t.Fatalf("after Steam shipped 101: %+v", u)
	}
	if got := getUpdate(t, h, token, created.ID); got.Status != "available" || got.AvailableBuild != "101" {
		t.Fatalf("GET after the check: %+v", got)
	}
	rec = do(t, h, http.MethodGet, "/api/v1/servers", token, nil)
	var list struct {
		Servers []struct {
			ID     string     `json:"id"`
			Update updateView `json:"update"`
		} `json:"servers"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Servers) != 1 || list.Servers[0].Update.Status != "available" {
		t.Fatalf("the list should carry the check too: %s", rec.Body.String())
	}

	// Fleet check: 202, and audited like the single one.
	if rec := do(t, h, http.MethodPost, "/api/v1/servers/update-check", token, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("fleet update-check: status %d, body %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v1/audit", token, nil)
	for _, action := range []string{"POST /servers/{id}/update-check", "POST /servers/update-check"} {
		if !strings.Contains(rec.Body.String(), action) {
			t.Errorf("audit log has no %q entry", action)
		}
	}
}

// An Agent that predates GetAppBuilds answers Unimplemented. The check reads
// unknown and says why — never current, which would let the start path skip
// an update it cannot vouch for.
func TestUpdateCheck_OldAgentIsUnknown(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	nodeID, rt := buildCheckNode(t, h, token, "node-old")
	specID := createSpec(t, h, token, "build-check-old")
	sv := seedOfflineServer(t, st, "sv-old", nodeID, specID, nil)
	rt.SetAppBuildsError(grpcstatus.Error(codes.Unimplemented, "unknown method GetAppBuilds for service kraken.agent.v1.NodeService"))

	u := postUpdateCheck(t, h, token, sv.ID)
	if u.Status != "unknown" || !strings.Contains(u.Error, "predates the build check") {
		t.Fatalf("old agent: %+v", u)
	}
	if got := getUpdate(t, h, token, sv.ID); got.Status != "unknown" || got.Error != u.Error {
		t.Fatalf("the failed check should be on the row: %+v", got)
	}
}

// A spec with no build to check — Factorio's shape: an install that never
// runs app_update — reads unsupported, from the get and the check alike.
func TestUpdateCheck_NonSteamSpecIsUnsupported(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	nodeID, _ := buildCheckNode(t, h, token, "node-ns")
	specID := createSpecWithInstall(t, h, token, "not-steam", map[string]any{
		"script": "curl -sSL https://factorio.com/get-download/stable/headless/linux64 -o /tmp/f.tar.xz",
	})
	sv := seedOfflineServer(t, st, "sv-ns", nodeID, specID, nil)

	if u := getUpdate(t, h, token, sv.ID); u.Status != "unsupported" {
		t.Fatalf("GET: %+v", u)
	}
	if u := postUpdateCheck(t, h, token, sv.ID); u.Status != "unsupported" || u.CheckedAt != nil {
		t.Fatalf("check: %+v", u)
	}
}

// A server that is installing is refused: its manifest may be half-written,
// and the install records its own build when it lands.
func TestUpdateCheck_RefusesAnInstallingServer(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	nodeID, _ := buildCheckNode(t, h, token, "node-busy")
	specID := createSpec(t, h, token, "build-check-busy")
	sv := seedOfflineServer(t, st, "sv-busy", nodeID, specID, func(s *store.Server) { s.State = store.StateInstalling })

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/update-check", token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("installing: status %d, want 409; body %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/servers/missing/update-check", token, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing server: status %d, want 404", rec.Code)
	}
}

// The update pass a start runs records the build it pulled, so a server that
// was behind reads current once it is running again.
func TestUpdateCheck_UpdateOnStartRecordsTheBuild(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	nodeID, rt := buildCheckNode(t, h, token, "node-uos")
	specID := createSpecWithInstall(t, h, token, "build-check-uos", map[string]any{
		"script": "steamcmd +login anonymous +app_update {{APP_ID}} validate +quit",
	})
	sv := seedOfflineServer(t, st, "sv-uos", nodeID, specID, nil)
	// The last check found the tree a build behind.
	at := time.Now().Add(-time.Hour)
	if err := st.UpdateServerBuild(context.Background(), sv.ID, store.ServerBuild{InstalledBuild: "99", AvailableBuild: "100", CheckedAt: &at}); err != nil {
		t.Fatal(err)
	}
	if u := getUpdate(t, h, token, sv.ID); u.Status != "available" {
		t.Fatalf("before the start: %+v", u)
	}
	rt.SetAppBuild("730", agent.FakeAppBuild{BuildID: "102"})

	if rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token, map[string]string{"action": "start"}); rec.Code != http.StatusAccepted {
		t.Fatalf("start: status %d, body %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "running")
	u := getUpdate(t, h, token, sv.ID)
	if u.Status != "current" || u.InstalledBuild != "102" || u.AvailableBuild != "102" || u.CheckedAt == nil || !u.CheckedAt.After(at) {
		t.Fatalf("after the update pass: %+v", u)
	}
}

// The fleet check runs in the background and lands on every checkable row;
// it is not for a caller who may only see their own servers.
func TestUpdateCheck_FleetPass(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	nodeID, rt := buildCheckNode(t, h, token, "node-fleet")
	specID := createSpecWithInstall(t, h, token, "build-check-fleet", map[string]any{
		"script": "steamcmd +login anonymous +app_update {{APP_ID}} validate +quit",
	})
	ctx := context.Background()
	for _, id := range []string{"fleet-a", "fleet-b"} {
		seedOfflineServer(t, st, id, nodeID, specID, nil)
		// The fake install is what writes the manifest a check reads.
		if err := rt.Create(ctx, &agentpb.ServerSpec{ServerId: id}); err != nil {
			t.Fatal(err)
		}
		if err := rt.Install(ctx, &agentpb.InstallServerRequest{ServerId: id, InstallScript: "steamcmd", Env: map[string]string{"APP_ID": "730"}},
			func(*agentpb.InstallEvent) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}

	rec := do(t, h, http.MethodPost, "/api/v1/servers/update-check", token, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("fleet: status %d, body %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, id := range []string{"fleet-a", "fleet-b"} {
		for {
			u := getUpdate(t, h, token, id)
			if u.Status == "current" && u.InstalledBuild == "100" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never checked: %+v", id, u)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	// An operator holds server.power but not server.any.
	if err := st.CreateUser(ctx, &store.User{ID: "ola", Username: "ola", RoleID: rbac.RoleOperator, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, &store.Session{Token: "ola-token", UserID: "ola", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/servers/update-check", "ola-token", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("operator fleet check: status %d, want 403", rec.Code)
	}
}
