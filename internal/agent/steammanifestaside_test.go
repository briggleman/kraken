package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// The line enshrouded-01 failed every update with on 2026-10-15, a few seconds
// into the pass, with nothing downloaded and no staging file anywhere.
const live0x6 = "Error! App '2278520' state is 0x6 after update job."

func TestStaleManifestState(t *testing.T) {
	cases := []struct {
		line   string
		app    string
		stales bool
	}{
		{live0x6, "2278520", true},
		{"Error! App '1963720' state is 0x2 after update job.", "1963720", true},
		{"Error! App '4019830' state is is 0x6 after update job.", "4019830", true},
		// The orphaned-commit family: an update started and paused.
		{live0x602, "", false},
		{"Error! App '4019830' state is 0x606 after update job.", "", false},
		{"Error! App '232250' state is 0x212 after update job.", "", false},
		// Fully installed alone is not "update required".
		{"Error! App '1' state is 0x4 after update job.", "", false},
		// No app id, nothing to find the manifest by.
		{"Error! State is 0x6 after update job.", "", false},
		{"ERROR! Failed to install app '740' (No subscription)", "", false},
	}
	for _, tc := range cases {
		app, ok := staleManifestState(tc.line)
		if ok != tc.stales || app != tc.app {
			t.Errorf("%q: got (%q, %v), want (%q, %v)", tc.line, app, ok, tc.app, tc.stales)
		}
	}
}

func staleTree() *memTree { return &memTree{manifests: map[string]bool{"2278520": true}} }

func TestRecovery_StaleManifestMovedAsideThenRetrySucceeds(t *testing.T) {
	r := &recoveryRun{verdicts: []string{live0x6}, passTook: 10 * time.Second}
	tree := staleTree()
	failure, err := r.run(deadlineCtx(t, 30*time.Minute), tree)
	if err != nil || failure != "" {
		t.Fatalf("want success after the retry, got failure=%q err=%v", failure, err)
	}
	if r.passes != 2 || len(tree.asides) != 1 {
		t.Fatalf("want one aside and two passes, got %d asides, %d passes", len(tree.asides), r.passes)
	}
	if a := tree.asides[0]; !a.discarded || a.restored {
		t.Errorf("a good second pass deletes what was set aside: discarded=%v restored=%v", a.discarded, a.restored)
	}
	joined := strings.Join(r.notes, "\n")
	for _, want := range []string{"could not build this update (state 0x6 = fully installed, update required, and no staging files)", "moved steamapps/appmanifest_2278520.acf aside", "downloads the whole depot"} {
		if !strings.Contains(joined, want) {
			t.Errorf("console should say %q; got\n%s", want, joined)
		}
	}
	if last := r.notes[len(r.notes)-1]; !strings.HasPrefix(last, "[kraken] recovered: the install pass ran again with steamapps/appmanifest_2278520.acf moved aside") {
		t.Errorf("want a closing recovered line; got %q", last)
	}
}

func TestRecovery_StaleManifestRetryFailsPutsItBack(t *testing.T) {
	second := "Error! App '2278520' state is 0x6 after update job."
	r := &recoveryRun{verdicts: []string{live0x6, second}, passTook: 10 * time.Second}
	tree := staleTree()
	failure, err := r.run(deadlineCtx(t, 30*time.Minute), tree)
	if err != nil {
		t.Fatal(err)
	}
	if r.passes != 2 || !tree.asides[0].restored || tree.asides[0].discarded {
		t.Fatalf("a failed retry puts the manifest back: passes=%d %+v", r.passes, tree.asides[0])
	}
	want := "Error! App '2278520' state is 0x6 (fully installed, update required) after update job; " +
		"second pass after moving steamapps/appmanifest_2278520.acf aside (first pass: state 0x6 = fully installed, update required); " +
		"put back steamapps/appmanifest_2278520.acf"
	if failure != want {
		t.Errorf("message:\n got %q\nwant %q", failure, want)
	}
}

func TestRecovery_StaleManifestNotRetried(t *testing.T) {
	cases := []struct {
		name    string
		tree    *memTree
		left    time.Duration
		noRetry string
		want    string
	}{
		{"no manifest to move", &memTree{}, 30 * time.Minute, "", "no steamapps/appmanifest_2278520.acf in the data dir to move aside"},
		{"move fails", &memTree{manifests: map[string]bool{"2278520": true}, asideErr: os.ErrPermission}, 30 * time.Minute, "", "could not move steamapps/appmanifest_2278520.acf aside for a retry: permission denied"},
		{"too little time for a whole-depot download", staleTree(), 10 * time.Minute, "", "not enough time left to retry with steamapps/appmanifest_2278520.acf moved aside (that needs 15m0s)"},
		{"steam guard", staleTree(), 30 * time.Minute, "this install used a one-time Steam Guard code, which a second pass cannot replay", "Steam Guard code, which a second pass cannot replay, so no automatic retry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &recoveryRun{verdicts: []string{live0x6}, noRetry: tc.noRetry}
			failure, err := r.run(deadlineCtx(t, tc.left), tc.tree)
			if err != nil {
				t.Fatal(err)
			}
			if r.passes != 1 {
				t.Fatalf("want no retry, got %d passes", r.passes)
			}
			if !strings.HasPrefix(failure, "Error! App '2278520' state is 0x6 (fully installed, update required) after update job; no SteamCMD staging files") || !strings.Contains(failure, tc.want) {
				t.Errorf("got %q, want it to name %q", failure, tc.want)
			}
		})
	}
}

