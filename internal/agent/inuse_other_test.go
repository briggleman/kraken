//go:build !windows

package agent

import (
	"io/fs"
	"syscall"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// inUseErrno is this OS's "another process holds the file" error, for the
// OS-agnostic tests in grpcerrors_test.go.
var inUseErrno error = syscall.EBUSY

func TestUnixErrnoClassification(t *testing.T) {
	cases := []struct {
		name  string
		errno syscall.Errno
		want  codes.Code
	}{
		{"EBUSY", syscall.EBUSY, codes.FailedPrecondition},
		{"ETXTBSY", syscall.ETXTBSY, codes.FailedPrecondition},
		{"EACCES", syscall.EACCES, codes.PermissionDenied},
		{"EPERM", syscall.EPERM, codes.PermissionDenied},
		{"ENOENT", syscall.ENOENT, codes.NotFound},
		{"EEXIST", syscall.EEXIST, codes.AlreadyExists},
		// Go's own Errno.Is maps a non-empty folder onto fs.ErrExist.
		{"ENOTEMPTY", syscall.ENOTEMPTY, codes.AlreadyExists},
		{"ENOSPC", syscall.ENOSPC, codes.Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &fs.PathError{Op: "remove", Path: "/data/x", Err: tc.errno}
			if got := status.Code(classifyError(err)); got != tc.want {
				t.Fatalf("%s classified as %s, want %s", tc.name, got, tc.want)
			}
		})
	}
	if osInUse(nil) {
		t.Fatal("nil is not in use")
	}
}
