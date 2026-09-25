package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
)

// #387: every path that starts an install attempt rotates the install log
// BEFORE the store write that makes the row `installing`. Reading the log after
// the 202 cannot tell that apart from rotating after the write; these read it
// from inside that write, through flakyStore's onUpdateServer hook, which runs
// once the row is stored and before the handler takes its next step.

// readInstallLogInHook is getInstallLog without t.Fatal, for use inside a
// store hook.
func readInstallLogInHook(h http.Handler, token, id string) (installLogBody, int) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/servers/"+id+"/install-log", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body installLogBody
	if rec.Code == http.StatusOK {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return body, rec.Code
}

// probeInstallLogAtInstallingWrite arranges for the install log to be read the
// first time id's row is written as `installing` from now on, and returns what
// that read saw and whether it happened.
func probeInstallLogAtInstallingWrite(t *testing.T, st *flakyStore, h http.Handler, token, id string) func() (installLogBody, bool) {
	t.Helper()
	var fired atomic.Bool
	var seen atomic.Pointer[installLogBody]
	hook := func(row *store.Server) {
		if row.ID != id || row.State != store.StateInstalling || !fired.CompareAndSwap(false, true) {
			return
		}
		body, code := readInstallLogInHook(h, token, id)
		if code != http.StatusOK {
			t.Errorf("install-log read inside the installing write: status %d", code)
			return
		}
		seen.Store(&body)
	}
	st.onUpdateServer.Store(&hook)
	t.Cleanup(func() { st.onUpdateServer.Store(nil) })
	return func() (installLogBody, bool) {
		if p := seen.Load(); p != nil {
			return *p, true
		}
		return installLogBody{}, false
	}
}

