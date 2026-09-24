package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

func TestRetryFits(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		left  time.Duration
		first time.Duration
		want  bool
	}{
		// first×1.25 + 2m: a 10m first pass needs 14m30s left.
		{"plenty left", 28 * time.Minute, 2 * time.Minute, true},
		{"exactly enough", 14*time.Minute + 30*time.Second, 10 * time.Minute, true},
		{"a second short", 14*time.Minute + 29*time.Second, 10 * time.Minute, false},
		{"room for the pass but not the Panel's start", 10 * time.Minute, 10 * time.Minute, false},
		{"a quick pass still needs the reserve", time.Minute, time.Second, false},
		{"already past", -time.Minute, time.Second, false},
	}
	for _, tc := range cases {
		if got := retryFits(now.Add(tc.left), now, tc.first); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// memTree is a stagingTree in memory.
type memTree struct {
	files     []stagingFile
	lockedRel map[string]bool
	scans     int
	// reappear, when set, is put back into files after the first scan — an
	// orphan the second pass left behind again.
	reappear []stagingFile
}

func (m *memTree) scan() []stagingFile {
	m.scans++
	out := append([]stagingFile(nil), m.files...)
	if m.scans == 1 && m.reappear != nil {
		defer func() { m.files = append(m.files, m.reappear...) }()
	}
	return out
}

func (m *memTree) remove(s stagingFile) error {
	if m.lockedRel[s.Rel] {
		return lockedErr()
	}
	for i, f := range m.files {
		if f.Rel == s.Rel {
			m.files = append(m.files[:i], m.files[i+1:]...)
			break
		}
	}
	return nil
}

// recoveryRun drives runInstallWithRecovery with scripted passes. Each pass
// takes passTook on a fake clock that runs alongside the real one, so a real
// context deadline can be measured against it.
type recoveryRun struct {
	verdicts []string
	passes   int
	passTook time.Duration
	offset   time.Duration
	notes    []string
	noRetry  string
}

func (r *recoveryRun) now() time.Time { return time.Now().Add(r.offset) }

func (r *recoveryRun) pass(context.Context) (string, error) {
	i := r.passes
	r.passes++
	r.offset += r.passTook
	if i < len(r.verdicts) {
		return r.verdicts[i], nil
	}
	return "", nil
}

func (r *recoveryRun) run(ctx context.Context, tree stagingTree) (string, error) {
	return runInstallWithRecovery(ctx, r.pass, tree, func(s string) { r.notes = append(r.notes, s) }, r.now, r.noRetry)
}

func deadlineCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

const liveOrphanRel = "RSDragonwilds/Binaries/Win64/RSDragonwildsServer-Win64-Shipping.exe~RF1561a.TMP"

func liveOrphan() *memTree {
	return &memTree{files: []stagingFile{{
		Rel:    liveOrphanRel,
		Target: "RSDragonwilds/Binaries/Win64/RSDragonwildsServer-Win64-Shipping.exe",
	}}}
}

func TestRecovery_OrphanClearedThenRetrySucceeds(t *testing.T) {
	r := &recoveryRun{verdicts: []string{live0x602}, passTook: time.Minute}
	tree := liveOrphan()
	failure, err := r.run(deadlineCtx(t, 30*time.Minute), tree)
	if err != nil || failure != "" {
		t.Fatalf("want success after the retry, got failure=%q err=%v", failure, err)
	}
	if r.passes != 2 {
		t.Fatalf("want exactly two passes, got %d", r.passes)
	}
	if len(tree.files) != 0 {
		t.Error("the orphan was not deleted")
	}
	joined := strings.Join(r.notes, "\n")
	for _, want := range []string{"[kraken] removed orphaned SteamCMD staging file", liveOrphanRel, "retrying the install pass once"} {
		if !strings.Contains(joined, want) {
			t.Errorf("console should say %q; got\n%s", want, joined)
		}
	}
	// The closing summary is the last console line, so a long second pass
	// cannot scroll the only record of the cleanup out of the buffer.
	last := r.notes[len(r.notes)-1]
	if !strings.HasPrefix(last, "[kraken] recovered: removed 1 orphaned staging file(s) before this pass: ") || !strings.Contains(last, liveOrphanRel) {
		t.Errorf("want a closing recovered summary naming the file; got %q", last)
	}
}

func TestRecovery_RetryFailsTooReportsTheSecondPass(t *testing.T) {
	second := "Error! App '4019830' state is 0x606 after update job."
	r := &recoveryRun{verdicts: []string{live0x602, second}, passTook: time.Minute}
	tree := liveOrphan()
	tree.reappear = tree.files // the second pass left it again
	failure, err := r.run(deadlineCtx(t, 30*time.Minute), tree)
	if err != nil {
		t.Fatal(err)
	}
	if r.passes != 2 {
		t.Fatalf("want exactly two passes, got %d", r.passes)
	}
	want := "Error! App '4019830' state is 0x606 (update started, update paused, fully installed, update required) after update job; " +
		"second pass after removing 1 orphaned staging file(s) (first pass: state 0x602 = update started, update paused, update required): " + liveOrphanRel + "; " +
		"orphaned staging file " + liveOrphanRel + " came back on the second pass — something still holds the data dir"
	if failure != want {
		t.Errorf("message:\n got %q\nwant %q", failure, want)
	}
}

func TestRecovery_NonRetryableRunsOnce(t *testing.T) {
	line := "ERROR! Failed to install app '740' (No subscription)"
	r := &recoveryRun{verdicts: []string{line}}
	tree := liveOrphan()
	failure, err := r.run(deadlineCtx(t, 30*time.Minute), tree)
	if err != nil || failure != line {
		t.Fatalf("want the line unchanged, got %q (err %v)", failure, err)
	}
	if r.passes != 1 || tree.scans != 0 || len(tree.files) != 1 {
		t.Errorf("a non-retryable failure must not scan, delete or retry: passes=%d scans=%d", r.passes, tree.scans)
	}
}

func TestRecovery_NoOrphansRunsOnce(t *testing.T) {
	r := &recoveryRun{verdicts: []string{live0x602}}
	failure, err := r.run(deadlineCtx(t, 30*time.Minute), &memTree{})
	if err != nil {
		t.Fatal(err)
	}
	if r.passes != 1 {
		t.Fatalf("nothing changed on disk, so no retry; got %d passes", r.passes)
	}
	// One sentence: SteamCMD's trailing period is dropped before the join.
	want := "Error! App '4019830' state is 0x602 (update started, update paused, update required) after update job; no SteamCMD staging files (*~RF*.TMP) in the data dir"
	if failure != want {
		t.Errorf("message:\n got %q\nwant %q", failure, want)
	}
}

func TestRecovery_InFlightOnlyIsReportedNotTouched(t *testing.T) {
	r := &recoveryRun{verdicts: []string{live0x602}}
	tree := &memTree{files: []stagingFile{{Rel: "a.pak~RF1.TMP", Target: "a.pak", TargetExists: true}}}
	failure, _ := r.run(deadlineCtx(t, 30*time.Minute), tree)
	if r.passes != 1 || len(tree.files) != 1 {
		t.Fatalf("an in-flight file is not an orphan: passes=%d files=%v", r.passes, tree.files)
	}
	if !strings.Contains(failure, "in-flight staging file a.pak~RF1.TMP (target present) — not touched") {
		t.Errorf("got %q", failure)
	}
}

func TestRecovery_LockedOrphanIsReportedAndNotRetried(t *testing.T) {
	r := &recoveryRun{verdicts: []string{live0x602}}
	tree := liveOrphan()
	tree.lockedRel = map[string]bool{tree.files[0].Rel: true}
	failure, _ := r.run(deadlineCtx(t, 30*time.Minute), tree)
	if r.passes != 1 {
		t.Fatalf("nothing was removed, so no retry; got %d passes", r.passes)
	}
	if !strings.Contains(failure, "is still locked — a container may be holding it") {
		t.Errorf("got %q", failure)
	}
}

// One orphan removed and another still locked: the lock means something holds
// the data dir, so the retry would fail the same way — skip it.
func TestRecovery_AnyLockedOrphanSkipsTheRetry(t *testing.T) {
	r := &recoveryRun{verdicts: []string{live0x602}}
	tree := liveOrphan()
	tree.files = append(tree.files, stagingFile{Rel: "b.dll~RF2.TMP", Target: "b.dll"})
	tree.lockedRel = map[string]bool{"b.dll~RF2.TMP": true}
	failure, _ := r.run(deadlineCtx(t, 30*time.Minute), tree)
	if r.passes != 1 {
		t.Fatalf("a locked orphan must skip the retry; got %d passes", r.passes)
	}
	for _, want := range []string{"removed orphaned staging file " + liveOrphanRel, "b.dll~RF2.TMP (target b.dll missing) is still locked", "no automatic retry while a staging file is locked"} {
		if !strings.Contains(failure, want) {
			t.Errorf("message should contain %q; got %q", want, failure)
		}
	}
	if strings.Contains(failure, "came back") {
		t.Errorf("no retry ran, so nothing came back: %q", failure)
	}
}

func TestRecovery_NoBudgetClearsButDoesNotRetry(t *testing.T) {
	// The first pass took 20 minutes of a 30-minute deadline: a second would
	// not finish, and would turn this into "install stream interrupted".
	r := &recoveryRun{verdicts: []string{live0x602}, passTook: 20 * time.Minute}
	tree := liveOrphan()
	failure, _ := r.run(deadlineCtx(t, 30*time.Minute), tree)
	if r.passes != 1 {
		t.Fatalf("no budget for a retry; got %d passes", r.passes)
	}
	if len(tree.files) != 0 {
		t.Error("the orphan should still be cleared, so the next install can succeed")
	}
	if !strings.Contains(failure, "removed orphaned staging file") || !strings.Contains(failure, "not enough time left for a second pass (the first took 20m0s)") {
		t.Errorf("got %q", failure)
	}
}

func TestRecovery_NoDeadlineDoesNotRetry(t *testing.T) {
	r := &recoveryRun{verdicts: []string{live0x602}}
	failure, _ := r.run(context.Background(), liveOrphan())
	if r.passes != 1 || !strings.Contains(failure, "no deadline on this install, so no automatic retry — run the install again") {
		t.Fatalf("no deadline, no retry: passes=%d failure=%q", r.passes, failure)
	}
	if strings.Contains(failure, "not enough time") {
		t.Errorf("a missing deadline is not a short one: %q", failure)
	}
}

// A one-time Steam Guard code cannot be replayed; a second pass would fail on
// the login and bury the first pass's report.
func TestRecovery_SteamGuardNeverRetries(t *testing.T) {
	req := &agentpb.InstallServerRequest{Env: map[string]string{steamGuardEnv: "X7K2Q"}}
	r := &recoveryRun{verdicts: []string{live0x602}, noRetry: noRetryReason(req)}
	tree := liveOrphan()
	failure, _ := r.run(deadlineCtx(t, 30*time.Minute), tree)
	if r.passes != 1 {
		t.Fatalf("a Steam Guard install must never retry; got %d passes", r.passes)
	}
	if len(tree.files) != 0 || !strings.Contains(failure, "Steam Guard code") {
		t.Errorf("want the orphan cleared and the reason named; got %q", failure)
	}
	if noRetryReason(&agentpb.InstallServerRequest{Env: map[string]string{"STEAM_USER": "x"}}) != "" {
		t.Error("only a Steam Guard code blocks the retry")
	}
}

func TestRecovery_PassErrorPassesThrough(t *testing.T) {
	boom := errors.New("install exited with code 1")
	tree := &memTree{}
	failure, err := runInstallWithRecovery(context.Background(), func(context.Context) (string, error) { return "", boom }, tree, func(string) {}, time.Now, "")
	if !errors.Is(err, boom) || failure != "" || tree.scans != 0 {
		t.Fatalf("got failure=%q err=%v scans=%d", failure, err, tree.scans)
	}
}

// The cap applies to every category, failed deletes included.
func TestClearOrphans_CapsEveryCategory(t *testing.T) {
	var found []stagingFile
	for i := range 8 {
		found = append(found, stagingFile{Rel: fmt.Sprintf("f%d~RF%d.TMP", i, i), Target: fmt.Sprintf("f%d", i)})
	}
	c := clearOrphans(found, func(stagingFile) error { return errors.New("read-only file system") })
	msg := c.describe()
	if n := strings.Count(msg, "could not remove"); n != maxNamedStagingFiles {
		t.Errorf("want %d failed deletes named, got %d in %q", maxNamedStagingFiles, n, msg)
	}
	if !strings.Contains(msg, "and 3 more") {
		t.Errorf("want the rest counted; got %q", msg)
	}
}

// The real delete, on the real filesystem, on every OS CI runs: the orphan is
// gone, the in-flight file and its target are byte-for-byte untouched.
func TestHostStagingTree_ClearsARealOrphan(t *testing.T) {
	root := t.TempDir()
	write(t, root, "bin/server.exe~RF1.TMP")
	write(t, root, "paks/a.pak")
	write(t, root, "paks/a.pak~RF2.TMP")
	tree := hostStagingTree{root: root}

	c := clearOrphans(tree.scan(), tree.remove)
	if len(c.removed) != 1 || c.removed[0].Rel != "bin/server.exe~RF1.TMP" {
		t.Fatalf("want the one orphan removed, got %+v", c)
	}
	if _, err := os.Stat(filepath.Join(root, "bin", "server.exe~RF1.TMP")); !os.IsNotExist(err) {
		t.Errorf("the orphan is still on disk: %v", err)
	}
	for _, rel := range []string{"paks/a.pak", "paks/a.pak~RF2.TMP"} {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || string(b) != rel {
			t.Errorf("%s was disturbed: %q, %v", rel, b, err)
		}
	}
}

// A target the scan cannot stat — permission denied, not absent — is not
// proof the target is gone, so its `.TMP` is never treated as an orphan.
func TestScanStagingFiles_UnreadableTargetIsNotMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory execute permission is a POSIX notion")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission this test relies on")
	}
	root := t.TempDir()
	write(t, root, "locked/app.bin")
	write(t, root, "locked/app.bin~RF1.TMP")
	dir := filepath.Join(root, "locked")
	// Readable (the walk lists its entries) but not searchable, so an Lstat of
	// the target inside it fails with EACCES rather than ENOENT.
	if err := os.Chmod(dir, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	got := scanStagingFiles(root)
	if len(got) != 1 {
		t.Fatalf("want the staging file listed, got %+v", got)
	}
	if got[0].orphan() {
		t.Fatal("an unreadable target was treated as missing — its .TMP would be deleted")
	}
}

