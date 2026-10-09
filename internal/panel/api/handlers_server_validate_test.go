package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/agent"
)

// validateScript is a two-pass install script written the way the bundled
// Steam specs are since #392: the pass decides whether `validate` is on the
// command line.
const validateScript = "steamcmd +login anonymous +app_update {{APP_ID}} {{VALIDATE}} +quit;\n" +
	"steamcmd +login anonymous +app_update {{APP_ID}} {{VALIDATE}} +quit"

const (
	// withValidate is one pass of validateScript as a create, a revive or a
	// reinstall renders it.
	withValidate = "+app_update 730 validate +quit"
	// withoutValidate is the same pass on update-on-start: the placeholder
	// renders as nothing and leaves its double space.
	withoutValidate = "+app_update 730  +quit"
)

// assertRendered checks one install pass's script for the expected render of
// both app_update passes, and that no placeholder reached the Agent.
func assertRendered(t *testing.T, what, script, want string) {
	t.Helper()
	if n := strings.Count(script, want); n != 2 {
		t.Errorf("%s: %q appears %d times, want 2 (both passes): %q", what, want, n, script)
	}
	if strings.Contains(script, "{{") {
		t.Errorf("%s: a placeholder reached the Agent unrendered: %q", what, script)
	}
}

// TestValidatePlaceholder_CreateAndReinstallValidate — the two passes the
// operator asks for by name both render {{VALIDATE}} as validate: a create has
// nothing to re-hash yet, and a reinstall is asking for the tree to be made
// right.
func TestValidatePlaceholder_CreateAndReinstallValidate(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-validate",
		agent.WithFakeAppBuilds(map[string]agent.FakeAppBuild{"730": {BuildID: "99"}}))
	liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "validate-create", map[string]any{"script": validateScript})

	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{"spec_id": specID, "name": "validate-01"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("create decode: %v (%s)", err, rec.Body.String())
	}
	waitForState(t, h, token, created.ID, "offline")

	if rec := do(t, h, http.MethodPost, "/api/v1/servers/"+created.ID+"/reinstall", token, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("reinstall: status %d, body %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, created.ID, "offline")

	scripts := rt.InstallScripts(created.ID)
	if len(scripts) != 2 {
		t.Fatalf("install passes: got %d, want 2 (create, reinstall): %q", len(scripts), scripts)
	}
	assertRendered(t, "create", scripts[0], withValidate)
	assertRendered(t, "reinstall", scripts[1], withValidate)

	// The pseudo-variable is the render's alone: it never lands on the row.
	var got struct {
		Vars map[string]string `json:"vars"`
	}
	_ = json.Unmarshal(do(t, h, http.MethodGet, "/api/v1/servers/"+created.ID, token, nil).Body.Bytes(), &got)
	if _, ok := got.Vars["VALIDATE"]; ok {
		t.Errorf("VALIDATE was stored in the server's vars: %v", got.Vars)
	}
}

// TestValidatePlaceholder_ReviveValidates — a revive provisions like a create,
// so its install validates too.
func TestValidatePlaceholder_ReviveValidates(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt, _ := startStoppableAgent(t, "node-validate-revive")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "validate-revive", map[string]any{"script": validateScript})
	sv := placedServer(t, st, "sv-validate-revive", nodeID, specID)
	retireServer(t, srv, token, sv.ID)

	if rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/revive", token, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("revive: status %d, body %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "offline")
	waitOpClear(t, srv, sv.ID)

	scripts := rt.InstallScripts(sv.ID)
	if len(scripts) != 1 {
		t.Fatalf("install passes: got %d, want 1 (the revive's): %q", len(scripts), scripts)
	}
	assertRendered(t, "revive", scripts[0], withValidate)
}

// TestValidatePlaceholder_UpdateOnStartSkipsValidate — the pass a start runs
// when Steam has a newer build renders {{VALIDATE}} as nothing: the tree was
// good at the last start, so the differential download is all it needs. The
// same spec's create renders validate (above); a spec without the placeholder
// keeps its literal validate on this pass (TestPower_StartRunsUpdatePassByDefault).
func TestValidatePlaceholder_UpdateOnStartSkipsValidate(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-validate-update",
		agent.WithFakeAppBuilds(map[string]agent.FakeAppBuild{"730": {BuildID: "99"}}))
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "validate-update", map[string]any{"script": validateScript})
	sv := seedOfflineServer(t, st, "sv-validate-update", nodeID, specID, nil)
	installOnFake(t, rt, sv.ID, "730") // build 99 on disk
	rt.SetAppBuild("730", agent.FakeAppBuild{BuildID: "100"})
	before := len(rt.InstallScripts(sv.ID))

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token, map[string]string{"action": "start"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: got %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "running")

	scripts := rt.InstallScripts(sv.ID)[before:]
	if len(scripts) != 1 {
		t.Fatalf("install passes: got %d, want 1 (the update pass): %q", len(scripts), scripts)
	}
	assertRendered(t, "update-on-start", scripts[0], withoutValidate)
	if strings.Contains(scripts[0], "validate") {
		t.Errorf("the update-on-start pass must not validate: %q", scripts[0])
	}
}

// TestValidatePlaceholder_ReservedName — nothing can shadow the pseudo-variable:
// a spec declaring a VALIDATE variable is refused, and so is VALIDATE as a
// server's variable override, at create and on the settings edit.
func TestValidatePlaceholder_ReservedName(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)

	rec := do(t, h, http.MethodPost, "/api/v1/specs", token, map[string]any{
		"name": "Shadow", "slug": "validate-shadow",
		"steam_app_ids": map[string]int{"linux": 730},
		"platforms":     []map[string]string{{"kind": "linux-native", "image": "registry/kraken/steam-base:latest"}},
		"install":       map[string]any{"script": validateScript},
		"startup": map[string]any{
			"command": "./srv -port {{PORT_GAME}}",
			"stop":    map[string]string{"type": "signal", "value": "SIGINT"},
		},
		"variables": []map[string]any{{"key": "VALIDATE", "default": "validate", "user_editable": true}},
		"ports":     []map[string]any{{"name": "game", "protocol": "udp", "default": 27015, "required": true}},
		"resources": map[string]int{"min_memory_mb": 1024},
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "reserved") {
		t.Errorf("spec with a VALIDATE variable: status %d, body %s; want 400 naming it reserved", rec.Code, rec.Body.String())
	}

	addr, _ := startFakeAgentRuntime(t, "node-validate-reserved")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "validate-reserved", map[string]any{"script": validateScript})

	rec = do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"spec_id": specID, "name": "validate-override", "variables": map[string]string{"VALIDATE": ""},
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "reserved") {
		t.Errorf("create with a VALIDATE override: status %d, body %s; want 400 naming it reserved", rec.Code, rec.Body.String())
	}

	sv := seedOfflineServer(t, st, "sv-validate-reserved", nodeID, specID, nil)
	rec = do(t, h, http.MethodPut, "/api/v1/servers/"+sv.ID+"/settings", token, map[string]any{
		"variables": map[string]string{"VALIDATE": ""},
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "reserved") {
		t.Errorf("settings edit of VALIDATE: status %d, body %s; want 400 naming it reserved", rec.Code, rec.Body.String())
	}
}
