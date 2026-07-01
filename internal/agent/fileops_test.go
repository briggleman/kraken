package agent

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// newFileOpsRuntime builds a DockerRuntime wired only for native file ops (no
// Docker client needed) rooted at a temp data dir.
func newFileOpsRuntime(t *testing.T) *DockerRuntime {
	t.Helper()
	return &DockerRuntime{
		dataDir:  t.TempDir(),
		osType:   "linux",
		backups:  &localBackupTarget{dir: t.TempDir()},
		specs:    map[string]*agentpb.ServerSpec{},
		monitors: map[string]*monitor{},
	}
}

func read(t *testing.T, d *DockerRuntime, sid, p string) string {
	t.Helper()
	b, _, _, _, err := d.ReadFile(context.Background(), sid, p, 1<<20)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", p, err)
	}
	return string(b)
}

func TestNativeFileOps(t *testing.T) {
	d := newFileOpsRuntime(t)
	ctx := context.Background()
	const sid = "s1"
	if err := d.Create(ctx, mkSpec(sid)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Write + read, with size/binary metadata.
	if err := d.WriteFile(ctx, sid, "a.txt", []byte("hello")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if b, size, trunc, bin, err := d.ReadFile(ctx, sid, "a.txt", 1<<20); err != nil || string(b) != "hello" || size != 5 || trunc || bin {
		t.Fatalf("ReadFile a.txt = %q size=%d trunc=%v bin=%v err=%v", b, size, trunc, bin, err)
	}

	// Nested dir + file.
	if err := d.MakeDir(ctx, sid, "sub"); err != nil {
		t.Fatalf("MakeDir: %v", err)
	}
	if err := d.WriteFile(ctx, sid, "sub/b.txt", []byte("world")); err != nil {
		t.Fatalf("WriteFile nested: %v", err)
	}

	// List root.
	ents, err := d.ListFiles(ctx, sid, ".")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	got := map[string]bool{}
	for _, e := range ents {
		got[e.Name] = e.IsDir
	}
	if isDir, ok := got["a.txt"]; !ok || isDir {
		t.Errorf("expected a.txt file in listing, got %+v", got)
	}
	if isDir, ok := got["sub"]; !ok || !isDir {
		t.Errorf("expected sub dir in listing, got %+v", got)
	}

	// Recursive directory copy (the case that needs no shell tooling).
	if err := d.CopyPath(ctx, sid, "sub", "sub2"); err != nil {
		t.Fatalf("CopyPath dir: %v", err)
	}
	if v := read(t, d, sid, "sub2/b.txt"); v != "world" {
		t.Fatalf("copied sub2/b.txt = %q", v)
	}

	// Move (rename) — source gone, dest present.
	if err := d.MovePath(ctx, sid, "a.txt", "moved.txt"); err != nil {
		t.Fatalf("MovePath: %v", err)
	}
	if v := read(t, d, sid, "moved.txt"); v != "hello" {
		t.Fatalf("moved.txt = %q", v)
	}
	if _, _, _, _, err := d.ReadFile(ctx, sid, "a.txt", 1<<20); err == nil {
		t.Fatal("a.txt should not exist after move")
	}

	// Zip a directory.
	var zbuf bytes.Buffer
	if err := d.ZipFiles(ctx, sid, []string{"sub2"}, &zbuf); err != nil {
		t.Fatalf("ZipFiles: %v", err)
	}
	if zbuf.Len() == 0 {
		t.Fatal("zip is empty")
	}

	// Delete file + dir.
	if err := d.DeletePaths(ctx, sid, []string{"moved.txt", "sub2"}); err != nil {
		t.Fatalf("DeletePaths: %v", err)
	}
	if _, _, _, _, err := d.ReadFile(ctx, sid, "moved.txt", 1<<20); err == nil {
		t.Fatal("moved.txt should be deleted")
	}

	// Path-traversal is rejected.
	if _, err := d.safePath("../../etc/passwd"); err == nil {
		t.Fatal("expected traversal rejection")
	}
}

func TestNativeBackupRoundTrip(t *testing.T) {
	d := newFileOpsRuntime(t)
	ctx := context.Background()
	const sid = "s2"
	if err := d.Create(ctx, mkSpec(sid)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := d.WriteFile(ctx, sid, "world/level.dat", []byte("savedata")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	bi, err := d.CreateBackup(ctx, sid, "", "snap")
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	// Backups are asynchronous: wait for the archive to be written before restore.
	waitBackupReady(t, d, sid, bi.Id)
	if err := d.DeletePaths(ctx, sid, []string{"world"}); err != nil {
		t.Fatalf("DeletePaths: %v", err)
	}
	if err := d.RestoreBackup(ctx, sid, "", bi.Id); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if v := read(t, d, sid, "world/level.dat"); v != "savedata" {
		t.Fatalf("restored level.dat = %q", v)
	}
}

func mkSpec(serverID string) *agentpb.ServerSpec {
	return &agentpb.ServerSpec{ServerId: serverID}
}

// waitBackupReady polls ListBackups until the async backup reaches READY (or fails).
func waitBackupReady(t *testing.T, d *DockerRuntime, sid, id string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		list, err := d.ListBackups(context.Background(), sid, "")
		if err != nil {
			t.Fatalf("ListBackups: %v", err)
		}
		for _, b := range list {
			if b.Id != id {
				continue
			}
			switch b.State {
			case agentpb.BackupState_BACKUP_STATE_READY:
				return
			case agentpb.BackupState_BACKUP_STATE_FAILED:
				t.Fatalf("backup %s failed", id)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("backup %s not ready in time", id)
}
