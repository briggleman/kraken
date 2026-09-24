package api

import (
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// codedErrorBody is the error envelope for a failure a client may want to branch
// on: `error` is the human sentence the UI shows, `code` is the stable
// machine-readable reason. (The 409 a start refuses with over missing settings
// adds a third field; see handlers_server.go.)
type codedErrorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// writeCoded writes an error envelope that carries a machine-readable code
// beside the message.
func writeCoded(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, codedErrorBody{Error: msg, Code: code})
}

// The machine-readable codes an Agent-call failure answers with. Clients branch
// on these, never on the text.
const (
	codeNodeUnreachable = "node_unreachable" // 503: the Panel could not reach the node's Agent, or it did not answer in time
	codeNotFound        = "not_found"        // 404: the path (or backup) does not exist on the node
	codeBadPath         = "bad_path"         // 400: the path can never work (escapes the data dir, is the data root, …)
	codeFileInUse       = "file_in_use"      // 409: another process holds the file
	codeNodeRefused     = "node_refused"     // 409: the node's filesystem refused the operation (permissions)
	codeAlreadyExists   = "already_exists"   // 409: the target already exists
	codeNodeError       = "node_error"       // 500: anything else the node reported
)

// agentFailure maps an error from an Agent RPC to the HTTP status, code and
// message the caller receives.
//
// Nothing here answers 502 or 504, and that is deliberate (#352). Those were
// what every Agent failure used to be, and the live Panel sits behind
// Cloudflare, which REPLACES a 502/504 body with its own error page — so the
// operator got a bare "HTTP 502" even when the Agent had said precisely what
// was wrong ("Access is denied." on a file a running game held). A 503 survives
// the edge, and so do the 4xx statuses; the Agent's own message is carried in
// every one of them.
//
// The code comes from the Agent's gRPC status (see agent.classifyError, which
// gives each runtime failure the code its cause deserves). An error with no
// gRPC status at all — a Panel-side failure dressed up as an Agent one — is the
// generic 500.
func agentFailure(err error) (int, string, string) {
	st, ok := status.FromError(err)
	if !ok {
		return http.StatusInternalServerError, codeNodeError, err.Error()
	}
	msg := st.Message()
	switch st.Code() {
	case codes.Unavailable:
		return http.StatusServiceUnavailable, codeNodeUnreachable, "could not reach the node's agent: " + msg
	case codes.DeadlineExceeded:
		return http.StatusServiceUnavailable, codeNodeUnreachable, "the node's agent did not answer in time: " + msg
	case codes.NotFound:
		return http.StatusNotFound, codeNotFound, msg
	case codes.InvalidArgument:
		return http.StatusBadRequest, codeBadPath, msg
	case codes.FailedPrecondition:
		// The one failure an operator can act on straight away, and the one
		// behind #352: the Agent names the file; the likely holder is the
		// server's own game process.
		return http.StatusConflict, codeFileInUse, msg + " — a game container may still be running"
	case codes.PermissionDenied:
		// On a Windows node this is also what deleting a running game's own
		// binaries answers ("Access is denied."), so the hint names both.
		return http.StatusConflict, codeNodeRefused, msg + " — the node refused this; check the server is stopped and the agent can write there"
	case codes.AlreadyExists:
		return http.StatusConflict, codeAlreadyExists, msg
	}
	return http.StatusInternalServerError, codeNodeError, msg
}

// writeAgentError answers a failed Agent RPC. See agentFailure for the mapping.
func writeAgentError(w http.ResponseWriter, err error) {
	st, code, msg := agentFailure(err)
	writeCoded(w, st, code, msg)
}

// writeNodeUnreachable answers when the Panel has no client for the node at
// all — a dial target it cannot build, or a tunnel-mode node with no tunnel
// transport. It is the same answer as an RPC that could not reach the Agent:
// a 503, whose body survives the edge (see agentFailure).
func writeNodeUnreachable(w http.ResponseWriter, err error) {
	msg := "could not reach the node's agent"
	if err != nil {
		msg += ": " + err.Error()
	}
	writeCoded(w, http.StatusServiceUnavailable, codeNodeUnreachable, msg)
}
