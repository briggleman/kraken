package agent

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
)

// The live line (dragonwilds-01, 2026-09-15..18).
const live0x602 = "Error! App '4019830' state is 0x602 after update job."

// TestSteamFailureRetryable splits the whole failure-phrase set: only the
// "state is 0x… after update job" family is worth a second pass.
func TestSteamFailureRetryable(t *testing.T) {
	cases := []struct {
		line      string
		retryable bool
	}{
		{live0x602, true},
		{"Error! App '4019830' state is 0x6 after update job.", true},
		{"Error! State is 0x402 after update job.", true},
		{"Error! App '4019830' state is is 0x2 after update job.", true},
		{"ERROR! Failed to install app '740' (No subscription)", false},
		{"Not for anonymous users.", false},
		{"ERROR! Password check for AppId 4019830 returned error Failure.", false},
		{"ERROR! Failed to install app '1829350' (Missing configuration)", false},
		{"ERROR! Failed to install app '232250' (Missing update files)", false},
		{"ERROR! Failed to install app '317670' (Corrupt update files)", false},
		{"ERROR! Failed to install app '4019830' (Invalid platform)", false},
		{"ERROR! Failed to install app '4019830' (Rate Limit Exceeded)", false},
		{"ERROR! Timeout downloading item 1234567890", false},
		{"ERROR! Failed to install app '4019830' (Disk write failure)", false},
		{"Error! Depot download failed : 4019831", false},
	}
	for _, tc := range cases {
		if !steamInstallFailureRE.MatchString(tc.line) {
			t.Errorf("%q is not a recognised failure line at all", tc.line)
		}
		if got := steamFailureRetryable(tc.line); got != tc.retryable {
			t.Errorf("%q: retryable = %v, want %v", tc.line, got, tc.retryable)
		}
	}
}

func TestDecodeSteamState(t *testing.T) {
	cases := map[uint32]string{
		0x0:    "invalid",
		0x6:    "fully installed, update required",
		0x602:  "update started, update paused, update required",
		0x606:  "update started, update paused, fully installed, update required",
		0x4:    "fully installed",
		0x2000: "unknown bits 0x2000",
		0x2002: "update required, unknown bits 0x2000",
		0xa0:   "files corrupt, files missing",
	}
	for state, want := range cases {
		if got := decodeSteamState(state); got != want {
			t.Errorf("0x%x: got %q, want %q", state, got, want)
		}
	}
}

func TestDescribeSteamFailure(t *testing.T) {
	cases := map[string]string{
		live0x602: "Error! App '4019830' state is 0x602 (update started, update paused, update required) after update job.",
		"Error! App '4019830' state is is 0x6 after update job.": "Error! App '4019830' state is is 0x6 (fully installed, update required) after update job.",
		"ERROR! Failed to install app '740' (No subscription)":   "ERROR! Failed to install app '740' (No subscription)",
	}
	for line, want := range cases {
		if got := describeSteamFailure(line); got != want {
			t.Errorf("%q:\n got %q\nwant %q", line, got, want)
		}
	}
}

func TestStagingTarget(t *testing.T) {
	ok := map[string]string{
		"RSDragonwildsServer-Win64-Shipping.exe~RF1561a.TMP": "RSDragonwildsServer-Win64-Shipping.exe",
		"engine.pak~rf0.tmp":        "engine.pak",
		"save~backup.dat~RF2a.TMP":  "save~backup.dat", // a `~` of the name's own is kept
		"~lock~RFff.TMP":            "~lock",
		"odd~RF1.TMP~RF2.TMP":       "odd~RF1.TMP", // only the last suffix is Steam's
		"with space.dll~RF9F9F.Tmp": "with space.dll",
	}
	for name, want := range ok {
		got, matched := stagingTarget(name)
		if !matched || got != want {
			t.Errorf("%q: got (%q, %v), want (%q, true)", name, got, matched, want)
		}
	}
	for _, name := range []string{
		"server.TMP", "foo~RF.TMP", "foo~RFxyz.TMP", "~RF12.TMP", "foo~RF12.TMP.bak", "foo~RF12.txt", "RSDragonwildsServer-Win64-Shipping.exe",
	} {
		if got, matched := stagingTarget(name); matched {
			t.Errorf("%q is not a staging file, got target %q", name, got)
		}
	}
}

