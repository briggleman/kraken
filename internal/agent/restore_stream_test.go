package agent

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// incompressible returns n bytes gzip cannot shrink, so the compressed archive
// is big enough that the meter takes more than one read to cross it.
func incompressible(n int) string {
	r := rand.New(rand.NewPCG(1, 2))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.UintN(256))
	}
	return string(b)
}

// restoreEvents runs a streamed restore and collects what it emitted.
func restoreEvents(t *testing.T, ctx context.Context, d *DockerRuntime, sid, id string, onEvent func(*agentpb.RestoreEvent)) ([]*agentpb.RestoreEvent, error) {
	t.Helper()
	var evs []*agentpb.RestoreEvent
	err := d.RestoreBackupStream(ctx, sid, "", id, func(ev *agentpb.RestoreEvent) error {
		evs = append(evs, ev)
		if onEvent != nil {
			onEvent(ev)
		}
		return nil
	})
	return evs, err
}

// The meter is the whole of #361 on this side: bytes read only ever go up, the
// phases arrive in order, and a local archive ends with every byte of it read
// — a bar that stops at 97% because the tar reader never touched the gzip
// trailer is the kind of lie the fake 60% was.
func TestRestoreStreamProgressIsMonotonicAndEndsAtTheArchiveSize(t *testing.T) {
	const sid = "s-meter"
	d, id := restoreFixture(t, sid,
		map[string]string{"savegame/a.db": "live-a"},
		dirEntry("savegame"),
		archiveEntry{name: "savegame/a.db", body: incompressible(512 << 10)},
		archiveEntry{name: "savegame/b.db", body: incompressible(256 << 10)},
	)
	list, err := d.backups.List(context.Background(), sid)
	if err != nil || len(list) != 1 {
		t.Fatalf("list archives: %v (%d)", err, len(list))
	}
	size := list[0].Size

	evs, err := restoreEvents(t, context.Background(), d, sid, id, nil)
	if err != nil {
		t.Fatalf("RestoreBackupStream: %v", err)
	}
	if len(evs) < 4 {
		t.Fatalf("got %d events, want at least opening/extracting/applying/done: %v", len(evs), evs)
	}
	order := map[string]int{restorePhaseOpening: 0, restorePhaseExtracting: 1, restorePhaseApplying: 2, restorePhaseDone: 3}
	var lastDone int64
	lastPhase := -1
	for i, ev := range evs {
		p, ok := order[ev.Phase]
		if !ok {
			t.Fatalf("event %d has phase %q", i, ev.Phase)
		}
		if p < lastPhase {
			t.Errorf("event %d: phase %q after a later phase", i, ev.Phase)
		}
		lastPhase = p
		if ev.BytesDone < lastDone {
			t.Errorf("event %d: bytes_done went backwards, %d after %d", i, ev.BytesDone, lastDone)
		}
		lastDone = ev.BytesDone
		if ev.Phase != restorePhaseOpening && ev.BytesTotal != size {
			t.Errorf("event %d (%s): bytes_total = %d, want the archive size %d", i, ev.Phase, ev.BytesTotal, size)
		}
		if ev.BytesTotal > 0 && ev.BytesDone > ev.BytesTotal {
			t.Errorf("event %d: bytes_done %d past bytes_total %d", i, ev.BytesDone, ev.BytesTotal)
		}
	}
	if evs[0].Phase != restorePhaseOpening {
		t.Errorf("first event is %q, want opening", evs[0].Phase)
	}
	last := evs[len(evs)-1]
	if last.Phase != restorePhaseDone {
		t.Errorf("last event is %q, want done", last.Phase)
	}
	if last.BytesDone != size || last.BytesTotal != size {
		t.Errorf("done event reads %d / %d, want %d / %d", last.BytesDone, last.BytesTotal, size, size)
	}
	liveMissing(t, d, sid, "savegame/stray.db")
	noRestoreLeftovers(t, d, sid)
}

