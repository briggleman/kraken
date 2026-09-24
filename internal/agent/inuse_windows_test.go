//go:build windows

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
var inUseErrno error = errSharingViolation

// The Win32 errors a running game's open files produce are "in use" — a 409
// the operator can act on — and ERROR_ACCESS_DENIED stays a permission
// failure, which is what Go's own Errno.Is maps it to.
func TestWindowsErrnoClassification(t *testing.T) {
	cases := []struct {
		name  string
		errno syscall.Errno
		want  codes.Code
	}{
		{"ERROR_SHARING_VIOLATION", 32, codes.FailedPrecondition},
		{"ERROR_LOCK_VIOLATION", 33, codes.FailedPrecondition},
		{"ERROR_ACCESS_DENIED", 5, codes.PermissionDenied},
		{"ERROR_FILE_NOT_FOUND", 2, codes.NotFound},
		{"ERROR_PATH_NOT_FOUND", 3, codes.NotFound},
		{"ERROR_ALREADY_EXISTS", 183, codes.AlreadyExists},
		// Go's own Errno.Is maps a non-empty folder onto fs.ErrExist.
		{"ERROR_DIR_NOT_EMPTY", 145, codes.AlreadyExists},
		{"ERROR_DISK_FULL", 112, codes.Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &fs.PathError{Op: "remove", Path: `C:\data\x`, Err: tc.errno}
			if got := status.Code(classifyError(err)); got != tc.want {
				t.Fatalf("%s classified as %s, want %s", tc.name, got, tc.want)
			}
		})
	}
	if osInUse(nil) {
		t.Fatal("nil is not in use")
	}
}