// The orphaned-commit states keep their own path: no manifest is moved for
// them, with or without staging files.
func TestRecovery_StaleManifestOnlyFor0x6Family(t *testing.T) {
	r := &recoveryRun{verdicts: []string{"Error! App '2278520' state is 0x602 after update job."}}
	tree := staleTree()
	if _, err := r.run(deadlineCtx(t, 30*time.Minute), tree); err != nil {
		t.Fatal(err)
	}
	if r.passes != 1 || len(tree.asides) != 0 {
		t.Errorf("0x602 must not move the manifest: passes=%d asides=%d", r.passes, len(tree.asides))
	}
	// A 0x6 beside an in-flight staging file is not the stale-manifest shape.
	r = &recoveryRun{verdicts: []string{live0x6}}
	tree = staleTree()
	tree.files = []stagingFile{{Rel: "a.pak~RF1.TMP", Target: "a.pak", TargetExists: true}}
	if _, err := r.run(deadlineCtx(t, 30*time.Minute), tree); err != nil {
		t.Fatal(err)
	}
	if r.passes != 1 || len(tree.asides) != 0 {
		t.Errorf("a 0x6 with a staging file must not move the manifest: passes=%d asides=%d", r.passes, len(tree.asides))
	}
}

// writeTree creates each slash path under root with its own name as content.
func writeTree(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readRel(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	return string(b)
}

func asideDirs(t *testing.T, steamapps string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(steamapps, manifestAsidePrefix+"*"))
	return m
}

func TestHostAside_MovesManifestAndDownloadStateThenDiscards(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root,
		"steamapps/appmanifest_2278520.acf",
		"steamapps/appmanifest_228980.acf",
		"steamapps/downloading/2278520/chunk",
		"steamapps/downloading/state_2278520_2278521.patch",
		"steamapps/downloading/state_228980_228981.patch",
		"enshrouded_server.exe",
	)
	a, err := hostStagingTree{root: root}.setManifestAside("2278520")
	if err != nil || a == nil {
		t.Fatalf("setManifestAside: %v %v", a, err)
	}
	want := "steamapps/appmanifest_2278520.acf, steamapps/downloading/2278520, steamapps/downloading/state_2278520_2278521.patch"
	if got := strings.Join(a.moved(), ", "); got != want {
		t.Errorf("moved %q, want %q", got, want)
	}
	for _, gone := range []string{"steamapps/appmanifest_2278520.acf", "steamapps/downloading/2278520/chunk", "steamapps/downloading/state_2278520_2278521.patch"} {
		if readRel(t, root, gone) != "" {
			t.Errorf("%s still in SteamCMD's sight", gone)
		}
	}
	// Another app's manifest and state, and the game itself, stay.
	for _, kept := range []string{"steamapps/appmanifest_228980.acf", "steamapps/downloading/state_228980_228981.patch", "enshrouded_server.exe"} {
		if readRel(t, root, kept) != kept {
			t.Errorf("%s was moved", kept)
		}
	}
	if err := a.discard(); err != nil {
		t.Fatal(err)
	}
	if d := asideDirs(t, filepath.Join(root, "steamapps")); d != nil {
		t.Errorf("aside dir left after discard: %v", d)
	}
}

