//go:build !windows

package agent

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
)

func TestShareDataTree_OpensWhatTheAgentOwns(t *testing.T) {
	root := filepath.Join(t.TempDir(), "srv")
	save := filepath.Join(root, "savegame")
	if err := os.MkdirAll(save, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, "enshrouded_server.json")
	slot := filepath.Join(save, "3ad85aea")
	exe := filepath.Join(root, "server.sh")
	for p, mode := range map[string]fs.FileMode{cfg: 0o644, slot: 0o600, exe: 0o755} {
		if err := os.WriteFile(p, []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil { // past the test's umask
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(slot, link); err != nil {
		t.Fatal(err)
	}

	if err := shareDataTree(root, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatalf("shareDataTree: %v", err)
	}

	for p, want := range map[string]fs.FileMode{
		root: fs.ModeDir | fs.ModeSetgid | 0o775,
		save: fs.ModeDir | fs.ModeSetgid | 0o775,
		cfg:  0o664,
		slot: 0o660, // adds group bits, never other
		exe:  0o775,
	} {
		info, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode(); got != want {
			t.Errorf("%s: mode %v, want %v", filepath.Base(p), got, want)
		}
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("symlink was replaced or followed: %v %v", info, err)
	}

	// Already shared: a second pass is a no-op and says nothing.
	if err := shareDataTree(root, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatalf("second pass: %v", err)
	}
}

func TestShareDataTree_LeavesOtherOwnersAlone(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "game-wrote-this")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f, 0o644); err != nil {
		t.Fatal(err)
	}
	// Pretend the Agent is someone else: nothing here is its to change.
	if err := shareDataTree(root, os.Geteuid()+1, os.Getegid()); err != nil {
		t.Fatalf("shareDataTree: %v", err)
	}
	if info, _ := os.Lstat(f); info.Mode() != 0o644 {
		t.Fatalf("mode of an entry the Agent does not own changed to %v", info.Mode())
	}
}

func TestShareDataTree_MissingRootIsAnError(t *testing.T) {
	err := shareDataTree(filepath.Join(t.TempDir(), "nope"), os.Geteuid(), os.Getegid())
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want not-exist", err)
	}
}

// The prefix is shell, so run it: a writable dir passes through to the spec's
// script with no probe left behind, and an unwritable one stops with 73 before
// the script runs.
func TestShareDataWithGame_PrefixRuns(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes through any mode")
	}
	pinAgentGroup(t, os.Getegid(), true)
	run := func(dir string) (string, int) {
		d := &DockerRuntime{osType: "linux"}
		cfg := &container.Config{Cmd: []string{"echo script-ran; umask"}}
		d.shareDataWithGame(cfg, &container.HostConfig{}, dir, true)
		out, err := exec.Command("sh", "-c", cfg.Cmd[0]).CombinedOutput()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(out), ee.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return string(out), 0
	}

	ok := t.TempDir()
	out, code := run(ok)
	if code != 0 || !strings.Contains(out, "script-ran") || !strings.Contains(out, "0002") {
		t.Fatalf("writable dir: exit %d, output:\n%s", code, out)
	}
	if _, err := os.Lstat(filepath.Join(ok, writeProbeName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("probe file left behind: %v", err)
	}

	locked := t.TempDir()
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	out, code = run(locked)
	if code != exitDataDirNotWritable || strings.Contains(out, "script-ran") {
		t.Fatalf("unwritable dir: exit %d, output:\n%s", code, out)
	}
	if !strings.Contains(out, "is not writable by this container (uid ") {
		t.Fatalf("probe does not say why:\n%s", out)
	}
}
