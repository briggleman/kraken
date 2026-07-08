package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
)

// TestPower_StartRejectedOnInstallFailed guards the fix for the bug where
// POST /servers/{id}/power {"action":"start"} would boot the runtime
// container against empty /data when install had failed, producing
// a misleading crash-loop error trail.
func TestPower_StartRejectedOnInstallFailed(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr := startFakeAgent(t, "node-x")
	nodeID := registerNode(t, h, token, addr)

	sv := &store.Server{
		ID: "sv-fail", Name: "sv-fail", NodeID: nodeID,
		State: store.StateInstallFailed, CreatedAt: time.Now(),
	}
	if err := st.CreateServer(context.Background(), sv); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, action := range []string{"start", "restart"} {
		rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
			map[string]string{"action": action})
		if rec.Code != http.StatusConflict {
			t.Errorf("%s on install_failed: got %d, want 409; body: %s", action, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "reinstall") {
			t.Errorf("%s error should mention reinstall path; body: %s", action, rec.Body.String())
		}
	}
}

// TestPower_StartRejectedWhileInstalling — the install goroutine hasn't
// finished yet, so racing a start onto an installing server would corrupt
// the install by having two competing container flows.
func TestPower_StartRejectedWhileInstalling(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr := startFakeAgent(t, "node-x")
	nodeID := registerNode(t, h, token, addr)

	sv := &store.Server{
		ID: "sv-installing", Name: "sv-installing", NodeID: nodeID,
		State: store.StateInstalling, CreatedAt: time.Now(),
	}
	if err := st.CreateServer(context.Background(), sv); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "start"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("start while installing: got %d, want 409; body: %s", rec.Code, rec.Body.String())
	}
}

// TestReinstall_RejectedFromWrongState — reinstall is scoped to recovery
// from a failed install. On a fully installed server (offline / running
// / crashed), the user should Delete + Recreate instead.
func TestReinstall_RejectedFromWrongState(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr := startFakeAgent(t, "node-x")
	nodeID := registerNode(t, h, token, addr)

	sv := &store.Server{
		ID: "sv-offline", Name: "sv-offline", NodeID: nodeID,
		State: store.StateOffline, CreatedAt: time.Now(),
	}
	if err := st.CreateServer(context.Background(), sv); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/reinstall", token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("reinstall from offline: got %d, want 409; body: %s", rec.Code, rec.Body.String())
	}
}

// TestReinstall_FromInstallFailedAcceptsAndFlipsState — an install-failed
// server accepts a POST /reinstall, immediately flips to installing (so a
// concurrent start still sees the guard), and the async provision goroutine
// fires. We don't wait for provision to complete — the state flip + 202
// response is the surface this handler owns.
func TestReinstall_FromInstallFailedAcceptsAndFlipsState(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr := startFakeAgent(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpec(t, h, token, "reinstall-target")

	sv := &store.Server{
		ID: "sv-retry", Name: "sv-retry", NodeID: nodeID, SpecID: specID,
		State: store.StateInstallFailed, CreatedAt: time.Now(),
	}
	if err := st.CreateServer(context.Background(), sv); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/reinstall", token, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("reinstall: got %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.State != "installing" {
		t.Fatalf("reinstall response state: got %q, want installing", resp.State)
	}
	// A racing start now sees the installing state and gets 409, not the
	// stale install_failed message.
	pw := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "start"})
	if pw.Code != http.StatusConflict {
		t.Errorf("start during reinstall: got %d, want 409", pw.Code)
	}
}
