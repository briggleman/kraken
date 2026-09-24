package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Every runtime failure crosses the gRPC boundary with the code its cause
// deserves, and the Agent's own words intact. Before this, all of them were
// codes.Unknown, and the Panel answered every one with the same 502 (#352).
func TestClassifyError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"missing file", &fs.PathError{Op: "remove", Path: "x", Err: fs.ErrNotExist}, codes.NotFound},
		{"wrapped missing file", fmt.Errorf("fake: delete /data/x: %w", fs.ErrNotExist), codes.NotFound},
		{"permission", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission}, codes.PermissionDenied},
		{"already exists", &fs.PathError{Op: "mkdir", Path: "x", Err: fs.ErrExist}, codes.AlreadyExists},
		{"in use (sentinel)", fmt.Errorf("fake: %w", ErrInUse), codes.FailedPrecondition},
		{"path escape", badPath("docker: path %q escapes %s", "../x", "/data"), codes.InvalidArgument},
		{"deadline", fmt.Errorf("docker: stop: %w", context.DeadlineExceeded), codes.DeadlineExceeded},
		{"canceled", fmt.Errorf("docker: stop: %w", context.Canceled), codes.Canceled},
		{"unrecognised", errors.New("docker daemon said something odd"), codes.Unknown},
		// Already typed: the RPCs that pick their own codes meant them.
		{"typed status", status.Error(codes.FailedPrecondition, "self-update is not available"), codes.FailedPrecondition},
		{"typed Unavailable", status.Error(codes.Unavailable, "fake: channel gone"), codes.Unavailable},
		// A typed status wins over what it wraps.
		{"wrapped status", fmt.Errorf("outer: %w", status.Error(codes.Aborted, "inner")), codes.Aborted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyError(tc.err)
			st, ok := status.FromError(got)
			if !ok {
				t.Fatalf("classifyError(%v) = %v, not a gRPC status", tc.err, got)
			}
			if st.Code() != tc.want {
				t.Fatalf("classifyError(%v) code = %s, want %s", tc.err, st.Code(), tc.want)
			}
			if _, typed := status.FromError(tc.err); typed {
				return // passed through as it came
			}
			// The cause is carried, not flattened: the in-use sentence adds to
			// the text, everything else passes it through as it was.
			if tc.want == codes.FailedPrecondition {
				if !strings.Contains(st.Message(), "in use") {
					t.Fatalf("in-use message %q does not say so", st.Message())
				}
			} else if !strings.Contains(tc.err.Error(), st.Message()) || st.Message() == "" {
				t.Fatalf("message %q lost the cause %q", st.Message(), tc.err.Error())
			}
		})
	}
	if classifyError(nil) != nil {
		t.Fatal("classifyError(nil) must stay nil")
	}
}

// The OS-level in-use errors classify as FailedPrecondition, and the message
// leads with the logical path, so the sentence the operator reads names the
// file that is held.
func TestClassifyErrorInUseNamesThePath(t *testing.T) {
	d := newFileOpsRuntime(t)
	host := filepath.Join(d.localDir("s1"), "Saves", "world.sav")
	err := d.fileErr("s1", "delete", "/data/Saves/world.sav", "",
		&fs.PathError{Op: "remove", Path: host, Err: inUseErrno})
	st := status.Convert(classifyError(err))
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("code = %s, want FailedPrecondition", st.Code())
	}
	if !strings.HasPrefix(st.Message(), "/data/Saves/world.sav is in use by another process") {
		t.Fatalf("message %q does not lead with the logical path", st.Message())
	}
	if strings.Contains(st.Message(), d.dataDir) {
		t.Fatalf("message %q names the host path", st.Message())
	}
}