func write(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanStagingFiles(t *testing.T) {
	root := t.TempDir()
	files := []string{
		// The live wreckage: the binary is gone, only its staging copy is left.
		"RSDragonwilds/Binaries/Win64/RSDragonwildsServer-Win64-Shipping.exe~RF1561a.TMP",
		// An ordinary in-flight staging file: its target is still there.
		"RSDragonwilds/Content/Paks/pakchunk0.pak",
		"RSDragonwilds/Content/Paks/pakchunk0.pak~RF77.TMP",
		// A name that contains `~` of its own, orphaned.
		"saves/world~1.sav~RFabc.TMP",
		// Real game files the scan must not report.
		"RSDragonwilds/Binaries/Win64/steam_api64.dll",
		"steamapps/appmanifest_4019830.acf",
		"server.TMP",
	}
	for _, f := range files {
		write(t, root, f)
	}
	// A directory named like a staging file is not a staging file.
	if err := os.MkdirAll(filepath.Join(root, "dir~RF1.TMP"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := scanStagingFiles(root)
	sort.Slice(got, func(i, j int) bool { return got[i].Rel < got[j].Rel })
	want := []stagingFile{
		{Rel: "RSDragonwilds/Binaries/Win64/RSDragonwildsServer-Win64-Shipping.exe~RF1561a.TMP", Target: "RSDragonwilds/Binaries/Win64/RSDragonwildsServer-Win64-Shipping.exe", TargetExists: false},
		{Rel: "RSDragonwilds/Content/Paks/pakchunk0.pak~RF77.TMP", Target: "RSDragonwilds/Content/Paks/pakchunk0.pak", TargetExists: true},
		{Rel: "saves/world~1.sav~RFabc.TMP", Target: "saves/world~1.sav", TargetExists: false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d staging files %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
	// The scan is read-only.
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err != nil {
			t.Errorf("scan disturbed %s: %v", f, err)
		}
	}
	if got := scanStagingFiles(filepath.Join(root, "does-not-exist")); len(got) != 0 {
		t.Errorf("a missing root must scan as empty, got %v", got)
	}
}

// lockedErr is the error a delete of a held file returns on this OS.
func lockedErr() error {
	if runtime.GOOS == "windows" {
		return &os.PathError{Op: "remove", Path: "x", Err: winErrorSharingViolation}
	}
	return &os.PathError{Op: "remove", Path: "x", Err: syscall.EBUSY}
}

func TestClearOrphans(t *testing.T) {
	found := []stagingFile{
		{Rel: "a.exe~RF1.TMP", Target: "a.exe"},
		{Rel: "b.pak~RF2.TMP", Target: "b.pak", TargetExists: true},
		{Rel: "c.dll~RF3.TMP", Target: "c.dll"},
		{Rel: "d.so~RF4.TMP", Target: "d.so"},
	}
	var tried []string
	c := clearOrphans(found, func(s stagingFile) error {
		tried = append(tried, s.Rel)
		switch s.Rel {
		case "c.dll~RF3.TMP":
			return lockedErr()
		case "d.so~RF4.TMP":
			return errors.New("read-only file system")
		}
		return nil
	})
	for _, rel := range tried {
		if rel == "b.pak~RF2.TMP" {
			t.Fatal("a staging file whose target exists must never be deleted")
		}
	}
	if len(c.removed) != 1 || len(c.locked) != 1 || len(c.failed) != 1 || len(c.inFlight) != 1 {
		t.Fatalf("got %+v", c)
	}
	msg := c.describe()
	for _, want := range []string{
		"removed orphaned staging file a.exe~RF1.TMP (its target a.exe was missing)",
		"orphaned staging file c.dll~RF3.TMP (target c.dll missing) is still locked — a container may be holding it",
		"could not remove orphaned staging file d.so~RF4.TMP",
		"in-flight staging file b.pak~RF2.TMP (target present) — not touched",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message should contain %q; got %q", want, msg)
		}
	}
	if got := (orphanCleanup{}).describe(); !strings.Contains(got, "no SteamCMD staging files") {
		t.Errorf("an empty scan should say so; got %q", got)
	}
}

// On Windows a file another process has open cannot be deleted — the exact
// situation of the live incident. The real delete must come back classified
// as locked.
func TestHostStagingTree_LockedFileIsReportedLocked(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Linux can unlink an open file; the lock only exists on Windows")
	}
	root := t.TempDir()
	rel := "x.exe~RF1.TMP"
	write(t, root, rel)
	h, err := os.Open(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	c := clearOrphans([]stagingFile{{Rel: rel, Target: "x.exe"}}, hostStagingTree{root: root}.remove)
	if len(c.locked) != 1 {
		t.Fatalf("a held file should be reported locked; got %+v", c)
	}
}
