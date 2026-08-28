package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// createSpecWithMemory seeds a spec whose resources state both figures, so the
// difference between the minimum and the recommended value is observable.
func createSpecWithMemory(t *testing.T, h http.Handler, token, slug string, minMB, recMB int) string {
	t.Helper()
	resources := map[string]int{"min_memory_mb": minMB}
	if recMB > 0 {
		resources["recommended_memory_mb"] = recMB
	}
	rec := do(t, h, http.MethodPost, "/api/v1/specs", token, map[string]any{
		"name": "Memory Spec", "slug": slug,
		"steam_app_ids": map[string]int{"linux": 730},
		"platforms":     []map[string]string{{"kind": "linux-native", "image": "registry/kraken/steam-base:latest"}},
		"install":       map[string]any{"script": "true"},
		"startup": map[string]any{
			"command": "./run -port {{PORT_GAME}}",
			"stop":    map[string]string{"type": "signal", "value": "SIGINT"},
		},
		"ports":     []map[string]any{{"name": "game", "protocol": "udp", "default": 27015, "required": true}},
		"resources": resources,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create spec: status %d, body %s", rec.Code, rec.Body.String())
	}
	var sp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sp)
	return sp.ID
}

// memoryTestFleet gives a logged-in handler with one online 16GB node.
func memoryTestFleet(t *testing.T) (http.Handler, string) {
	t.Helper()
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startAgentWithRuntimeStatus(t, "mem-node", agentpb.RuntimeStatus_RUNTIME_STATUS_OK, "")
	id := registerNode(t, h, token, addr)
	pollNode(t, h, token, id)
	return h, token
}

func createdServerMemory(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	var sv struct {
		MemoryMB int `json:"memory_mb"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sv); err != nil {
		t.Fatalf("decode server: %v", err)
	}
	return sv.MemoryMB
}

// The default is the spec's recommended figure. The minimum is the floor at
// which the game boots, not the figure it runs at — a Valheim server placed at
// its 2048MB minimum was OOM-killed generating its first world (#131).
func TestServerCreate_DefaultsToRecommendedMemory(t *testing.T) {
	h, token := memoryTestFleet(t)
	specID := createSpecWithMemory(t, h, token, "mem-default", 2048, 4096)

	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"spec_id": specID, "name": "mem-default-01",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := createdServerMemory(t, rec); got != 4096 {
		t.Errorf("memory_mb = %d, want the recommended 4096", got)
	}
}

// With no recommended figure stated, the minimum is all there is.
func TestServerCreate_FallsBackToMinimumMemory(t *testing.T) {
	h, token := memoryTestFleet(t)
	specID := createSpecWithMemory(t, h, token, "mem-min-only", 3072, 0)

	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"spec_id": specID, "name": "mem-min-01",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := createdServerMemory(t, rec); got != 3072 {
		t.Errorf("memory_mb = %d, want the minimum 3072", got)
	}
}

// The regression this fixes: memory_mb was a field on the create request that
// nothing read, so a caller sizing a server got a 201 and the spec's figure
// anyway — a silent discard, which is worse than not accepting the field.
func TestServerCreate_HonorsExplicitMemory(t *testing.T) {
	h, token := memoryTestFleet(t)
	specID := createSpecWithMemory(t, h, token, "mem-explicit", 2048, 4096)

	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"spec_id": specID, "name": "mem-explicit-01", "memory_mb": 8192,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := createdServerMemory(t, rec); got != 8192 {
		t.Errorf("memory_mb = %d, want the requested 8192 — the value must not be silently dropped", got)
	}
}

// Below the spec's floor the game does not boot, so provisioning a server that
// cannot start is not a choice worth honoring.
func TestServerCreate_RejectsMemoryBelowSpecMinimum(t *testing.T) {
	h, token := memoryTestFleet(t)
	specID := createSpecWithMemory(t, h, token, "mem-too-small", 2048, 4096)

	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"spec_id": specID, "name": "mem-small-01", "memory_mb": 1024,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

// An explicit request larger than the node is a placement conflict, not an
// over-commit — the node's capacity still governs.
func TestServerCreate_ExplicitMemoryStillBoundedByNode(t *testing.T) {
	h, token := memoryTestFleet(t)
	specID := createSpecWithMemory(t, h, token, "mem-too-big", 2048, 4096)

	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"spec_id": specID, "name": "mem-big-01", "memory_mb": 999999,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409, body %s", rec.Code, rec.Body.String())
	}
}

// Explicitly asking for exactly the minimum is allowed — it is the operator's
// call, and the floor is a floor, not an exclusion.
func TestServerCreate_ExplicitMemoryAtTheMinimumIsAllowed(t *testing.T) {
	h, token := memoryTestFleet(t)
	specID := createSpecWithMemory(t, h, token, "mem-at-min", 2048, 4096)

	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"spec_id": specID, "name": "mem-at-min-01", "memory_mb": 2048,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := createdServerMemory(t, rec); got != 2048 {
		t.Errorf("memory_mb = %d, want 2048", got)
	}
}