// The mutating file ops render their failures against the logical path, as
// the stat family already did (TestStatErrorsDoNotLeakTheHostPath): an
// *fs.PathError or *os.LinkError from mkdir/write/remove/rename/walk carries the
// resolved HOST path, and whatever these return reaches an API client verbatim.
// The cause is not flattened on the way: each error still classifies at the
// gRPC boundary.
func TestMutatingFileOpErrorsDoNotLeakTheHostPath(t *testing.T) {
	d := newFileOpsRuntime(t)
	ctx := context.Background()
	const sid = "s1"
	if err := d.Create(ctx, mkSpec(sid)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A plain file where the ops below expect a folder: everything that
	// needs to descend through it fails at the OS.
	if err := d.WriteFile(ctx, sid, "blocker.txt", []byte("x")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	check := func(what, logical string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected an error", what)
		}
		if strings.Contains(err.Error(), d.dataDir) {
			t.Fatalf("%s: error names the host path (%q): %v", what, d.dataDir, err)
		}
		if !strings.Contains(err.Error(), logical) {
			t.Fatalf("%s: error does not name the logical path %q: %v", what, logical, err)
		}
		if st := status.Convert(classifyError(err)); strings.Contains(st.Message(), d.dataDir) {
			t.Fatalf("%s: the classified message names the host path: %s", what, st.Message())
		}
	}

	check("MakeDir", "blocker.txt/sub", d.MakeDir(ctx, sid, "blocker.txt/sub"))
	check("WriteFile", "blocker.txt/sub/f.ini", d.WriteFile(ctx, sid, "blocker.txt/sub/f.ini", []byte("x")))
	check("MovePath", "missing.txt", d.MovePath(ctx, sid, "missing.txt", "moved.txt"))
	check("CopyPath", "missing.txt", d.CopyPath(ctx, sid, "missing.txt", "copied.txt"))
	check("ZipFiles", "missing-dir", d.ZipFiles(ctx, sid, []string{"missing-dir"}, &bytes.Buffer{}))

	// A missing source is still a missing source once sanitised.
	if err := d.MovePath(ctx, sid, "missing.txt", "moved.txt"); status.Code(classifyError(err)) != codes.NotFound {
		t.Fatalf("a move of a missing file classified as %s, want NotFound", status.Code(classifyError(err)))
	}

	// Delete: the #352 case. Forcing a real delete failure is OS-specific.
	switch {
	case runtime.GOOS == "windows":
		// A handle open without FILE_SHARE_DELETE — what a running game holds
		// on its save — makes DeleteFile fail with a sharing violation.
		host := filepath.Join(d.localDir(sid), "held.sav")
		if err := os.WriteFile(host, []byte("save"), 0o644); err != nil {
			t.Fatalf("seed held file: %v", err)
		}
		f, err := os.Open(host)
		if err != nil {
			t.Fatalf("open held file: %v", err)
		}
		derr := d.DeletePaths(ctx, sid, []string{"held.sav"})
		f.Close()
		check("DeletePaths", "held.sav", derr)
		if c := status.Code(classifyError(derr)); c != codes.FailedPrecondition {
			t.Fatalf("deleting a held file classified as %s (%v), want FailedPrecondition", c, derr)
		}
	case os.Geteuid() != 0:
		// A folder the Agent may not write: its entries cannot be unlinked.
		dir := filepath.Join(d.localDir(sid), "ro")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		derr := d.DeletePaths(ctx, sid, []string{"ro/f"})
		check("DeletePaths", "ro/f", derr)
		if c := status.Code(classifyError(derr)); c != codes.PermissionDenied {
			t.Fatalf("a refused delete classified as %s (%v), want PermissionDenied", c, derr)
		}
	}
}

// A path that escapes the data dir, or names the data root where a file or
// folder is required, is refused as bad input — a 400 at the Panel — not as an
// unexplained failure.
func TestBadPathsClassifyAsInvalidArgument(t *testing.T) {
	d := newFileOpsRuntime(t)
	ctx := context.Background()
	const sid = "s1"
	if err := d.Create(ctx, mkSpec(sid)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for what, err := range map[string]error{
		"escape":         d.DeletePaths(ctx, sid, []string{"/etc/passwd"}),
		"move data root": d.MovePath(ctx, sid, "/data", "/data/x"),
		"copy onto root": d.CopyPath(ctx, sid, "/data/a", "/data"),
		"read a folder":  func() error { _, _, _, _, e := d.ReadFile(ctx, sid, "/data", 1); return e }(),
	} {
		if !errors.Is(err, ErrBadPath) {
			t.Fatalf("%s: %v is not ErrBadPath", what, err)
		}
		if c := status.Code(classifyError(err)); c != codes.InvalidArgument {
			t.Fatalf("%s: classified as %s, want InvalidArgument", what, c)
		}
	}
}

// A restore failure keeps its context ("stopped at …") but not the host path.
func TestScrubbedKeepsTheChainButNotTheHostPath(t *testing.T) {
	d := newFileOpsRuntime(t)
	host := filepath.Join(d.localDir("s1"), "Saves")
	inner := &fs.PathError{Op: "rename", Path: host, Err: fs.ErrPermission}
	err := d.scrubbed("s1", fmt.Errorf("docker: restore stopped at %q: %w", "Saves", inner))
	if strings.Contains(err.Error(), d.dataDir) {
		t.Fatalf("scrubbed error names the host path: %v", err)
	}
	if !strings.Contains(err.Error(), "restore stopped at") {
		t.Fatalf("scrubbed error lost its context: %v", err)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("scrubbed error lost its cause: %v", err)
	}
	if d.scrubbed("s1", nil) != nil {
		t.Fatal("scrubbed(nil) must stay nil")
	}
}
