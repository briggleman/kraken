//go:build !windows

package agent

import (
	"context"
	"io/fs"
	"syscall"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// inUseErrno is this OS's "another process holds the file" error, for the
// OS-agnostic tests in grpcerrors_test.go.
var inUseErrno error = syscall.EBUSY

// All of these come from a file operation; see TestClassifyError for why that
// matters.
func TestUnixErrnoClassification(t *testing.T) {
	cases := []struct {
		name  string
		op    string
		errno syscall.Errno
		want  codes.Code
	}{
		{"EBUSY", "delete", syscall.EBUSY, codes.FailedPrecondition},
		{"ETXTBSY", "write", syscall.ETXTBSY, codes.FailedPrecondition},
		{"EACCES", "delete", syscall.EACCES, codes.PermissionDenied},
		{"EPERM", "delete", syscall.EPERM, codes.PermissionDenied},
		{"ENOENT", "delete", syscall.ENOENT, codes.NotFound},
		{"EEXIST", "mkdir", syscall.EEXIST, codes.AlreadyExists},
		// Go's own Errno.Is maps a non-empty folder onto fs.ErrExist. On a
		// delete that is a folder something is still writing into: in use. On
		// a move it is a real collision.
		{"ENOTEMPTY on delete", "delete", syscall.ENOTEMPTY, codes.FailedPrecondition},
		{"ENOTEMPTY on move", "move", syscall.ENOTEMPTY, codes.AlreadyExists},
		{"ENOSPC", "write", syscall.ENOSPC, codes.Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := fileFailure(tc.op, "/data/x", &fs.PathError{Op: "remove", Path: "/data/x", Err: tc.errno})
			if got := status.Code(classifyError(context.Background(), err)); got != tc.want {
				t.Fatalf("%s classified as %s, want %s", tc.name, got, tc.want)
			}
		})
	}
	if osInUse(nil) {
		t.Fatal("nil is not in use")
	}
}
