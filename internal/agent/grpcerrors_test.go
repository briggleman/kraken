package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/docker/docker/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// fileOp marks err the way the Docker runtime's file operations do, which is
// what makes it eligible for the filesystem codes.
func fileOp(op string, err error) error {
	return fileFailure(op, "/data/x", err)
}

// Every runtime failure crosses the gRPC boundary with the code its cause
// deserves, and the Agent's own words intact. Before this, all of them were
// codes.Unknown, and the Panel answered every one with the same 502 (#352).
//
// The filesystem codes go to file operations ONLY. The same fs sentinels turn
// up in failures that have nothing to do with a file the operator asked about
// — a Docker engine that is down on Windows is ERROR_FILE_NOT_FOUND on its
// named pipe, docker.sock refusing the Agent is EACCES — and a start that
// answered "404 not found" would send the operator looking in the wrong place.
func TestClassifyError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"missing file", fileOp("delete", &fs.PathError{Op: "remove", Path: "x", Err: fs.ErrNotExist}), codes.NotFound},
		{"wrapped missing file", fileOp("delete", fmt.Errorf("fake: delete /data/x: %w", fs.ErrNotExist)), codes.NotFound},
		{"permission", fileOp("read", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission}), codes.PermissionDenied},
		{"mkdir onto an existing path", fileOp("mkdir", &fs.PathError{Op: "mkdir", Path: "x", Err: fs.ErrExist}), codes.AlreadyExists},
		{"move onto an existing path", fileOp("move", &fs.PathError{Op: "rename", Path: "x", Err: fs.ErrExist}), codes.AlreadyExists},
		// A delete whose folder is "not empty" (Go: fs.ErrExist) lost a race
		// with a writer, or holds a delete-pending child: it is in use.
		{"delete of a folder still being written", fileOp("delete", &fs.PathError{Op: "remove", Path: "x", Err: fs.ErrExist}), codes.FailedPrecondition},
		{"held file", fileOp("delete", &fs.PathError{Op: "remove", Path: "x", Err: inUseErrno}), codes.FailedPrecondition},
		{"path escape", badPath("docker: path %q escapes %s", "../x", "/data"), codes.InvalidArgument},
		// Not file operations: the same sentinels, no file codes.
		{"docker engine pipe missing", &fs.PathError{Op: "open", Path: `\\.\pipe\docker_engine`, Err: fs.ErrNotExist}, codes.Unknown},
		{"docker.sock refused", &os.PathError{Op: "dial", Path: "/var/run/docker.sock", Err: fs.ErrPermission}, codes.Unknown},
		{"bare exists", fmt.Errorf("docker: create: %w", fs.ErrExist), codes.Unknown},
		// A timeout inside the handler while the RPC is alive is the Agent's
		// own wait on something else (an SMB share, a registry), not the node
		// failing to answer.
		{"agent-side timeout", fmt.Errorf("smb: dial nas:445: %w", os.ErrDeadlineExceeded), codes.Unknown},
		{"agent-side deadline", fmt.Errorf("docker: pull: %w", context.DeadlineExceeded), codes.Unknown},
		{"agent-side cancel", fmt.Errorf("docker: pull: %w", context.Canceled), codes.Unknown},
		{"unrecognised", errors.New("docker daemon said something odd"), codes.Unknown},
		// Already typed: the RPCs that pick their own codes meant them.
		{"typed status", status.Error(codes.FailedPrecondition, "self-update is not available"), codes.FailedPrecondition},
		{"typed Unavailable", status.Error(codes.Unavailable, "fake: channel gone"), codes.Unavailable},
		// A typed status wins over what it wraps.
		{"wrapped status", fmt.Errorf("outer: %w", status.Error(codes.Aborted, "inner")), codes.Aborted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyError(context.Background(), tc.err)
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
	if classifyError(context.Background(), nil) != nil {
		t.Fatal("classifyError(nil) must stay nil")
	}
}

// DeadlineExceeded and Canceled mean the RPC's OWN context ended — and then
// they are that, whatever the handler wrapped around them.
func TestClassifyErrorDeadlineOnlyWhenTheRPCContextEnded(t *testing.T) {
	expired, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-expired.Done()
	err := fmt.Errorf("docker: stop: %w", context.DeadlineExceeded)
	st := status.Convert(classifyError(expired, err))
	if st.Code() != codes.DeadlineExceeded || !strings.Contains(st.Message(), "docker: stop") {
		t.Fatalf("expired RPC classified as %s %q, want DeadlineExceeded with the message", st.Code(), st.Message())
	}
	canceled, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if c := status.Code(classifyError(canceled, fmt.Errorf("x: %w", context.Canceled))); c != codes.Canceled {
		t.Fatalf("canceled RPC classified as %s, want Canceled", c)
	}
	// A live context with a non-deadline error stays what it is.
	if c := status.Code(classifyError(expired, errors.New("boom"))); c != codes.Unknown {
		t.Fatalf("an unrelated error on an expired RPC classified as %s, want Unknown", c)
	}
}

