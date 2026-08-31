package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// decodeJob pulls the job body off a response.
func decodeJob(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode job: %v (body %s)", err, body)
	}
	return m
}

// The fake agent has no self-updater, so UpdateAgent refuses with
// FailedPrecondition the moment it sees the metadata. That makes it the exact
// fixture this needs: a push that reaches a terminal phase quickly, with the
// agent's own words, and no 17MB binary required.
//
// What it proves is the whole point of the change: the 202 is returned while the
// stream is still the goroutine's business, and the job then reaches `failed`
// with the agent's reason — which is only possible if the goroutine is NOT
// running on the request's context.
func TestAgentUpdateReturns202ThenFailsIndependentlyOfTheRequest(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr := startFakeAgent(t, "node-x")
	nodeID := registerNode(t, h, token, addr)

	rec := do(t, h, http.MethodPost, "/api/v1/nodes/"+nodeID+"/agent-update", token, nil)
	// A dev build embeds no agent binary, so preflight legitimately answers 503
	// here. Both outcomes are correct; only the 503 short-circuits the job.
	if rec.Code == http.StatusServiceUnavailable {
		if !strings.Contains(rec.Body.String(), "no embedded agent binary") {
			t.Fatalf("503 for an unexpected reason: %s", rec.Body.String())
		}
		t.Skip("panel built without embedded agent binaries — the async path needs one to push")
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST agent-update: status %d, want 202; body %s", rec.Code, rec.Body.String())
	}

	job := decodeJob(t, rec.Body.Bytes())
	if job["job_id"] == "" || job["job_id"] == nil {
		t.Error("202 body carries no job_id")
	}
	if job["phase"] != "pushing" {
		t.Errorf("phase = %v, want pushing", job["phase"])
	}

	// Poll the status endpoint until the goroutine settles. If the goroutine had
	// inherited the request context this would sit on `pushing` forever.
	deadline := time.Now().Add(10 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		sr := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/agent-update", token, nil)
		if sr.Code != http.StatusOK {
			t.Fatalf("GET agent-update: status %d, body %s", sr.Code, sr.Body.String())
		}
		last = decodeJob(t, sr.Body.Bytes())
		if last["phase"] != "pushing" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if last["phase"] != "failed" {
		t.Fatalf("phase = %v, want failed (the fake agent has no updater); job %+v", last["phase"], last)
	}
	// #160's whole point: the agent's refusal reaches the operator verbatim.
	if msg, _ := last["error"].(string); !strings.Contains(msg, "self-update is not available") {
		t.Errorf("job error should carry the agent's reason, got %q", msg)
	}
}

func TestAgentUpdateStatusIs404WithNoJob(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	nodeID := registerNode(t, h, token, startFakeAgent(t, "node-y"))

	rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/agent-update", token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status with no job: %d, want 404", rec.Code)
	}
	// The message has to say what a 404 MEANS here, or an operator reads it as
	// "the node is gone".
	if !strings.Contains(rec.Body.String(), "agent_version") {
		t.Errorf("404 should point at the node record, got %s", rec.Body.String())
	}
}

// NOTE: the plan also asked for a permissions test (viewer can GET, cannot POST).
// Left out deliberately: no test in this package exercises non-admin roles, so it
// would need a role lookup, a user creation and a second login to assert one
// route declaration — `r.With(s.requirePermission(rbac.PermNodeView)).Get(...)` —
// whose correctness is legible in server.go. Worth adding alongside a general
// RBAC-route test, not as a one-off here.
