package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/docker/docker/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrBadPath marks a request the Agent refuses because of the path it names,
// not because of anything on disk: a path that escapes the server's data dir,
// an attempt to move or copy the data root itself, a directory handed to an
// operation that needs a file. It reaches the Panel as codes.InvalidArgument,
// which the Panel answers with a 400 — the caller asked for something that can
// never succeed, and retrying it will not help.
var ErrBadPath = errors.New("bad path")

// ServerOptions are the options every Agent gRPC server is built with — the
// direct mTLS listener in cmd/agent and each reverse-tunnel session — so the
// two transports answer the Panel identically. Callers append their own
// (credentials, for the direct listener).
func ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(classifyUnary),
		grpc.ChainStreamInterceptor(classifyStream),
	}
}

func classifyUnary(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	resp, err := handler(ctx, req)
	return resp, classifyError(ctx, err)
}

func classifyStream(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	return classifyError(ss.Context(), handler(srv, ss))
}

// classifyError gives a runtime failure the gRPC code its cause deserves.
//
// Without this every plain error a runtime returned — a locked save file, a
// path that does not exist, a directory the Agent may not write — crossed the
// wire as codes.Unknown with only its text, and the Panel could do nothing but
// answer all of them the same way (a 502, whose body Cloudflare then threw
// away: #352). The code is what lets the Panel pick a status that says what
// happened and that survives the edge; the message is carried unchanged, so
// the operator still reads the OS's own words ("Access is denied.").
//
// The filesystem classes (NotFound, PermissionDenied, AlreadyExists, in use)
// are given ONLY to a failure a file operation produced — one whose chain holds
// a *fileOpError. errors.Is alone cannot tell them apart from anything else
// that happens to wrap an fs sentinel: a Docker engine that is not running on a
// Windows node is an *os.PathError{ERROR_FILE_NOT_FOUND} on its named pipe, and
// docker.sock refusing the Agent is EACCES — neither is a file the operator
// asked about, and answering a start with "404 not found" or "check the agent
// can write there" would send them looking in the wrong place.
//
// Likewise DeadlineExceeded and Canceled mean THIS RPC's context ended. A
// deadline error inside the handler's own result while that context is still
// live is a timeout the Agent hit talking to something else (an SMB share, an
// image registry) — Go's net timeout errors satisfy errors.Is(…,
// context.DeadlineExceeded) — and reporting it as the node not answering would
// be false. It stays Unknown, message kept.
//
// An error that is already a gRPC status passes through untouched: the RPCs
// that choose their own codes (self-update, cert rotation, telemetry) meant
// them.
func classifyError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if st, ok := status.FromError(err); ok {
		return st.Err()
	}
	msg := err.Error()
	if cerr := ctx.Err(); cerr != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
		return status.Error(status.FromContextError(cerr).Code(), msg)
	}
	if errors.Is(err, ErrBadPath) {
		return status.Error(codes.InvalidArgument, msg)
	}
	// The node answered; its container engine did not. Internal, with a plain
	// sentence, so the Panel shows it as the node's error and not as a file one.
	if client.IsErrConnectionFailed(err) {
		return status.Error(codes.Internal, "docker: cannot connect to the daemon: "+msg)
	}
	var fo *fileOpError
	if !errors.As(err, &fo) {
		return status.Error(codes.Unknown, msg)
	}
	switch {
	// In-use is checked before permission on purpose: it is the one an operator
	// can act on (stop the server, retry), so it must not be swallowed by the
	// broader class.
	case isInUse(err):
		return status.Error(codes.FailedPrecondition, inUseMessage(fo, msg))
	// A delete that finds its folder not empty (Go reads ENOTEMPTY and
	// ERROR_DIR_NOT_EMPTY as fs.ErrExist) lost a race with a process writing
	// into it, or holds a delete-pending child on Windows: the folder is in use.
	// For a rename or a mkdir, "exists" means exactly that.
	case fo.op == "delete" && errors.Is(err, fs.ErrExist):
		return status.Error(codes.FailedPrecondition, inUseMessage(fo, msg))
	case errors.Is(err, fs.ErrNotExist):
		return status.Error(codes.NotFound, msg)
	case errors.Is(err, fs.ErrPermission):
		return status.Error(codes.PermissionDenied, msg)
	case errors.Is(err, fs.ErrExist):
		return status.Error(codes.AlreadyExists, msg)
	}
	return status.Error(codes.Unknown, msg)
}

// isInUse reports whether err means another process holds the file. See
// osInUse for the per-OS cases.
func isInUse(err error) bool {
	return osInUse(err)
}

// inUseMessage leads with the logical path when the file operation knows it,
// so the sentence the operator reads names the file that is held — the OS's
// text alone ("The process cannot access the file because it is being used by
// another process.") does not say which.
func inUseMessage(fo *fileOpError, msg string) string {
	if fo.path == "" {
		return "a file is in use by another process (" + msg + ")"
	}
	op := fo.op
	if op == "" {
		op = "open" // the stat/read/download family
	}
	// Windows ends every system message with a full stop, which reads badly
	// inside the parenthesis.
	return fmt.Sprintf("%s is in use by another process (%s: %s)", fo.path, op, strings.TrimSuffix(fo.causeMsg, "."))
}