// A target that cannot size the archive reports 0, never a guess.
func TestArchiveSizeIsZeroWhenTheReaderCannotSayIt(t *testing.T) {
	if got := archiveSize(bytes.NewReader([]byte("x"))); got != 0 {
		t.Errorf("archiveSize(unsized reader) = %d, want 0", got)
	}
	if got := archiveSize(&sftpReadCloser{f: nopReadCloser{}}); got != 0 {
		t.Errorf("archiveSize(wrapper over an unsized reader) = %d, want 0", got)
	}
}

type nopReadCloser struct{}

func (nopReadCloser) Read([]byte) (int, error) { return 0, nil }
func (nopReadCloser) Close() error             { return nil }

// fakeRestoreStream is the server half of a RestoreBackupStream call, enough of
// it for the Service to Send into.
type fakeRestoreStream struct {
	grpc.ServerStream
	ctx context.Context
	mu  sync.Mutex
	evs []*agentpb.RestoreEvent
}

func (s *fakeRestoreStream) Context() context.Context { return s.ctx }
func (s *fakeRestoreStream) Send(ev *agentpb.RestoreEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evs = append(s.evs, ev)
	return nil
}

func (s *fakeRestoreStream) last(t *testing.T) *agentpb.RestoreEvent {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.evs) == 0 {
		t.Fatal("the stream carried no events")
	}
	return s.evs[len(s.evs)-1]
}