// A container engine the Agent cannot reach is the node's own failure, said
// plainly — not a file one, and not "the node did not answer" (it did).
func TestClassifyErrorDockerConnectionFailure(t *testing.T) {
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://127.0.0.1:1"))
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, perr := cli.Ping(ctx)
	if !client.IsErrConnectionFailed(perr) {
		t.Fatalf("precondition: Ping against a closed port = %v, not a connection failure", perr)
	}
	st := status.Convert(classifyError(context.Background(), fmt.Errorf("docker: start: %w", perr)))
	if st.Code() != codes.Internal {
		t.Fatalf("docker connection failure classified as %s, want Internal", st.Code())
	}
	if !strings.HasPrefix(st.Message(), "docker: cannot connect to the daemon") {
		t.Fatalf("message %q does not say the daemon is unreachable", st.Message())
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
	st := status.Convert(classifyError(context.Background(), err))
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
		if st := status.Convert(classifyError(context.Background(), err)); strings.Contains(st.Message(), d.dataDir) {
			t.Fatalf("%s: the classified message names the host path: %s", what, st.Message())
		}
	}

	check("MakeDir", "blocker.txt/sub", d.MakeDir(ctx, sid, "blocker.txt/sub"))
	check("WriteFile", "blocker.txt/sub/f.ini", d.WriteFile(ctx, sid, "blocker.txt/sub/f.ini", []byte("x")))
	check("MovePath", "missing.txt", d.MovePath(ctx, sid, "missing.txt", "moved.txt"))
	check("CopyPath", "missing.txt", d.CopyPath(ctx, sid, "missing.txt", "copied.txt"))
	check("ZipFiles", "missing-dir", d.ZipFiles(ctx, sid, []string{"missing-dir"}, &bytes.Buffer{}))

	// A missing source is still a missing source once sanitised.
	if err := d.MovePath(ctx, sid, "missing.txt", "moved.txt"); status.Code(classifyError(context.Background(), err)) != codes.NotFound {
		t.Fatalf("a move of a missing file classified as %s, want NotFound", status.Code(classifyError(context.Background(), err)))
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
		if c := status.Code(classifyError(context.Background(), derr)); c != codes.FailedPrecondition {
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
		if c := status.Code(classifyError(context.Background(), derr)); c != codes.PermissionDenied {
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
		if c := status.Code(classifyError(context.Background(), err)); c != codes.InvalidArgument {
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

// archiveStub is a backup store whose archive is at a host path (the node's
// backup dir, or a share) and fails the way a real one does: at Open, or at
// the first read.
type archiveStub struct {
	path    string
	openErr bool
}

func (a *archiveStub) Put(context.Context, string, string, io.Reader, int64) error { return nil }
func (a *archiveStub) List(context.Context, string) ([]*agentpb.BackupInfo, error) {
	return nil, nil
}
func (a *archiveStub) Delete(context.Context, string, string) error { return nil }
func (a *archiveStub) Kind() string                                 { return "local" }
func (a *archiveStub) Open(context.Context, string, string) (io.ReadCloser, error) {
	if a.openErr {
		return nil, &fs.PathError{Op: "open", Path: a.path, Err: fs.ErrPermission}
	}
	return io.NopCloser(failingReader{&fs.PathError{Op: "read", Path: a.path, Err: syscall.EIO}}), nil
}

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

// A restore names its archive by the backup's id, never by where the archive
// lives: KRAKEN_BACKUP_DIR, a node-configured backup dir, a share. Both the
// open and the reads that follow (gzip's header, every tar entry) fail with an
// *fs.PathError carrying that location.
func TestRestoreErrorsDoNotLeakTheArchivePath(t *testing.T) {
	const sid, id = "s1", "1726000000000__nightly"
	// A share path is named by no local root, so only rendering the store's
	// own failure by the backup's id keeps it out; the local one is caught
	// either way.
	share := `\\nas\kraken-backups\` + sid + `\` + id + ".tar.gz"
	for _, tc := range []struct {
		name    string
		local   bool
		openErr bool
	}{
		{"local open", true, true},
		{"local read", true, false},
		{"share open", false, true},
		{"share read", false, false},
	} {
		d := newFileOpsRuntime(t)
		d.backupDir = t.TempDir()
		archive := share
		if tc.local {
			archive = filepath.Join(d.backupDir, sid, id+".tar.gz")
		}
		d.backups = &archiveStub{path: archive, openErr: tc.openErr}
		err := d.RestoreBackup(context.Background(), sid, "", id)
		if err == nil {
			t.Fatalf("%s: expected an error", tc.name)
		}
		for _, leak := range []string{d.backupDir, d.dataDir, "kraken-backups"} {
			if strings.Contains(err.Error(), leak) {
				t.Fatalf("%s: restore failure names a host path (%s): %v", tc.name, leak, err)
			}
		}
		if !strings.Contains(err.Error(), id) {
			t.Fatalf("%s: restore failure does not name the backup: %v", tc.name, err)
		}
	}

	// A backup dir the Panel configured is scrubbed too, up to its first
	// per-server token.
	d := newFileOpsRuntime(t)
	d.nodeCfg = &agentpb.NodeConfig{BackupDir: "/mnt/kraken-backups/{{SLUG}}"}
	if got := d.scrubHostPaths("s1", "open /mnt/kraken-backups/palworld/s1/x.tar.gz: denied"); strings.Contains(got, "/mnt/kraken-backups") {
		t.Fatalf("configured backup dir survived scrubbing: %s", got)
	}
}
