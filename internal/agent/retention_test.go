package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

func putArchive(t *testing.T, tgt backupTarget, serverID, id string) {
	t.Helper()
	body := "archive-" + id
	if err := tgt.Put(context.Background(), serverID, id, strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("put %s: %v", id, err)
	}
}

func listIDs(t *testing.T, tgt backupTarget, serverID string) []string {
	t.Helper()
	list, err := tgt.List(context.Background(), serverID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ids := make([]string, len(list))
	for i, b := range list {
		ids[i] = b.Id
	}
	return ids
}

// Retention keeps the newest backupRetentionKeep archives and evicts the rest
// from the primary store AND the off-node mirror both — the promise the UI's
// eviction warning makes ("removes … on this node and its mirror").
func TestPruneBackupsEvictsOldestFromNodeAndMirror(t *testing.T) {
	primary := &localBackupTarget{dir: t.TempDir()}
	mirror := &localBackupTarget{dir: t.TempDir()}
	d := &DockerRuntime{backups: primary, replicate: mirror, backupJobs: map[string]*agentpb.BackupInfo{}}
	const serverID = "srv1"

	total := backupRetentionKeep + 2 // two over the cap
	for i := 1; i <= total; i++ {
		id := fmt.Sprintf("%d__nightly", i) // created_ms prefix i → deterministic order
		putArchive(t, primary, serverID, id)
		putArchive(t, mirror, serverID, id)
	}

	d.pruneBackups(context.Background(), serverID, "")

	for _, tgt := range []struct {
		name string
		t    backupTarget
	}{{"primary", primary}, {"mirror", mirror}} {
		ids := listIDs(t, tgt.t, serverID)
		if len(ids) != backupRetentionKeep {
			t.Fatalf("%s: kept %d archives, want %d", tgt.name, len(ids), backupRetentionKeep)
		}
		for _, id := range ids {
			if id == "1__nightly" || id == "2__nightly" {
				t.Errorf("%s: %s is the oldest and should have been evicted", tgt.name, id)
			}
		}
	}
}

// A failed backup captures nothing, so it never lands as an archive and never
// occupies a retention slot: a store already at the cap is left untouched.
func TestPruneBackupsNoopUnderCap(t *testing.T) {
	primary := &localBackupTarget{dir: t.TempDir()}
	d := &DockerRuntime{backups: primary, backupJobs: map[string]*agentpb.BackupInfo{}}
	const serverID = "srv1"

	for i := 1; i <= backupRetentionKeep; i++ {
		putArchive(t, primary, serverID, fmt.Sprintf("%d__nightly", i))
	}
	d.pruneBackups(context.Background(), serverID, "")
	if got := len(listIDs(t, primary, serverID)); got != backupRetentionKeep {
		t.Fatalf("kept %d, want %d — at the cap, nothing should be evicted", got, backupRetentionKeep)
	}
}
