package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

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

// ErrInUse marks a failure because another process holds the file — on Windows
// a sharing or lock violation, on Linux EBUSY/ETXTBSY. It is exported so a
// runtime that is not backed by the real filesystem (the fake) can report one
// the way the Docker runtime does. See isInUse for the OS-level cases.
var ErrInUse = errors.New("in use by another process")

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
	return resp, classifyError(err)
}

func classifyStream(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	return classifyError(handler(srv, ss))
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
// An error that is already a gRPC status passes through untouched: the RPCs
// that choose their own codes (self-update, cert rotation, telemetry) meant
// them.
func classifyError(err error) error {
	if err == nil {
		return nil
	}
	if st, ok := status.FromError(err); ok {
		return st.Err()
	}
	msg := err.Error()
	switch {
	case errors.Is(err, ErrBadPath):
		return status.Error(codes.InvalidArgument, msg)
	// In-use is checked before permission on purpose: it is the one an operator
	// can act on (stop the server, retry), so it must not be swallowed by the
	// broader class.
	case isInUse(err):
		return status.Error(codes.FailedPrecondition, inUseMessage(err))
	case errors.Is(err, fs.ErrNotExist):
		return status.Error(codes.NotFound, msg)
	case errors.Is(err, fs.ErrPermission):
		return status.Error(codes.PermissionDenied, msg)
	case errors.Is(err, fs.ErrExist):
		return status.Error(codes.AlreadyExists, msg)
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, msg)
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, msg)
	}
	return status.Error(codes.Unknown, msg)
}

// isInUse reports whether err means another process holds the file.
func isInUse(err error) bool {
	return errors.Is(err, ErrInUse) || osInUse(err)
}

// inUseMessage leads with the logical path when the failure came from a file
// operation that knows it, so the sentence the operator reads names the file
// that is held — the OS's text alone ("The process cannot access the file
// because it is being used by another process.") does not say which.
func inUseMessage(err error) string {
	var fo *fileOpError
	if errors.As(err, &fo) && fo.path != "" {
		op := fo.op
		if op == "" {
			op = "open" // the stat/read/download family
		}
		// Windows ends every system message with a full stop, which reads badly
		// inside the parenthesis.
		return fmt.Sprintf("%s is in use by another process (%s: %s)", fo.path, op, strings.TrimSuffix(fo.causeMsg, "."))
	}
	return "a file is in use by another process (" + err.Error() + ")"
}