// A bad archive ends the stream with a failed event that says the live tree was
// not touched — and it was not.
func TestRestoreStreamReportsABadArchiveAsFailedAndLeavesTheTree(t *testing.T) {
	const sid = "s-bad"
	d, _ := restoreFixture(t, sid, map[string]string{"savegame/a.db": "live-a"})
	good := buildArchive(t, dirEntry("savegame"), archiveEntry{name: "savegame/a.db", body: incompressible(64 << 10)})
	const id = "1700000000001__truncated"
	truncated := good[:len(good)/2]
	if err := d.backups.Put(context.Background(), sid, id, bytes.NewReader(truncated), int64(len(truncated))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	stream := &fakeRestoreStream{ctx: context.Background()}
	if err := NewService(d).RestoreBackupStream(&agentpb.RestoreBackupRequest{ServerId: sid, Id: id}, stream); err != nil {
		t.Fatalf("a failed restore is reported in the stream, not as the RPC status: %v", err)
	}
	last := stream.last(t)
	if last.Phase != restorePhaseFailed || last.Failed == "" {
		t.Fatalf("last event = %+v, want a failed event with a reason", last)
	}
	if !strings.Contains(last.Failed, "not touched") {
		t.Errorf("the reason should say the live tree was not touched, got %q", last.Failed)
	}
	if v := liveRead(t, d, sid, "savegame/a.db"); v != "live-a" {
		t.Errorf("savegame/a.db = %q; a bad archive must not swap anything in", v)
	}
	noRestoreLeftovers(t, d, sid)
}

// A swap that fails partway is unwound, and the failed event says so.
func TestRestoreStreamMidSwapFailureSaysItRolledBack(t *testing.T) {
	const sid = "s-stream-rollback"
	d, id := restoreFixture(t, sid,
		map[string]string{"savegame/a.db": "live-a", "cfg.json": "live-cfg"},
		dirEntry("savegame"),
		archiveEntry{name: "savegame/a.db", body: "archived-a"},
		archiveEntry{name: "cfg.json", body: "archived-cfg"},
	)
	orig := restoreRename
	t.Cleanup(func() { restoreRename = orig })
	restoreRename = func(oldpath, newpath string) error {
		if strings.HasSuffix(oldpath, string(os.PathSeparator)+"savegame") {
			return errors.New("forced sharing violation")
		}
		return orig(oldpath, newpath)
	}
	stream := &fakeRestoreStream{ctx: context.Background()}
	if err := NewService(d).RestoreBackupStream(&agentpb.RestoreBackupRequest{ServerId: sid, Id: id}, stream); err != nil {
		t.Fatalf("RestoreBackupStream: %v", err)
	}
	last := stream.last(t)
	if last.Phase != restorePhaseFailed || !strings.Contains(last.Failed, "rolled back") {
		t.Fatalf("last event = %+v, want a failed event saying the tree was rolled back", last)
	}
	if v := liveRead(t, d, sid, "cfg.json"); v != "live-cfg" {
		t.Errorf("cfg.json = %q; the swapped unit was not rolled back", v)
	}
	noRestoreLeftovers(t, d, sid)
}

// Cancelling mid-extract stops the restore — on the next read of the archive,
// not after the file in hand — and nothing in the live tree moves.
func TestRestoreStreamCancelMidExtractStops(t *testing.T) {
	const sid = "s-cancel-extract"
	d, id := restoreFixture(t, sid,
		map[string]string{"savegame/a.db": "live-a"},
		dirEntry("savegame"),
		archiveEntry{name: "savegame/a.db", body: incompressible(1 << 20)},
	)
	// An event per read, so the cancel can land with the one entry's bytes
	// already flowing — past the between-entries check, inside its io.Copy.
	orig := restoreEmitEvery
	restoreEmitEvery = 0
	t.Cleanup(func() { restoreEmitEvery = orig })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const cancelAfter = 128 << 10
	var cancelledAt int64
	evs, err := restoreEvents(t, ctx, d, sid, id, func(ev *agentpb.RestoreEvent) {
		if cancelledAt == 0 && ev.Phase == restorePhaseExtracting && ev.Entry == "savegame/a.db" && ev.BytesDone > cancelAfter {
			cancelledAt = ev.BytesDone
			cancel()
		}
	})
	if cancelledAt == 0 {
		t.Fatal("the restore never reported the entry mid-read; the test did not cancel mid-file")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	last := evs[len(evs)-1]
	if last.BytesDone >= last.BytesTotal {
		t.Errorf("the archive was read to the end (%d of %d) after a cancel mid-file", last.BytesDone, last.BytesTotal)
	}
	// One read may already be in flight when the cancel lands; nothing more.
	if last.BytesDone-cancelledAt > 64<<10 {
		t.Errorf("reads went on %d bytes past the cancel", last.BytesDone-cancelledAt)
	}
	for _, ev := range evs {
		if ev.Phase == restorePhaseApplying || ev.Phase == restorePhaseDone {
			t.Fatalf("a cancelled restore reached %q", ev.Phase)
		}
	}
	if v := liveRead(t, d, sid, "savegame/a.db"); v != "live-a" {
		t.Errorf("savegame/a.db = %q; a cancelled extract must not swap anything in", v)
	}
	noRestoreLeftovers(t, d, sid)
}

// Cancelling between two swaps unwinds the one that already landed.
func TestRestoreStreamCancelMidSwapUnwinds(t *testing.T) {
	const sid = "s-cancel-swap"
	d, id := restoreFixture(t, sid,
		map[string]string{"savegame/a.db": "live-a", "cfg.json": "live-cfg"},
		dirEntry("savegame"),
		archiveEntry{name: "savegame/a.db", body: "archived-a"},
		archiveEntry{name: "cfg.json", body: "archived-cfg"},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	orig := restoreRename
	t.Cleanup(func() { restoreRename = orig })
	restoreRename = func(oldpath, newpath string) error {
		err := orig(oldpath, newpath)
		// cfg.json sorts first: once its staged copy is installed, cancel, so
		// the check before the savegame unit sees it.
		if err == nil && strings.Contains(oldpath, restoreScratchPrefix) && strings.HasSuffix(newpath, "cfg.json") {
			cancel()
		}
		return err
	}
	_, err := restoreEvents(t, ctx, d, sid, id, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("the error should say the tree was rolled back, got %v", err)
	}
	if v := liveRead(t, d, sid, "cfg.json"); v != "live-cfg" {
		t.Errorf("cfg.json = %q; the unit swapped before the cancel was not unwound", v)
	}
	if v := liveRead(t, d, sid, "savegame/a.db"); v != "live-a" {
		t.Errorf("savegame/a.db = %q, want the untouched live copy", v)
	}
	noRestoreLeftovers(t, d, sid)
}
