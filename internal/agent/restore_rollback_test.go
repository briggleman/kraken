package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// An old Panel holds the unary call open under a 10-minute timeout and gives
// up on it; before the restore honoured its context that left the restore
// running to completion, and it still must — cancelling now would roll back a
// restore the old Agent would have finished.
func TestUnaryRestoreIgnoresTheCallersCancellation(t *testing.T) {
	const sid = "s-unary-detached"
	d, id := restoreFixture(t, sid,
		map[string]string{"savegame/a.db": "live-a"},
		dirEntry("savegame"),
		archiveEntry{name: "savegame/a.db", body: "archived-a"},
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the old Panel's deadline has already passed
	if _, err := NewService(d).RestoreBackup(ctx, &agentpb.RestoreBackupRequest{ServerId: sid, Id: id}); err != nil {
		t.Fatalf("unary RestoreBackup under a cancelled context: %v", err)
	}
	if v := liveRead(t, d, sid, "savegame/a.db"); v != "archived-a" {
		t.Errorf("savegame/a.db = %q; the unary restore must finish whatever its caller does", v)
	}
	noRestoreLeftovers(t, d, sid)
}

// A rollback that cannot remove a restored path which had no original leaves
// that path in place — it must not claim an original is "preserved beside it"
// when there never was one.
func TestRestoreRollbackNamesALeftoverHonestly(t *testing.T) {
	const sid = "s-leftover"
	d, id := restoreFixture(t, sid,
		map[string]string{"savegame/a.db": "live-a"},
		dirEntry("newdir"),
		archiveEntry{name: "newdir/x.db", body: "archived-x"},
		dirEntry("savegame"),
		archiveEntry{name: "savegame/a.db", body: "archived-a"},
	)
	origRename, origRemove := restoreRename, restoreRemoveAll
	t.Cleanup(func() { restoreRename, restoreRemoveAll = origRename, origRemove })
	restoreRename = func(oldpath, newpath string) error {
		// newdir sorts first and lands; savegame's move-aside then fails.
		if strings.HasSuffix(oldpath, string(os.PathSeparator)+"savegame") {
			return errors.New("forced sharing violation")
		}
		return origRename(oldpath, newpath)
	}
	restoreRemoveAll = func(p string) error {
		if strings.HasSuffix(p, string(os.PathSeparator)+"newdir") {
			return errors.New("forced: in use")
		}
		return origRemove(p)
	}
	err := d.RestoreBackup(context.Background(), sid, "", id)
	if err == nil {
		t.Fatal("RestoreBackup should have failed at the savegame swap")
	}
	msg := err.Error()
	if !strings.Contains(msg, "1 restored path(s) where nothing was before could not be removed") {
		t.Errorf("the error should name the leftover, got %q", msg)
	}
	if strings.Contains(msg, "original(s) could not be put back") || strings.Contains(msg, "rolled back to how it was") {
		t.Errorf("the error claims something about originals that is not true: %q", msg)
	}
}

// A failure in the merge phase — before any swap — says nothing was replaced.
func TestRestoreMergePhaseFailureSaysNothingWasReplaced(t *testing.T) {
	const sid = "s-merge"
	d, id := restoreFixture(t, sid,
		// A FILE where the archive has an ancestor directory: MkdirAll fails.
		map[string]string{"Pal": "not a directory"},
		dirEntry("Pal"), dirEntry("Pal/Saved"),
		archiveEntry{name: "Pal/Saved/Level.sav", body: "archived"},
	)
	err := d.RestoreBackup(context.Background(), sid, "", id)
	if err == nil {
		t.Fatal("RestoreBackup should fail to merge Pal over a file")
	}
	if !strings.Contains(err.Error(), "nothing in the live tree was replaced") {
		t.Errorf("a merge-phase failure should say nothing was replaced, got %q", err)
	}
	if v := liveRead(t, d, sid, "Pal"); v != "not a directory" {
		t.Errorf("Pal = %q; the merge must not have replaced it", v)
	}
}