// createInstalledServer creates a server through the API and waits for its
// first install to settle, returning its id and that attempt's log.
func createInstalledServer(t *testing.T, h http.Handler, token, specID, name string) (string, installLogBody) {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{"spec_id": specID, "name": name})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create server: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if getServerState(t, h, token, created.ID) == "offline" {
			if body := getInstallLog(t, h, token, created.ID); body.Done {
				return created.ID, body
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("first install never settled (state %q)", getServerState(t, h, token, created.ID))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// assertRotatedOver checks a read taken inside the installing write: the new
// attempt is current and empty, and the attempt it replaced is previous.
func assertRotatedOver(t *testing.T, got installLogBody, first installLogBody) {
	t.Helper()
	if got.Previous == nil {
		t.Fatal("previous is null inside the write that made the row installing: the log was not rotated before it")
	}
	if got.Previous.StartedMs != first.StartedMs || got.Previous.FinishedMs != first.FinishedMs {
		t.Errorf("previous bracket %d–%d, want the replaced attempt's %d–%d",
			got.Previous.StartedMs, got.Previous.FinishedMs, first.StartedMs, first.FinishedMs)
	}
	if len(got.Lines) != 0 || got.Done || got.FinishedMs != 0 {
		t.Errorf("current attempt inside the write: done=%v finished_ms=%d lines=%+v, want the new, empty one",
			got.Done, got.FinishedMs, got.Lines)
	}
	if got.StartedMs == 0 || got.StartedMs < first.FinishedMs {
		t.Errorf("current attempt started_ms = %d, want set and not before the replaced one finished (%d)",
			got.StartedMs, first.FinishedMs)
	}
}

func TestInstallLog_ReinstallRotatesBeforeTheInstallingWrite(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	liveNode(t, h, token, startFakeAgent(t, "node-rotate-reinstall"))
	specID := createSpecWithInstall(t, h, token, "rotate-reinstall", map[string]any{"script": "install.sh"})
	id, first := createInstalledServer(t, h, token, specID, "rotate-reinstall-01")

	seen := probeInstallLogAtInstallingWrite(t, st, h, token, id)
	if rec := do(t, h, http.MethodPost, "/api/v1/servers/"+id+"/reinstall", token, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("reinstall: status %d, body %s", rec.Code, rec.Body.String())
	}
	got, ok := seen()
	if !ok {
		t.Fatal("the reinstall never wrote the row as installing")
	}
	assertRotatedOver(t, got, first)
	waitForState(t, h, token, id, "offline")
}

// The update-on-start path is the one the provision hook cannot gate: the pass
// runs in updateThenStart, not provision.
func TestInstallLog_UpdateOnStartRotatesBeforeTheInstallingWrite(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	liveNode(t, h, token, startFakeAgent(t, "node-rotate-start"))
	specID := createSpecWithInstall(t, h, token, "rotate-start", map[string]any{"script": "install.sh"})
	id, first := createInstalledServer(t, h, token, specID, "rotate-start-01")
	// Out of the fresh-install window, so the start runs the update pass.
	sv, err := st.Store.GetServer(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	sv.ProvisionedAt = nil
	if err := st.Store.UpdateServer(ctx, sv); err != nil {
		t.Fatal(err)
	}

	seen := probeInstallLogAtInstallingWrite(t, st, h, token, id)
	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+id+"/power", token, map[string]string{"action": "start"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: status %d, want 202 (the update pass); body %s", rec.Code, rec.Body.String())
	}
	got, ok := seen()
	if !ok {
		t.Fatal("the start never wrote the row as installing")
	}
	assertRotatedOver(t, got, first)
	waitForState(t, h, token, id, "running")
}

// A retire drops the log, so a revive has no attempt to keep as previous; what
// the read inside the write must show is the revive's own attempt, already
// open, rather than nothing at all.
func TestInstallLog_ReviveOpensItsAttemptBeforeTheInstallingWrite(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	nodeID := liveNode(t, h, token, startFakeAgent(t, "node-rotate-revive"))
	specID := createSpecWithInstall(t, h, token, "rotate-revive", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-rotate-revive", nodeID, specID)
	retireServer(t, srv, token, sv.ID)

	seen := probeInstallLogAtInstallingWrite(t, st, h, token, sv.ID)
	if rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/revive", token, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("revive: status %d, body %s", rec.Code, rec.Body.String())
	}
	got, ok := seen()
	if !ok {
		t.Fatal("the revive never wrote the row as installing")
	}
	if !got.Retained || got.StartedMs == 0 {
		t.Fatalf("inside the revive's installing write: retained=%v started_ms=%d, want its attempt already open",
			got.Retained, got.StartedMs)
	}
	if len(got.Lines) != 0 || got.Done || got.Previous != nil {
		t.Errorf("inside the revive's installing write: done=%v lines=%+v previous=%+v, want a new, empty attempt with no previous",
			got.Done, got.Lines, got.Previous)
	}
	waitForState(t, h, token, sv.ID, "offline")
	waitOpClear(t, srv, sv.ID)
}

// A revive with start takes the update-on-start branch whenever its install
// left the row unstamped (settings edited during the install) or a long
// restore outlived the fresh-install window. The hook stands in for both: when
// the revive's install writes the row offline, it clears provisioned_at, so
// startAfterRevive runs the update pass. Its installing write must find the
// log already rotated: the revive's attempt as previous, the new one empty.
func TestInstallLog_StartAfterReviveRotatesBeforeTheInstallingWrite(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	nodeID := liveNode(t, h, token, startFakeAgent(t, "node-rotate-revive-start"))
	specID := createSpecWithInstall(t, h, token, "rotate-revive-start", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-rotate-revive-start", nodeID, specID)
	retireServer(t, srv, token, sv.ID)

	var installingWrites atomic.Int32
	var unstamped atomic.Bool
	var reviveAttempt, atUpdate atomic.Pointer[installLogBody]
	hook := func(row *store.Server) {
		if row.ID != sv.ID {
			return
		}
		switch {
		case row.State == store.StateInstalling:
			n := installingWrites.Add(1)
			if n > 2 {
				return
			}
			body, code := readInstallLogInHook(h, token, sv.ID)
			if code != http.StatusOK {
				t.Errorf("install-log read inside installing write %d: status %d", n, code)
				return
			}
			if n == 1 {
				reviveAttempt.Store(&body)
			} else {
				atUpdate.Store(&body)
			}
		case row.State == store.StateOffline && row.ProvisionedAt != nil && installingWrites.Load() == 1 &&
			unstamped.CompareAndSwap(false, true):
			// The revive's install landing: leave the row unstamped.
			fresh, err := st.Store.GetServer(ctx, sv.ID)
			if err != nil {
				t.Errorf("reload after the revive's install: %v", err)
				return
			}
			fresh.ProvisionedAt = nil
			if err := st.Store.UpdateServer(ctx, fresh); err != nil {
				t.Errorf("clear provisioned_at: %v", err)
			}
		}
	}
	st.onUpdateServer.Store(&hook)
	t.Cleanup(func() { st.onUpdateServer.Store(nil) })

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/revive", token, map[string]any{"start": true})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("revive: status %d, body %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "running")
	waitOpClear(t, srv, sv.ID)

	if !unstamped.Load() {
		t.Fatal("the revive's install never wrote the row offline with provisioned_at set")
	}
	if n := installingWrites.Load(); n != 2 {
		t.Fatalf("%d installing writes, want 2 (the revive, then the start's update pass)", n)
	}
	first, got := reviveAttempt.Load(), atUpdate.Load()
	if first == nil || got == nil {
		t.Fatal("an install-log read inside an installing write did not happen")
	}
	if got.Previous == nil {
		t.Fatal("previous is null inside the start's installing write: the log was not rotated before it")
	}
	if got.Previous.StartedMs != first.StartedMs || !got.Previous.Done || len(got.Previous.Lines) == 0 {
		t.Errorf("previous = started %d done=%v %d lines, want the revive's finished attempt (started %d)",
			got.Previous.StartedMs, got.Previous.Done, len(got.Previous.Lines), first.StartedMs)
	}
	if len(got.Lines) != 0 || got.Done || got.FinishedMs != 0 || got.StartedMs == 0 {
		t.Errorf("current attempt inside the start's installing write: done=%v started_ms=%d finished_ms=%d lines=%+v, want the new, empty one",
			got.Done, got.StartedMs, got.FinishedMs, got.Lines)
	}
	if got.StartedMs < got.Previous.FinishedMs {
		t.Errorf("current attempt started %d, before the revive's finished %d", got.StartedMs, got.Previous.FinishedMs)
	}
}