func TestHostAside_RestorePutsBackWhatSteamCmdDidNotReplace(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "steamapps/appmanifest_1.acf", "steamapps/downloading/1/chunk")
	a, err := hostStagingTree{root: root}.setManifestAside("1")
	if err != nil || a == nil {
		t.Fatalf("setManifestAside: %v %v", a, err)
	}
	// The second pass wrote a new manifest and nothing under downloading/1.
	writeTree(t, root, "steamapps/appmanifest_1.acf")
	if err := os.WriteFile(filepath.Join(root, "steamapps", "appmanifest_1.acf"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := a.restore()
	if !strings.Contains(got, "put back steamapps/downloading/1") || !strings.Contains(got, "kept the steamapps/appmanifest_1.acf SteamCMD wrote on the second pass") {
		t.Errorf("restore said %q", got)
	}
	if readRel(t, root, "steamapps/appmanifest_1.acf") != "new" {
		t.Error("SteamCMD's newer manifest was replaced by the one that failed")
	}
	if readRel(t, root, "steamapps/downloading/1/chunk") != "steamapps/downloading/1/chunk" {
		t.Error("download state not put back")
	}
	if d := asideDirs(t, filepath.Join(root, "steamapps")); d != nil {
		t.Errorf("aside dir left after restore: %v", d)
	}
}

func TestHostAside_RestoreAfterNothingReplaced(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "steamapps/appmanifest_1.acf")
	a, _ := hostStagingTree{root: root}.setManifestAside("1")
	if got := a.restore(); got != "put back steamapps/appmanifest_1.acf" {
		t.Errorf("restore said %q", got)
	}
	if readRel(t, root, "steamapps/appmanifest_1.acf") != "steamapps/appmanifest_1.acf" {
		t.Error("manifest not put back")
	}
}

func TestFindAppManifest(t *testing.T) {
	root := t.TempDir()
	if dir, err := findAppManifest(root, "1"); dir != "" || err != nil {
		t.Errorf("empty tree: %q %v", dir, err)
	}
	// A spec whose force_install_dir is a folder of the data dir.
	writeTree(t, root, "server/steamapps/appmanifest_1.acf", ".kraken-restore-9/steamapps/appmanifest_1.acf")
	dir, err := findAppManifest(root, "1")
	if err != nil || dir != filepath.Join(root, "server", "steamapps") {
		t.Errorf("nested: %q %v (restore scratch must be skipped)", dir, err)
	}
	// The data dir's own steamapps wins without a walk.
	writeTree(t, root, "steamapps/appmanifest_1.acf")
	if dir, err := findAppManifest(root, "1"); err != nil || dir != filepath.Join(root, "steamapps") {
		t.Errorf("direct: %q %v", dir, err)
	}
	// Two nested copies and none at the top: not a guess to make.
	other := t.TempDir()
	writeTree(t, other, "a/steamapps/appmanifest_1.acf", "b/steamapps/appmanifest_1.acf")
	if _, err := findAppManifest(other, "1"); err == nil || !strings.Contains(err.Error(), "2 copies of appmanifest_1.acf") {
		t.Errorf("ambiguous: %v", err)
	}
	if a, err := (hostStagingTree{root: other}).setManifestAside("1"); a != nil || err == nil {
		t.Errorf("an ambiguous tree must not be touched: %v %v", a, err)
	}
}

// End to end over a real tree: the stale-manifest failure, the move, a second
// pass that succeeds, and nothing left behind.
func TestRecovery_StaleManifestOnDisk(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "steamapps/appmanifest_2278520.acf", "steamapps/downloading/", "enshrouded_server.exe")
	steamapps := filepath.Join(root, "steamapps")
	var sawManifest []bool
	pass := func(context.Context) (string, error) {
		_, err := os.Stat(filepath.Join(steamapps, "appmanifest_2278520.acf"))
		sawManifest = append(sawManifest, err == nil)
		if len(sawManifest) == 1 {
			return live0x6, nil
		}
		// SteamCMD's fresh install writes a new manifest.
		return "", os.WriteFile(filepath.Join(steamapps, "appmanifest_2278520.acf"), []byte("new"), 0o644)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	failure, err := runInstallWithRecovery(ctx, pass, hostStagingTree{root: root}, func(string) {}, time.Now, "")
	if err != nil || failure != "" {
		t.Fatalf("got failure=%q err=%v", failure, err)
	}
	if len(sawManifest) != 2 || !sawManifest[0] || sawManifest[1] {
		t.Errorf("the second pass must run without the old manifest: %v", sawManifest)
	}
	if readRel(t, root, "steamapps/appmanifest_2278520.acf") != "new" || readRel(t, root, "enshrouded_server.exe") == "" {
		t.Error("tree not as the second pass left it")
	}
	if d := asideDirs(t, steamapps); d != nil {
		t.Errorf("aside dir left after a good retry: %v", d)
	}
}

// A move that fails part-way puts back what had moved, so the tree is as it was
// found and the failure is reported, not passed over.
func TestHostAside_FailedMoveLeavesTheTreeAsFound(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions that apply to this user")
	}
	root := t.TempDir()
	writeTree(t, root, "steamapps/appmanifest_1.acf", "steamapps/downloading/1/chunk")
	downloading := filepath.Join(root, "steamapps", "downloading")
	if err := os.Chmod(downloading, 0o555); err != nil { // downloading/1 cannot leave
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(downloading, 0o755) })
	a, err := hostStagingTree{root: root}.setManifestAside("1")
	if a != nil || err == nil || !strings.Contains(err.Error(), "move steamapps/downloading/1 aside") {
		t.Fatalf("got %v, %v; want the failed move reported", a, err)
	}
	if readRel(t, root, "steamapps/appmanifest_1.acf") != "steamapps/appmanifest_1.acf" {
		t.Error("the manifest that had moved was not put back")
	}
	if d := asideDirs(t, filepath.Join(root, "steamapps")); d != nil {
		t.Errorf("aside dir left after a failed move: %v", d)
	}
}

