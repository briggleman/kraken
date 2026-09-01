package api_test

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel/agentbin"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/version"
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

// embeddedStubSHA returns the SHA-256 of the agent binary this Panel build has
// embedded for the platform the fake agents report — linux, on the test host's
// GOARCH, because Service.GetNodeInfo stamps Arch = runtime.GOARCH.
//
// Everything downstream of preflight's binary lookup needs one to exist, and a
// plain checkout embeds nothing (dist/ holds only .gitkeep). CI writes stubs
// before the test step precisely so these paths run (#189), so a missing binary
// there is a broken CI step, not an environment fact — fail loudly rather than
// skip, or the coverage silently evaporates the way it did before. Locally the
// skip stays: a dev checkout legitimately has no embedded agent, and
// `make embed-agents` is the way to opt in.
func embeddedStubSHA(t *testing.T) string {
	t.Helper()
	sha := agentbin.SHA("linux", runtime.GOARCH)
	if sha == "" {
		if os.Getenv("CI") != "" {
			t.Fatalf("no agent binary embedded for linux/%s on CI — the ci.yml step that writes "+
				"internal/panel/agentbin/dist/kraken-agent-* stubs did not run, so the agent-update "+
				"artifact-identity paths are untested (see #189)", runtime.GOARCH)
		}
		t.Skipf("panel built without an embedded agent binary for linux/%s — run `make embed-agents` to cover this path", runtime.GOARCH)
	}
	return sha
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

	// A dev build embeds no agent binary, so preflight legitimately answers 503
	// and there is no push to observe. On CI that is a broken stub step, not an
	// environment fact — embeddedStubSHA fails there instead of skipping (#189).
	embeddedStubSHA(t)

	rec := do(t, h, http.MethodPost, "/api/v1/nodes/"+nodeID+"/agent-update", token, nil)
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

// startFakeAgentReporting is startFakeAgent with the reported agent version
// under the test's control. The shared fixture reports "test", which can never
// equal the Panel's own version — so preflight's already-current refusal is
// unreachable through it.
//
// Called with an empty sha the fake has nothing to report, so its NodeInfo
// carries no BinarySha256 (see Service.GetNodeInfo) — exactly the missing-hash
// version-string fallback. With a sha it stands in for an agent whose
// self-updater reports one, which is what makes the artifact-identity branches
// (#93, #178, #186) reachable end to end; those also need a binary embedded on
// the Panel side, so their tests go through embeddedStubSHA.
func startFakeAgentReporting(t *testing.T, nodeID, agentVersion, sha string) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	rt := agent.NewFakeRuntime(nodeID, "linux", true, agentVersion, agent.WithFakeBinarySHA(sha))
	agentpb.RegisterNodeServiceServer(srv, agent.NewService(rt))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// An agent that already reports the Panel's version, with no hash on either
// side, must be refused with a 409 that names the version — the fallback half
// of the skew rule, end to end through the handler.
func TestAgentUpdateRefusesAnAgentAlreadyAtThePanelsVersion(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	nodeID := registerNode(t, h, token, startFakeAgentReporting(t, "node-current", version.Version, ""))

	rec := do(t, h, http.MethodPost, "/api/v1/nodes/"+nodeID+"/agent-update", token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST agent-update: status %d, want 409; body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already at the panel's version") {
		t.Errorf("409 should say the agent is already current, got %s", rec.Body.String())
	}
}

// Artifact identity beats the version strings: an agent whose self-reported hash
// equals the Panel's embedded build is refused even though the versions differ,
// and the refusal has to SAY "identical" — an operator reading "already at the
// panel's version" while the versions plainly differ reads it as a version bug
// (#186). Asserted on the message text for that reason.
//
// This is the branch that had no end-to-end coverage: it needs a real hash on
// both sides, so it runs only where a binary is embedded (CI stubs, or a local
// `make embed-agents`).
func TestAgentUpdateRefusesAnIdenticalBinaryDespiteADifferentVersion(t *testing.T) {
	sha := embeddedStubSHA(t)

	h, _ := newTestServerStore(t)
	token := login(t, h)
	// A version string that cannot equal the Panel's, so a 409 here can only
	// have come from the hash comparison.
	nodeID := registerNode(t, h, token, startFakeAgentReporting(t, "node-identical", "v0.0.0-different", sha))

	rec := do(t, h, http.MethodPost, "/api/v1/nodes/"+nodeID+"/agent-update", token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST agent-update: status %d, want 409; body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "identical") {
		t.Errorf("409 must say the binary is identical, got %s", body)
	}
	if strings.Contains(body, "already at the panel's version") {
		t.Errorf("409 took the version-string branch, not the hash branch: %s", body)
	}
	// The message also has to carry both labels, or "identical" reads as a
	// contradiction of the versions the operator can see.
	if !strings.Contains(body, "v0.0.0-different") || !strings.Contains(body, version.Version) {
		t.Errorf("409 should name both versions, got %s", body)
	}
}

// The other half of #178: equal version strings with DIFFERENT bytes is a dirty
// rebuild of the same tag, and "bring the node to the panel's build" means bytes
// — so it pushes. Covered until now only by the pure decision matrix, where the
// version-equality shortcut could have been reinstated ahead of the hash check
// without any end-to-end test noticing.
func TestAgentUpdatePushesADifferentBinaryAtTheSameVersion(t *testing.T) {
	embeddedStubSHA(t)

	h, _ := newTestServerStore(t)
	token := login(t, h)
	// Same version as the Panel, hash that cannot match the embedded build.
	const otherSHA = "0000000000000000000000000000000000000000000000000000000000000000"
	nodeID := registerNode(t, h, token, startFakeAgentReporting(t, "node-dirty-rebuild", version.Version, otherSHA))

	rec := do(t, h, http.MethodPost, "/api/v1/nodes/"+nodeID+"/agent-update", token, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST agent-update: status %d, want 202; body %s", rec.Code, rec.Body.String())
	}
	job := decodeJob(t, rec.Body.Bytes())
	if job["phase"] != "pushing" {
		t.Errorf("phase = %v, want pushing", job["phase"])
	}
	// from/to both being the same version is the whole point of the edge — the
	// push is justified by the bytes, not the label.
	if job["from_version"] != version.Version || job["to_version"] != version.Version {
		t.Errorf("job should push %s → %s, got from=%v to=%v",
			version.Version, version.Version, job["from_version"], job["to_version"])
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
