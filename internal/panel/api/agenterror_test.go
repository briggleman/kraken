package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/agent"
)

// These run the real Panel → gRPC → Agent path (agent.NewService over the fake
// runtime, served with agent.ServerOptions), so each failure crosses the same
// classification a live Agent applies before the Panel maps it to a status.
//
// The point of all of them is #352: an Agent failure used to be a 502 whatever
// its cause, and the live Panel's edge (Cloudflare) replaces a 502 body with
// its own page — the operator saw "HTTP 502" for a file a running game held
// locked. None of these may be a 502 or a 504, and each must carry the cause.

type agentErrorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// newFilesEnv is newDownloadEnv with the fake Agent's options exposed, so a
// test can make the node's filesystem refuse an operation.
func newFilesEnv(t *testing.T, opts ...agent.FakeOption) *downloadEnv {
	t.Helper()
	srv, st := newTestAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, _ := startFakeAgentRuntime(t, "node-agent-errors", opts...)
	nodeID := registerNode(t, h, token, addr)
	pollNode(t, h, token, nodeID)
	specID := createSpecWithBackup(t, h, token, "agent-errors-spec", nil)
	serverID := createServerFromSpec(t, h, token, specID, "abyssal-errors")
	waitInstalled(t, st, serverID)
	return &downloadEnv{srv: srv, h: h, st: st, token: token, server: serverID, nodeID: nodeID}
}

func decodeAgentError(t *testing.T, rec *httptest.ResponseRecorder) agentErrorBody {
	t.Helper()
	var b agentErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode error body: %v (body %s)", err, rec.Body.String())
	}
	if b.Error == "" {
		t.Fatalf("error body carries no message: %s", rec.Body.String())
	}
	return b
}

func (e *downloadEnv) deleteFiles(t *testing.T, paths ...string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, e.h, http.MethodPost, "/api/v1/servers/"+e.server+"/files/delete", e.token,
		map[string]any{"paths": paths})
}

// A node the Panel cannot reach is a 503 node_unreachable — the one genuinely
// gateway-shaped failure, answered with the status whose body survives the edge.
func TestAgentUnreachableIs503NodeUnreachable(t *testing.T) {
	e := newDownloadEnv(t)
	ctx := context.Background()
	node, err := e.st.GetNode(ctx, e.nodeID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	node.Address = "127.0.0.1:1"
	if err := e.st.UpdateNode(ctx, node); err != nil {
		t.Fatalf("update node: %v", err)
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"list":   do(t, e.h, http.MethodGet, "/api/v1/servers/"+e.server+"/files?path=/data", e.token, nil),
		"delete": e.deleteFiles(t, fakeCfgPath),
	} {
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s against an unreachable node: got %d, want 503 (body %s)", name, rec.Code, rec.Body.String())
		}
		if b := decodeAgentError(t, rec); b.Code != "node_unreachable" {
			t.Fatalf("%s: code = %q, want node_unreachable", name, b.Code)
		}
	}
}

// A path the node does not have is a 404 not_found, end to end: the fake
// Agent's delete wraps fs.ErrNotExist exactly as the os package does.
func TestAgentMissingPathIs404(t *testing.T) {
	e := newDownloadEnv(t)
	rec := e.deleteFiles(t, "/data/no-such-file")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete of a missing file: got %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	b := decodeAgentError(t, rec)
	if b.Code != "not_found" || !strings.Contains(b.Error, "/data/no-such-file") {
		t.Fatalf("missing file answered %+v, want not_found naming the path", b)
	}
}

// The #352 case: a file another process holds. 409 file_in_use, with the
// node's own words in the message and the likely holder named.
func TestAgentFileInUseIs409(t *testing.T) {
	held := &fs.PathError{Op: "remove", Path: "/data/Saves/world.sav", Err: inUseErrno}
	e := newFilesEnv(t, agent.WithFakeFileError(held))
	rec := e.deleteFiles(t, "/data/Saves/world.sav")
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete of a held file: got %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	b := decodeAgentError(t, rec)
	if b.Code != "file_in_use" {
		t.Fatalf("code = %q, want file_in_use (body %+v)", b.Code, b)
	}
	if !strings.Contains(b.Error, inUseErrno.Error()) {
		t.Fatalf("message %q dropped the cause %q", b.Error, inUseErrno.Error())
	}
	if !strings.Contains(b.Error, "in use by another process") || !strings.Contains(b.Error, "game container may still be running") {
		t.Fatalf("message %q does not say what to do about it", b.Error)
	}
	if strings.HasPrefix(b.Error, "agent error:") {
		t.Fatalf("message %q still wraps the cause in the old prefix", b.Error)
	}
}

// Every other refusal gets its own non-5xx status and code, and the operator
// still reads the cause.
func TestAgentRefusalsMapToTheirStatus(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"permission", &fs.PathError{Op: "remove", Path: "/data/x", Err: fs.ErrPermission}, http.StatusConflict, "node_refused"},
		{"exists", &fs.PathError{Op: "mkdir", Path: "/data/x", Err: fs.ErrExist}, http.StatusConflict, "already_exists"},
		{"bad path", agent.ErrBadPath, http.StatusBadRequest, "bad_path"},
		// A failure the Agent could not classify is a 500, never a 502.
		{"unclassified", errors.New("the disk is on fire"), http.StatusInternalServerError, "node_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newFilesEnv(t, agent.WithFakeFileError(tc.err))
			rec := e.deleteFiles(t, fakeCfgPath)
			if rec.Code != tc.status {
				t.Fatalf("got %d, want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
			b := decodeAgentError(t, rec)
			if b.Code != tc.code {
				t.Fatalf("code = %q, want %q", b.Code, tc.code)
			}
			if !strings.Contains(b.Error, tc.err.Error()) {
				t.Fatalf("message %q dropped the cause %q", b.Error, tc.err.Error())
			}
		})
	}
}

// A data-root move is refused by the fake exactly as by the real runtime, as
// bad input.
func TestAgentBadPathIs400(t *testing.T) {
	e := newDownloadEnv(t)
	rec := do(t, e.h, http.MethodPost, "/api/v1/servers/"+e.server+"/files/move", e.token,
		map[string]string{"src": "/data", "dst": "/data/elsewhere"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("move of the data root: got %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if b := decodeAgentError(t, rec); b.Code != "bad_path" {
		t.Fatalf("code = %q, want bad_path", b.Code)
	}
}