// exitOps is fakeOps with an exit code per created container: Linux SteamCMD
// exits 8 when an app fails, and a `;` chain ending in it passes that on.
type exitOps struct {
	*fakeOps
	exits []int64
}

func (e *exitOps) ContainerWait(_ context.Context, _ string, _ container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	status, errs := make(chan container.WaitResponse, 1), make(chan error, 1)
	var code int64
	if n := len(e.creates); n >= 1 && n <= len(e.exits) {
		code = e.exits[n-1]
	}
	status <- container.WaitResponse{StatusCode: code}
	return status, errs
}

func runFakeInstall(t *testing.T, d *DockerRuntime, script string) (failed string, completed bool, lines []string) {
	t.Helper()
	err := d.Install(deadlineCtx(t, 30*time.Minute), &agentpb.InstallServerRequest{ServerId: guardServer, Image: testRef, InstallScript: script},
		func(ev *agentpb.InstallEvent) error {
			switch e := ev.Event.(type) {
			case *agentpb.InstallEvent_Failed:
				failed = e.Failed
			case *agentpb.InstallEvent_Completed:
				completed = true
			case *agentpb.InstallEvent_LogLine:
				lines = append(lines, e.LogLine)
			}
			return nil
		})
	d.installCleanup.Wait()
	if err != nil && failed == "" {
		failed = err.Error()
	}
	return failed, completed, lines
}

// The live Linux run (kraken-steam-base, 2026-10-15): both app_update halves
// end `state is 0x6` and the script exits 8. That exit must not hide the
// SteamCMD line, or the manifest-aside retry never runs.
func TestDockerInstall_StaleManifestRecoversThroughExit8(t *testing.T) {
	d := newFileOpsRuntime(t)
	d.images = &fakeImages{local: map[string]image.InspectResponse{testRef: {ID: oldID}}}
	d.pullPolicy = pullNever
	ops := &exitOps{fakeOps: &fakeOps{passLogs: [][]string{
		{"Success! App '2278520' already up to date.", live0x6},
		{"Success! App '2278520' fully installed."},
	}}, exits: []int64{8, 0}}
	d.containers = ops
	writeTree(t, d.localDir(guardServer), "steamapps/appmanifest_2278520.acf", "steamapps/downloading/")

	failed, completed, lines := runFakeInstall(t, d, "steamcmd +app_update 2278520 +quit;\nsteamcmd +app_update 2278520 +quit")
	if failed != "" || !completed {
		t.Fatalf("want a completed install after the retry, got failed=%q", failed)
	}
	if len(ops.creates) != 2 {
		t.Fatalf("want two passes, got %d", len(ops.creates))
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"install script ran", "exit 8", "moved steamapps/appmanifest_2278520.acf aside", "[kraken] recovered:"} {
		if !strings.Contains(joined, want) {
			t.Errorf("console should say %q; got\n%s", want, joined)
		}
	}
	// Each pass got the depot cache: bound, and linked by the script.
	for i, cmd := range ops.cmds {
		if !strings.Contains(cmd[0], linkSteamCmdCache) {
			t.Errorf("pass %d ran without the depot cache link: %q", i+1, cmd[0])
		}
	}
	if d := asideDirs(t, filepath.Join(d.localDir(guardServer), "steamapps")); d != nil {
		t.Errorf("aside dir left after a good retry: %v", d)
	}
}

// A non-zero exit with no SteamCMD failure line is still the script's own
// failure, reported by its code.
func TestDockerInstall_NonZeroExitWithoutSteamLineStillFails(t *testing.T) {
	d := newFileOpsRuntime(t)
	d.images = &fakeImages{local: map[string]image.InspectResponse{testRef: {ID: oldID}}}
	d.pullPolicy = pullNever
	ops := &exitOps{fakeOps: &fakeOps{passLogs: [][]string{{"tar: unexpected end of file"}}}, exits: []int64{2}}
	d.containers = ops
	failed, completed, _ := runFakeInstall(t, d, "curl -L https://example.invalid/s.tgz | tar xz")
	if completed || failed != "install exited with code 2" || len(ops.creates) != 1 {
		t.Errorf("got failed=%q completed=%v passes=%d", failed, completed, len(ops.creates))
	}
}
