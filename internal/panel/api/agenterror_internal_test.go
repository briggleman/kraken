package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/panel/nodeclient"
)

// The two 503s say different things, because they are different facts: an
// Agent the Panel never reached did nothing, while a call the Panel stopped
// waiting for may still be running on the node (a stop inside its
// graceful-stop window). Telling the operator "nothing was attempted" about
// the second would be false.
func TestAgentFailureDistinguishesUnreachableFromTimedOut(t *testing.T) {
	st, code, msg := agentFailure(status.Error(codes.Unavailable, "connection refused"))
	if st != http.StatusServiceUnavailable || code != codeNodeUnreachable || !strings.HasPrefix(msg, "could not reach the node's agent") {
		t.Fatalf("Unavailable → %d %s %q", st, code, msg)
	}
	st, code, msg = agentFailure(status.Error(codes.DeadlineExceeded, "context deadline exceeded"))
	if st != http.StatusServiceUnavailable || code != codeNodeUnreachable || !strings.Contains(msg, "may still be completing on the node") {
		t.Fatalf("DeadlineExceeded → %d %s %q", st, code, msg)
	}
	// No client at all is the node being unreachable too, however the error
	// travels (reconcileNode and applyConfig only return it up).
	st, code, _ = agentFailure(&nodeclient.ClientError{Err: errors.New("no tunnel transport")})
	if st != http.StatusServiceUnavailable || code != codeNodeUnreachable {
		t.Fatalf("ClientError → %d %s, want 503 node_unreachable", st, code)
	}
}
