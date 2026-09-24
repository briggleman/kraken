package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

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

// gameDaemon is a daemon that knows one thing: the state of the server's game
// container, or that there is none, or that it cannot be asked. Only
// ContainerInspect is called on the restore path; anything else panicking is
// the test telling you so.
type gameDaemon struct {
	containerOps
	status string
	found  bool
	err    error
}

func (g *gameDaemon) ContainerInspect(_ context.Context, name string) (container.InspectResponse, error) {
	if g.err != nil {
		return container.InspectResponse{}, g.err
	}
	if !g.found {
		return container.InspectResponse{}, fmt.Errorf("no such container: %s: %w", name, cerrdefs.ErrNotFound)
	}
	return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{
		ID: "c-" + name, Name: "/" + name, State: &container.State{Status: g.status, Running: g.status == "running"},
	}}, nil
}

// An install pass holds the same tree a restore would swap: while the install
// gate is held for the server, both restore RPCs refuse with the Aborted a
// start gets there (install_running on the Panel), before touching anything.
func TestRestoreRefusesWhileAnInstallRuns(t *testing.T) {
	const sid = "s-installing"
	d, id := restoreFixture(t, sid,
		map[string]string{"savegame/a.db": "live-a"},
		dirEntry("savegame"), archiveEntry{name: "savegame/a.db", body: "archived-a"})
	leave := d.installs.enter(sid)
	defer leave()
	if err := d.RestoreBackup(context.Background(), sid, "", id); grpcstatus.Code(err) != codes.Aborted {
		t.Fatalf("unary restore during an install: %v; want Aborted", err)
	}
	stream := &fakeRestoreStream{ctx: context.Background()}
	if err := NewService(d).RestoreBackupStream(&agentpb.RestoreBackupRequest{ServerId: sid, Id: id}, stream); grpcstatus.Code(err) != codes.Aborted {
		t.Fatalf("streamed restore during an install: %v; want the Aborted status", err)
	}
	if v := liveRead(t, d, sid, "savegame/a.db"); v != "live-a" {
		t.Errorf("savegame/a.db = %q; a refused restore must not touch the tree", v)
	}
	leave()
	if err := d.RestoreBackup(context.Background(), sid, "", id); err != nil {
		t.Fatalf("restore after the install ended: %v", err)
	}
}

// The Agent refuses a restore while the game's container is running, whatever
// the Panel believed — the Panel's view of "stopped" can be a start that has
// not written its row yet. A stopped or absent container is restored as usual.
func TestRestoreRefusesARunningContainer(t *testing.T) {
	for _, status := range []string{"running", "restarting", "paused"} {
		t.Run(status, func(t *testing.T) {
			sid := "s-live-" + status
			d, id := restoreFixture(t, sid,
				map[string]string{"savegame/a.db": "live-a"},
				dirEntry("savegame"),
				archiveEntry{name: "savegame/a.db", body: "archived-a"},
			)
			d.containers = &gameDaemon{status: status, found: true}

			err := d.RestoreBackup(context.Background(), sid, "", id)
			if grpcstatus.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "stop it before restoring") {
				t.Fatalf("unary restore over a %s container: %v; want FailedPrecondition", status, err)
			}
			stream := &fakeRestoreStream{ctx: context.Background()}
			serr := NewService(d).RestoreBackupStream(&agentpb.RestoreBackupRequest{ServerId: sid, Id: id}, stream)
			if grpcstatus.Code(serr) != codes.FailedPrecondition {
				t.Fatalf("streamed restore over a %s container: %v; want the FailedPrecondition status, not a failed event", status, serr)
			}
			if v := liveRead(t, d, sid, "savegame/a.db"); v != "live-a" {
				t.Errorf("savegame/a.db = %q; a refused restore must not touch the tree", v)
			}
			noRestoreLeftovers(t, d, sid)
		})
	}
	// A stopped container and no container at all (a server that never
	// started) both restore — on the unary and the streamed RPC alike.
	for _, tc := range []struct {
		name   string
		status string
		found  bool
	}{{"exited", "exited", true}, {"no container", "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			for _, rpc := range []string{"unary", "stream"} {
				sid := "s-" + strings.ReplaceAll(tc.name, " ", "-") + "-" + rpc
				d, id := restoreFixture(t, sid,
					map[string]string{"savegame/a.db": "live-a"},
					dirEntry("savegame"), archiveEntry{name: "savegame/a.db", body: "archived-a"})
				d.containers = &gameDaemon{status: tc.status, found: tc.found}
				if rpc == "unary" {
					if err := d.RestoreBackup(context.Background(), sid, "", id); err != nil {
						t.Fatalf("unary restore (%s): %v", tc.name, err)
					}
				} else {
					stream := &fakeRestoreStream{ctx: context.Background()}
					if err := NewService(d).RestoreBackupStream(&agentpb.RestoreBackupRequest{ServerId: sid, Id: id}, stream); err != nil {
						t.Fatalf("streamed restore (%s): %v", tc.name, err)
					}
					if last := stream.last(t); last.Phase != restorePhaseDone {
						t.Fatalf("streamed restore (%s) ended with %+v, want done", tc.name, last)
					}
				}
				if v := liveRead(t, d, sid, "savegame/a.db"); v != "archived-a" {
					t.Errorf("%s restore (%s): savegame/a.db = %q, want archived-a", rpc, tc.name, v)
				}
			}
		})
	}
}

// When the container cannot be inspected at all, the restore fails closed:
// the check exists because the Panel's view can be stale, so "could not look"
// is not "stopped".
func TestRestoreFailsClosedWhenTheContainerCannotBeInspected(t *testing.T) {
	const sid = "s-inspect-err"
	d, id := restoreFixture(t, sid,
		map[string]string{"savegame/a.db": "live-a"},
		dirEntry("savegame"), archiveEntry{name: "savegame/a.db", body: "archived-a"})
	d.containers = &gameDaemon{err: errors.New("docker daemon is not answering")}
	err := d.RestoreBackup(context.Background(), sid, "", id)
	if grpcstatus.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), "could not check whether the server's container is running") {
		t.Fatalf("unary restore with a failed inspect: %v; want Unavailable", err)
	}
	stream := &fakeRestoreStream{ctx: context.Background()}
	if serr := NewService(d).RestoreBackupStream(&agentpb.RestoreBackupRequest{ServerId: sid, Id: id}, stream); grpcstatus.Code(serr) != codes.Unavailable {
		t.Fatalf("streamed restore with a failed inspect: %v; want the Unavailable status", serr)
	}
	if v := liveRead(t, d, sid, "savegame/a.db"); v != "live-a" {
		t.Errorf("savegame/a.db = %q; a restore that could not check the container must not touch the tree", v)
	}
	noRestoreLeftovers(t, d, sid)
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
