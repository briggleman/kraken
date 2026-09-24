package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/client"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/shared/agentpb"
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
	// Windows ends its system messages with a full stop, which the sentence
	// drops inside its parenthesis.
	if !strings.Contains(b.Error, strings.TrimSuffix(inUseErrno.Error(), ".")) {
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
		// A delete that finds its folder "not empty" (Go: fs.ErrExist) is a
		// folder something is still writing into — in use, not a collision.
		{"delete of a folder still being written", &fs.PathError{Op: "remove", Path: "/data/x", Err: fs.ErrExist}, http.StatusConflict, "file_in_use"},
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
			// The OS's own words survive: the whole error, or — where the
			// in-use sentence rebuilds the message around the path — its cause.
			cause := tc.err.Error()
			var pe *fs.PathError
			if errors.As(tc.err, &pe) && tc.code == "file_in_use" {
				cause = pe.Err.Error()
			}
			if !strings.Contains(b.Error, cause) {
				t.Fatalf("message %q dropped the cause %q", b.Error, cause)
			}
		})
	}
}

// A mkdir onto a path that exists is a real collision.
func TestAgentMkdirOntoAnExistingPathIs409AlreadyExists(t *testing.T) {
	e := newFilesEnv(t, agent.WithFakeFileError(&fs.PathError{Op: "mkdir", Path: "/data/x", Err: fs.ErrExist}))
	rec := do(t, e.h, http.MethodPost, "/api/v1/servers/"+e.server+"/files/mkdir", e.token,
		map[string]string{"path": "/data/x"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("mkdir onto an existing path: got %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if b := decodeAgentError(t, rec); b.Code != "already_exists" {
		t.Fatalf("code = %q, want already_exists", b.Code)
	}
}

// A power action that fails for a reason that is not a file is the node's own
// error, said plainly: a 500 carrying the message, and none of the file hints.
// The same fs sentinels turn up here — a Docker engine that is down on Windows
// is ERROR_FILE_NOT_FOUND on its named pipe, docker.sock refusing the Agent is
// EACCES — and must not come back as "404 not found" or "check the agent can
// write there" on a start.
func TestAgentNonFileFailuresOnPowerAre500WithoutFileHints(t *testing.T) {
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://127.0.0.1:1"))
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, dockerDown := cli.Ping(ctx)
	if !client.IsErrConnectionFailed(dockerDown) {
		t.Fatalf("precondition: Ping against a closed port = %v, not a connection failure", dockerDown)
	}
	cases := []struct {
		name string
		err  error
		want string // a fragment the message must carry
	}{
		{"docker engine unreachable", fmt.Errorf("docker: stop: %w", dockerDown), "cannot connect to the daemon"},
		{"docker engine pipe missing", &fs.PathError{Op: "open", Path: `\\.\pipe\docker_engine`, Err: fs.ErrNotExist}, "docker_engine"},
		{"docker.sock refused", &fs.PathError{Op: "dial", Path: "/var/run/docker.sock", Err: fs.ErrPermission}, "docker.sock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newFilesEnv(t, agent.WithFakePowerError(agentpb.PowerAction_POWER_ACTION_STOP, tc.err))
			rec := do(t, e.h, http.MethodPost, "/api/v1/servers/"+e.server+"/power", e.token,
				map[string]string{"action": "stop"})
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("got %d, want 500 (body %s)", rec.Code, rec.Body.String())
			}
			b := decodeAgentError(t, rec)
			if b.Code != "node_error" {
				t.Fatalf("code = %q, want node_error", b.Code)
			}
			if !strings.Contains(b.Error, tc.want) {
				t.Fatalf("message %q does not carry %q", b.Error, tc.want)
			}
			for _, hint := range []string{"game container may still be running", "agent can write there"} {
				if strings.Contains(b.Error, hint) {
					t.Fatalf("a non-file failure carries the file hint %q: %s", hint, b.Error)
				}
			}
		})
	}
}

// A tunnel-mode node the Panel has no tunnel transport for gets no client at
// all. That is the node being unreachable — a 503 — not a Panel 500.
func TestNodeInfoWithoutATunnelTransportIs503(t *testing.T) {
	e := newDownloadEnv(t)
	ctx := context.Background()
	node, err := e.st.GetNode(ctx, e.nodeID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	node.ConnectionMode = cluster.ConnTunnel
	if err := e.st.UpdateNode(ctx, node); err != nil {
		t.Fatalf("update node: %v", err)
	}
	rec := do(t, e.h, http.MethodGet, "/api/v1/nodes/"+e.nodeID+"/info", e.token, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("node info without a tunnel transport: got %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	b := decodeAgentError(t, rec)
	if b.Code != "node_unreachable" || !strings.Contains(b.Error, "tunnel") {
		t.Fatalf("answered %+v, want node_unreachable naming the missing tunnel transport", b)
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