// The Agent's own restore scratch is not SteamCMD's, and is never scanned.
func TestScanStagingFiles_SkipsRestoreScratch(t *testing.T) {
	root := t.TempDir()
	write(t, root, restoreScratchPrefix+"123/x.exe~RF1.TMP")
	write(t, root, "saves"+asideMarker+"9/y.sav~RF2.TMP")
	write(t, root, "real/z.exe~RF3.TMP")
	got := scanStagingFiles(root)
	if len(got) != 1 || got[0].Rel != "real/z.exe~RF3.TMP" {
		t.Fatalf("want only the real staging file, got %+v", got)
	}
}

// DockerRuntime.Install end to end over the containerOps seam: a 0x602 pass
// that left an orphan, then a clean one. Two `_install` containers are created
// under the same name, and the guard runs before each — the retry goes through
// the same checks as the first pass.
func TestDockerInstall_RetriesThroughTheGuard(t *testing.T) {
	d := newFileOpsRuntime(t)
	d.images = &fakeImages{local: map[string]image.InspectResponse{testRef: {ID: oldID}}}
	d.pullPolicy = pullNever
	ops := &fakeOps{passLogs: [][]string{
		{"Update state (0x5) verifying install, progress: 80.32", live0x602},
		{"Success! App '4019830' fully installed."},
	}}
	d.ctrOps = ops
	write(t, d.localDir(guardServer), liveOrphanRel)

	var failed string
	var completed bool
	var lines []string
	ctx := deadlineCtx(t, 30*time.Minute)
	err := d.Install(ctx, &agentpb.InstallServerRequest{ServerId: guardServer, Image: testRef, InstallScript: "steamcmd"},
		func(ev *agentpb.InstallEvent) error {
			switch e := ev.Event.(type) {
			case *agentpb.InstallEvent_Failed:
				failed = e.Failed
			case *agentpb.InstallEvent_Completed:
				completed = true
				// The Panel starts the server on Completed, while the deferred
				// install-container removal may still be running.
				if err := d.installs.check(guardServer); err != nil {
					t.Errorf("the install gate was still shut when Completed went out: %v", err)
				}
			case *agentpb.InstallEvent_LogLine:
				lines = append(lines, e.LogLine)
			}
			return nil
		})
	if err != nil || failed != "" || !completed {
		t.Fatalf("want a completed install, got err=%v failed=%q", err, failed)
	}
	want := guardServer
	want = containerName(want) + "_install"
	if len(ops.creates) != 2 || ops.creates[0] != want || ops.creates[1] != want {
		t.Errorf("want two %s containers, got %v", want, ops.creates)
	}
	if ops.lists != 2 {
		t.Errorf("the guard should run before each pass: %d container listings", ops.lists)
	}
	if len(ops.removed) != 2 {
		t.Errorf("each install container should be removed after its pass: %v", ops.removed)
	}
	if _, err := os.Stat(filepath.Join(d.localDir(guardServer), filepath.FromSlash(liveOrphanRel))); !os.IsNotExist(err) {
		t.Errorf("the orphan is still on disk: %v", err)
	}
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "[kraken] recovered:") {
		t.Errorf("the recovery summary should be the last console line; got %q", last)
	}
}
