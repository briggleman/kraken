package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// #360: the permanent delete of a retired server deletes its backup archives —
// but only where they are provably its own. The zero-config layout keeps one
// directory per server; every other target keeps every server's archives side
// by side, where an archive's name does not say whose it is, so those are kept
// and named.

// seedArchives writes one archive for each server under root/<server>/ (flat
// false) or straight into root (flat true), and returns their paths.
func seedArchives(t *testing.T, root string, flat bool, servers ...string) []string {
	t.Helper()
	var out []string
	for _, id := range servers {
		dir := filepath.Join(root, id)
		if flat {
			dir = root
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "1700000000000__"+id+".tar.gz")
		if err := os.WriteFile(p, []byte("archive"), 0o644); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestPurgeBackups_NamespacedLayoutDeletesOnlyThatServersArchives(t *testing.T) {
	root := t.TempDir()
	d := &DockerRuntime{backupJobs: map[string]*agentpb.BackupInfo{}}
	d.backups = &localBackupTarget{dir: root}
	paths := seedArchives(t, root, false, "sv-gone", "sv-neighbour")

	kept, err := d.PurgeBackups(context.Background(), "sv-gone")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if kept != "" {
		t.Fatalf("kept = %q, want nothing kept on the zero-config layout", kept)
	}
	if exists(paths[0]) || exists(filepath.Join(root, "sv-gone")) {
		t.Fatal("the retired server's archives (or their directory) survived the purge")
	}
	if !exists(paths[1]) {
		t.Fatal("a neighbouring server's archive was deleted")
	}
}

func TestPurgeBackups_FlatTargetsAreKeptAndNamed(t *testing.T) {
	cases := []struct {
		name    string
		primary func(dir string) backupTarget
		mirror  backupTarget
		want    string
	}{
		{"configured directory", func(dir string) backupTarget { return &localBackupTarget{dir: dir, flat: true} }, nil,
			"the configured backup directory"},
		{"network share", func(dir string) backupTarget {
			return &shareBackupTarget{localBackupTarget{dir: dir, flat: true}}
		}, nil, "the network share"},
		{"zero-config primary, sftp mirror", func(dir string) backupTarget { return &localBackupTarget{dir: dir} },
			&sftpBackupTarget{}, "the SFTP target (mirror)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			d := &DockerRuntime{backupJobs: map[string]*agentpb.BackupInfo{}}
			d.backups = tc.primary(root)
			d.replicate = tc.mirror
			flat := tc.mirror == nil
			paths := seedArchives(t, root, flat, "sv-gone", "sv-neighbour")

			kept, err := d.PurgeBackups(context.Background(), "sv-gone")
			if err != nil {
				t.Fatalf("purge: %v", err)
			}
			if kept != tc.want {
				t.Fatalf("kept = %q, want %q", kept, tc.want)
			}
			if !exists(paths[1]) {
				t.Fatal("a neighbouring server's archive was deleted")
			}
			if flat && !exists(paths[0]) {
				t.Fatal("an archive on a flat target was deleted; it cannot be attributed to one server")
			}
			if !flat && exists(paths[0]) {
				t.Fatal("the namespaced primary's archives were kept because of the mirror")
			}
		})
	}
}

// A flat target is kept even when something under it is named after the
// server — the flat rule, not the path arithmetic, is what keeps it.
func TestPurgeBackups_FlatTargetKeepsAFolderNamedAfterTheServer(t *testing.T) {
	root := t.TempDir()
	d := &DockerRuntime{backupJobs: map[string]*agentpb.BackupInfo{}}
	d.backups = &localBackupTarget{dir: root, flat: true}
	// Another game's archives, filed by the operator under a folder that
	// shares the retired server's id.
	nested := seedArchives(t, root, false, "sv-gone")
	kept, err := d.PurgeBackups(context.Background(), "sv-gone")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if kept != "the configured backup directory" || !exists(nested[0]) {
		t.Fatalf("kept = %q, archive left = %v; a flat target must be kept whole", kept, exists(nested[0]))
	}
}

func TestPurgeBackups_RefusesAnIDThatIsNotOneDirectory(t *testing.T) {
	root := t.TempDir()
	d := &DockerRuntime{backupJobs: map[string]*agentpb.BackupInfo{}}
	d.backups = &localBackupTarget{dir: root}
	paths := seedArchives(t, root, false, "sv-a")
	for _, id := range []string{"", ".", "..", "../sv-a", `a\b`} {
		if _, err := d.PurgeBackups(context.Background(), id); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("purge %q: err %v, want InvalidArgument", id, err)
		}
	}
	if !exists(paths[0]) {
		t.Fatal("a refused purge deleted something")
	}
}

// The Service runs the purge only when asked, after the removal, and says so
// on the response — which is how a Panel tells this Agent from an older one
// that ignored the field.
func TestRemoveServer_DeleteBackupsIsReportedHandled(t *testing.T) {
	rt := NewFakeRuntime("n", "linux", false, "test")
	svc := NewService(rt)
	ctx := context.Background()
	if _, err := rt.CreateBackup(ctx, "sv-1", "", "b", nil, nil); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.RemoveServer(ctx, &agentpb.RemoveServerRequest{ServerId: "sv-1", DeleteData: true})
	if err != nil {
		t.Fatal(err)
	}
	if resp.BackupsHandled || len(rt.Purges()) != 0 || len(rt.Backups("sv-1")) != 1 {
		t.Fatalf("a removal that did not ask purged: handled=%v purges=%v", resp.BackupsHandled, rt.Purges())
	}

	resp, err = svc.RemoveServer(ctx, &agentpb.RemoveServerRequest{ServerId: "sv-1", DeleteData: true, DeleteBackups: true})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.BackupsHandled || resp.BackupsKept != "" || len(rt.Backups("sv-1")) != 0 {
		t.Fatalf("purge: handled=%v kept=%q archives=%d, want handled, nothing kept, none left",
			resp.BackupsHandled, resp.BackupsKept, len(rt.Backups("sv-1")))
	}

	rt.SetSharedBackupTarget("the network share")
	if _, err := rt.CreateBackup(ctx, "sv-1", "", "b", nil, nil); err != nil {
		t.Fatal(err)
	}
	resp, err = svc.RemoveServer(ctx, &agentpb.RemoveServerRequest{ServerId: "sv-1", DeleteData: true, DeleteBackups: true})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.BackupsHandled || resp.BackupsKept != "the network share" || len(rt.Backups("sv-1")) != 1 {
		t.Fatalf("shared target: handled=%v kept=%q archives=%d, want the archive kept and named",
			resp.BackupsHandled, resp.BackupsKept, len(rt.Backups("sv-1")))
	}
}
