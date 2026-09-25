package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/api"
)

// installLogBody is the shape of GET /servers/{id}/install-log.
type installLogBody struct {
	ServerID   string              `json:"server_id"`
	Done       bool                `json:"done"`
	Retained   bool                `json:"retained"`
	Lines      []installLineBody   `json:"lines"`
	StartedMs  int64               `json:"started_ms"`
	FinishedMs int64               `json:"finished_ms"`
	Previous   *installAttemptBody `json:"previous"`
}

// installLineBody is one line of install output as the endpoint answers it.
type installLineBody struct {
	Ts     int64  `json:"ts"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

// installAttemptBody is the shape of the endpoint's `previous` (#381): the
// current attempt's shape without the server-level fields.
type installAttemptBody struct {
	Done       bool              `json:"done"`
	Lines      []installLineBody `json:"lines"`
	StartedMs  int64             `json:"started_ms"`
	FinishedMs int64             `json:"finished_ms"`
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
	srv, _ := newTestAPI(t)
	h := srv.Handler()
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
	// Retiring and deleting the server drops its buffer — the same end state
	// as a Panel restart, reached without one.
	retireServer(t, srv, token, created.ID)
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

// #381: a reinstall used to erase the output of the attempt it replaced, which
// on 2026-09-25 was the only record of why an update pass broke the tree. The
// endpoint now answers `previous` — null before any second attempt, and the
// replaced attempt in the current attempt's shape after one. The completion
// line also says, on a reinstall only, that the node has no container for the
// server until START.
func TestInstallLog_PreviousAttemptSurvivesAReinstall(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	nodeID := registerNode(t, h, token, startFakeAgent(t, "node-install-prev"))
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
	specID := createSpec(t, h, token, "install-log-previous")
	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"spec_id": specID, "name": "prev-01",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create server: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	waitInstalled := func(wantPrevious bool) installLogBody {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			if getServerState(t, h, token, created.ID) == "offline" {
				body := getInstallLog(t, h, token, created.ID)
				if body.Done && (body.Previous != nil) == wantPrevious {
					return body
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("install never settled (state %q)", getServerState(t, h, token, created.ID))
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	first := waitInstalled(false)
	// null, not absent: the console surface reads the key directly.
	raw := do(t, h, http.MethodGet, "/api/v1/servers/"+created.ID+"/install-log", token, nil)
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(raw.Body.Bytes(), &generic); err != nil {
		t.Fatalf("decode raw install-log: %v", err)
	}
	if p, ok := generic["previous"]; !ok || string(p) != "null" {
		t.Fatalf("first attempt: previous = %s (present %v), want null", p, ok)
	}
	firstComplete := completionLine(first.Lines)
	if firstComplete == "" || strings.Contains(firstComplete, "no container") {
		t.Errorf("a first provision's completion line must keep its wording; got %q", firstComplete)
	}

	if rec := do(t, h, http.MethodPost, "/api/v1/servers/"+created.ID+"/reinstall", token, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("reinstall: status %d, body %s", rec.Code, rec.Body.String())
	}
	second := waitInstalled(true)

	p := second.Previous
	if !p.Done {
		t.Error("a previous attempt is over, so it must read as done")
	}
	if p.StartedMs != first.StartedMs || p.FinishedMs != first.FinishedMs {
		t.Errorf("previous bracket %d–%d, want the first attempt's %d–%d",
			p.StartedMs, p.FinishedMs, first.StartedMs, first.FinishedMs)
	}
	if len(p.Lines) != len(first.Lines) || completionLine(p.Lines) != firstComplete {
		t.Errorf("previous lines = %+v, want the first attempt's %+v", p.Lines, first.Lines)
	}
	if second.StartedMs < first.FinishedMs {
		t.Errorf("current attempt started %d, before the previous finished %d", second.StartedMs, first.FinishedMs)
	}
	if got := completionLine(second.Lines); !strings.HasSuffix(got, "; no container exists until you start it") {
		t.Errorf("a reinstall's completion line must say no container exists until you start it; got %q", got)
	}
}

// #387: the reinstall used to write `installing` and answer 202 before its
// provision goroutine rotated the install buffer, so a read in that gap got the
// attempt before the button press as if it were the new one — and the drill-in
// does not read again while the state stays `installing`. The buffer is now
// rotated in the handler, before the state write. The provision goroutine is
// held at its very first line here, so the read below lands in the gap every
// time rather than only when the scheduler happens to be slow.
func TestInstallLog_ReinstallRotatesTheLogBeforeItAnswers(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	nodeID := registerNode(t, h, token, startFakeAgent(t, "node-install-rotate"))
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
	specID := createSpec(t, h, token, "install-log-rotate")
	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"spec_id": specID, "name": "rotate-01",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create server: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	waitSettled := func(wantPrevious bool) installLogBody {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			if getServerState(t, h, token, created.ID) == "offline" {
				body := getInstallLog(t, h, token, created.ID)
				if body.Done && (body.Previous != nil) == wantPrevious {
					return body
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("install never settled (state %q)", getServerState(t, h, token, created.ID))
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	first := waitSettled(false)
	if len(first.Lines) == 0 {
		t.Fatal("the first attempt printed nothing, so it cannot be told apart from a new one")
	}

	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	clearHook := api.SetProvisionHookForTest(func(id string) {
		if id != created.ID {
			return
		}
		entered <- struct{}{}
		<-gate
	})
	defer clearHook()
	var opened sync.Once
	openGate := func() { opened.Do(func() { close(gate) }) }
	defer openGate()

	if rec := do(t, h, http.MethodPost, "/api/v1/servers/"+created.ID+"/reinstall", token, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("reinstall: status %d, body %s", rec.Code, rec.Body.String())
	}
	got := getInstallLog(t, h, token, created.ID)

	if got.Previous == nil {
		t.Fatal("previous is null right after the reinstall's 202: the log was not rotated before the answer")
	}
	if got.Previous.StartedMs != first.StartedMs || got.Previous.FinishedMs != first.FinishedMs {
		t.Errorf("previous bracket %d–%d, want the first attempt's %d–%d",
			got.Previous.StartedMs, got.Previous.FinishedMs, first.StartedMs, first.FinishedMs)
	}
	if len(got.Previous.Lines) != len(first.Lines) {
		t.Errorf("previous has %d lines, want the first attempt's %d", len(got.Previous.Lines), len(first.Lines))
	}
	if len(got.Lines) != 0 || got.Done || got.FinishedMs != 0 {
		t.Errorf("the current attempt must be the new, empty one; got done=%v finished_ms=%d lines=%+v",
			got.Done, got.FinishedMs, got.Lines)
	}
	if got.StartedMs == 0 || got.StartedMs < first.FinishedMs {
		t.Errorf("the new attempt's started_ms = %d, want set and not before the first finished (%d)",
			got.StartedMs, first.FinishedMs)
	}
	if !got.Retained {
		t.Error("the new attempt must read as retained")
	}

	// The provision goroutine reached the hook, so the read above really was
	// in the gap before it had done anything.
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the reinstall's provision never started")
	}
	openGate()
	second := waitSettled(true)
	// Rotated once, not twice: the attempt kept as previous is still the first.
	if second.Previous.StartedMs != first.StartedMs {
		t.Errorf("after the install, previous started %d, want the first attempt's %d (rotated twice?)",
			second.Previous.StartedMs, first.StartedMs)
	}
	if second.StartedMs != got.StartedMs {
		t.Errorf("the finished attempt started %d, want the one opened in the handler (%d)", second.StartedMs, got.StartedMs)
	}
}

// completionLine finds the Panel's "install complete" line in an attempt.
func completionLine(lines []installLineBody) string {
	for _, l := range lines {
		if strings.Contains(l.Text, "install complete") {
			return l.Text
		}
	}
	return ""
}
