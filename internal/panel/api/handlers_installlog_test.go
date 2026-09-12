package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// installLogBody is the shape of GET /servers/{id}/install-log.
type installLogBody struct {
	ServerID string `json:"server_id"`
	Done     bool   `json:"done"`
	Retained bool   `json:"retained"`
	Lines    []struct {
		Ts     int64  `json:"ts"`
		Stream string `json:"stream"`
		Text   string `json:"text"`
	} `json:"lines"`
	StartedMs  int64 `json:"started_ms"`
	FinishedMs int64 `json:"finished_ms"`
}

func getInstallLog(t *testing.T, h http.Handler, token, id string) installLogBody {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/v1/servers/"+id+"/install-log", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET install-log: status %d, body %s", rec.Code, rec.Body.String())
	}
	var body installLogBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode install-log: %v (body %s)", err, rec.Body.String())
	}
	return body
}

// The whole of #280's first half: a SUCCESSFUL install's output must still be
// readable once the server is offline. The console WebSocket routes to the
// install buffer only while the state is installing/install_failed, so after a
// success the only way back to what SteamCMD actually did used to be a
// wipe-and-reinstall while watching — which is what a broken-but-exit-0 install
// makes you do repeatedly.
func TestInstallLog_ReadableAfterASuccessfulInstall(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	nodeID := registerNode(t, h, token, startFakeAgent(t, "node-install-log"))
	// An info probe is what marks a freshly registered node online (and so
	// schedulable) — the same warm-up the console-stream tests do.
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
	specID := createSpec(t, h, token, "install-log-kept")

	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"spec_id": specID, "name": "kept-01",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create server: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	deadline := time.Now().Add(20 * time.Second)
	for getServerState(t, h, token, created.ID) != "offline" {
		if time.Now().After(deadline) {
			t.Fatalf("server never finished installing (state %q)", getServerState(t, h, token, created.ID))
		}
		time.Sleep(50 * time.Millisecond)
	}

	body := getInstallLog(t, h, token, created.ID)
	if !body.Retained {
		t.Fatal("the install log was dropped on success — #280 all over again")
	}
	if !body.Done {
		t.Error("a finished install must read as done")
	}
	if body.ServerID != created.ID {
		t.Errorf("server_id = %q, want %q", body.ServerID, created.ID)
	}
	if body.StartedMs == 0 || body.FinishedMs == 0 {
		t.Errorf("want both timestamps; started_ms=%d finished_ms=%d", body.StartedMs, body.FinishedMs)
	}
	var sawInstaller, sawCompletion bool
	for _, l := range body.Lines {
		if strings.Contains(l.Text, "Downloading update") {
			sawInstaller = true
		}
		if strings.Contains(l.Text, "install complete") {
			sawCompletion = true
		}
	}
	if !sawInstaller {
		t.Errorf("the installer's own output is missing; got %+v", body.Lines)
	}
	if !sawCompletion {
		t.Errorf("the completion line is missing; got %+v", body.Lines)
	}
}

// A Panel that restarted since the attempt holds nothing — which the endpoint
// reports as retained:false rather than as an install that printed nothing, so
// the UI can say which of the two it is. A never-installed server takes the same
// path, and the answer must still be a 200 with an empty array (never null),
// because the console surface renders it directly.
func TestInstallLog_UnretainedReadsAsEmptyNotMissing(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	nodeID := registerNode(t, h, token, startFakeAgent(t, "node-no-log"))
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
	specID := createSpec(t, h, token, "install-log-absent")

	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"spec_id": specID, "name": "absent-01",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create server: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for getServerState(t, h, token, created.ID) != "offline" {
		if time.Now().After(deadline) {
			t.Fatalf("server never finished installing (state %q)", getServerState(t, h, token, created.ID))
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Deleting the server drops its buffer — the same end state as a Panel
	// restart, reached without one.
	if rec := do(t, h, http.MethodDelete, "/api/v1/servers/"+created.ID, token, nil); rec.Code != http.StatusOK &&
		rec.Code != http.StatusNoContent && rec.Code != http.StatusAccepted {
		t.Fatalf("delete server: status %d, body %s", rec.Code, rec.Body.String())
	}

	// The server record is gone with it, so the endpoint 404s like every other
	// server-scoped route rather than serving a stranger's buffer.
	if rec := do(t, h, http.MethodGet, "/api/v1/servers/"+created.ID+"/install-log", token, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("install-log for a deleted server: status %d, want 404", rec.Code)
	}
}

// The endpoint is server-scoped and must refuse an unknown id the same way the
// rest of the server API does, rather than reporting an empty log.
func TestInstallLog_UnknownServerIs404(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	rec := do(t, h, http.MethodGet, "/api/v1/servers/00000000-0000-0000-0000-000000000000/install-log", token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404; body %s", rec.Code, rec.Body.String())
	}
}

// Unauthenticated reads are refused: the install log carries the installer's
// environment-shaped output and is no more public than the console.
func TestInstallLog_RequiresASession(t *testing.T) {
	h, _ := newTestServerStore(t)
	rec := do(t, h, http.MethodGet, "/api/v1/servers/whatever/install-log", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401; body %s", rec.Code, rec.Body.String())
	}
}
