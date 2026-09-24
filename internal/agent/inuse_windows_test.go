//go:build windows

package agent

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// inUseErrno is this OS's "another process holds the file" error, for the
// OS-agnostic tests in grpcerrors_test.go.
var inUseErrno error = errSharingViolation

// The Win32 errors a running game's open files produce are "in use" — a 409
// the operator can act on — and ERROR_ACCESS_DENIED stays a permission
// failure, which is what Go's own Errno.Is maps it to. All of these come from
// a file operation; see TestClassifyError for why that matters.
func TestWindowsErrnoClassification(t *testing.T) {
	cases := []struct {
		name  string
		op    string
		errno syscall.Errno
		want  codes.Code
	}{
		{"ERROR_SHARING_VIOLATION", "delete", 32, codes.FailedPrecondition},
		{"ERROR_LOCK_VIOLATION", "write", 33, codes.FailedPrecondition},
		{"ERROR_ACCESS_DENIED", "delete", 5, codes.PermissionDenied},
		{"ERROR_FILE_NOT_FOUND", "delete", 2, codes.NotFound},
		{"ERROR_PATH_NOT_FOUND", "mkdir", 3, codes.NotFound},
		{"ERROR_ALREADY_EXISTS", "mkdir", 183, codes.AlreadyExists},
		// Go's own Errno.Is maps a non-empty folder onto fs.ErrExist. On a
		// delete that is a folder something is still writing into (or holding
		// a delete-pending child in): in use. On a move it is a real collision.
		{"ERROR_DIR_NOT_EMPTY on delete", "delete", 145, codes.FailedPrecondition},
		{"ERROR_DIR_NOT_EMPTY on move", "move", 145, codes.AlreadyExists},
		{"ERROR_DISK_FULL", "write", 112, codes.Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := fileFailure(tc.op, "/data/x", &fs.PathError{Op: "remove", Path: `C:\data\x`, Err: tc.errno})
			if got := status.Code(classifyError(context.Background(), err)); got != tc.want {
				t.Fatalf("%s classified as %s, want %s", tc.name, got, tc.want)
			}
		})
	}
	if osInUse(nil) {
		t.Fatal("nil is not in use")
	}
}

// A byte-range lock — what a running game holds on the save it is writing —
// lets the file be stat'ed and opened but fails the read partway. That read
// error is an *os.PathError naming the host path, and it used to reach the
// operator verbatim from DownloadFile's copy loop; it is rendered against the
// logical path now, and still classifies as in use.
func TestDownloadFileLockedRangeDoesNotLeakTheHostPath(t *testing.T) {
	d := newFileOpsRuntime(t)
	ctx := context.Background()
	const sid = "s1"
	if err := d.Create(ctx, mkSpec(sid)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	host := filepath.Join(d.localDir(sid), "Saves", "world.sav")
	if err := os.MkdirAll(filepath.Dir(host), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(host, bytes.Repeat([]byte("s"), 4096), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	f, err := os.OpenFile(host, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1<<20, 0, ol); err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer func() { _ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1<<20, 0, ol) }()

	derr := d.DownloadFile(ctx, sid, "Saves/world.sav", &bytes.Buffer{})
	if derr == nil {
		t.Fatal("DownloadFile of a locked range: expected an error")
	}
	st := status.Convert(classifyError(ctx, derr))
	for _, m := range []string{derr.Error(), st.Message()} {
		if strings.Contains(m, d.dataDir) {
			t.Fatalf("the failure names the host path: %s", m)
		}
		if !strings.Contains(m, "world.sav") {
			t.Fatalf("the failure does not name the logical path: %s", m)
		}
	}
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("a locked save classified as %s (%v), want FailedPrecondition", st.Code(), derr)
	}
}
