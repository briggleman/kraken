package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
)

func TestKeepSteamCmdDepotCache(t *testing.T) {
	const script = "steamcmd +force_install_dir /data +login anonymous +app_update 1 +quit"
	pinAgentGroup(t, 0, false)

	t.Run("linux binds a neutral path and links it as the image's user", func(t *testing.T) {
		d := &DockerRuntime{dataDir: t.TempDir(), hostDataDir: "/host/data", osType: "linux"}
		cfg := &container.Config{Cmd: []string{script}}
		host := &container.HostConfig{Binds: []string{"/host/data/s1:/data"}}
		d.keepSteamCmdDepotCache("s1", cfg, host, func(l string) { t.Errorf("unexpected note %q", l) })
		want := filepath.Join("/host/data", steamCmdCacheDirName, "s1") + ":" + steamCmdCacheLinuxTarget
		if !slices.Contains(host.Binds, want) || len(host.Binds) != 2 {
			t.Errorf("binds %v, want the data dir and %q", host.Binds, want)
		}
		if cfg.Cmd[0] != linkSteamCmdCache+script {
			t.Errorf("cmd %q, want the link prefix then the script", cfg.Cmd[0])
		}
		if fi, err := os.Stat(d.steamCmdCacheDir("s1")); err != nil || !fi.IsDir() {
			t.Errorf("cache dir not made on the Agent's side: %v", err)
		}
		// Beside the data dirs, never inside one.
		if strings.HasPrefix(d.steamCmdCacheDir("s1"), d.localDir("s1")) {
			t.Errorf("cache %s is inside the data dir", d.steamCmdCacheDir("s1"))
		}
	})

	t.Run("windows binds C:\\steamcmd\\depotcache and leaves the script", func(t *testing.T) {
		d := &DockerRuntime{dataDir: t.TempDir(), hostDataDir: `D:\kraken\data`, osType: "windows"}
		cfg := &container.Config{Cmd: []string{"steamcmd.exe +quit"}}
		host := &container.HostConfig{}
		d.keepSteamCmdDepotCache("s1", cfg, host, func(string) {})
		if len(host.Binds) != 1 || !strings.HasSuffix(host.Binds[0], ":"+steamCmdCacheWindowsTarget) ||
			!strings.HasPrefix(host.Binds[0], filepath.Join(`D:\kraken\data`, steamCmdCacheDirName, "s1")) {
			t.Errorf("binds %v", host.Binds)
		}
		if cfg.Cmd[0] != "steamcmd.exe +quit" {
			t.Errorf("the Windows script must not change: %q", cfg.Cmd[0])
		}
	})

	t.Run("a script without steamcmd gets nothing", func(t *testing.T) {
		d := &DockerRuntime{dataDir: t.TempDir(), hostDataDir: "/host/data", osType: "linux"}
		cfg := &container.Config{Cmd: []string{"curl -L https://example.invalid/server.tar.gz | tar xz"}}
		host := &container.HostConfig{}
		d.keepSteamCmdDepotCache("s1", cfg, host, func(string) {})
		if len(host.Binds) != 0 || strings.HasPrefix(cfg.Cmd[0], linkSteamCmdCache) {
			t.Errorf("binds %v cmd %q", host.Binds, cfg.Cmd[0])
		}
		if _, err := os.Stat(d.steamCmdCacheDir("s1")); !os.IsNotExist(err) {
			t.Errorf("no cache dir for a non-SteamCMD install (stat %v)", err)
		}
	})

	t.Run("a cache that cannot be made is said and skipped", func(t *testing.T) {
		root := t.TempDir()
		blocker := filepath.Join(root, steamCmdCacheDirName)
		if err := os.WriteFile(blocker, nil, 0o644); err != nil { // a file where the dir goes
			t.Fatal(err)
		}
		d := &DockerRuntime{dataDir: root, hostDataDir: root, osType: "linux"}
		cfg := &container.Config{Cmd: []string{script}}
		host := &container.HostConfig{}
		var notes []string
		d.keepSteamCmdDepotCache("s1", cfg, host, func(l string) { notes = append(notes, l) })
		if len(host.Binds) != 0 || cfg.Cmd[0] != script {
			t.Errorf("binds %v cmd %q, want the pass untouched", host.Binds, cfg.Cmd[0])
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "may end in state 0x6") {
			t.Errorf("notes %v", notes)
		}
	})
}

// The link prefix, run by a real POSIX shell as it would be in the container.
func TestLinkSteamCmdCacheScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	run := func(home string) (string, error) {
		cmd := exec.Command(sh, "-c", linkSteamCmdCache+`readlink "$HOME/Steam/depotcache" || echo no-link`)
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	home := t.TempDir()
	if out, err := run(home); err != nil || out != steamCmdCacheLinuxTarget {
		t.Errorf("fresh home: %q %v, want a link to %s", out, err, steamCmdCacheLinuxTarget)
	}
	// Idempotent: a second pass in the same container relinks.
	if out, err := run(home); err != nil || out != steamCmdCacheLinuxTarget {
		t.Errorf("second run: %q %v", out, err)
	}

	// A real depotcache the image ships is left alone, said, and the script
	// still runs.
	baked := t.TempDir()
	if err := os.MkdirAll(filepath.Join(baked, "Steam", "depotcache"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := run(baked)
	if err != nil || !strings.Contains(out, "is not kept on this pass") || !strings.HasSuffix(out, "no-link") {
		t.Errorf("baked depotcache: %q %v", out, err)
	}
}

// The cache describes the tree, so it goes when the data goes and stays when
// it stays.
func TestRemoveDeletesTheDepotCacheWithTheData(t *testing.T) {
	for _, deleteData := range []bool{true, false} {
		const id = "srv-1"
		d := newRemoveRuntime(t, id, &fakeContainers{byName: map[string]string{}})
		if err := os.MkdirAll(d.steamCmdCacheDir(id), 0o755); err != nil {
			t.Fatal(err)
		}
		other := d.steamCmdCacheDir("srv-2")
		if err := os.MkdirAll(other, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := d.Remove(context.Background(), id, deleteData); err != nil {
			t.Fatalf("Remove(deleteData=%v): %v", deleteData, err)
		}
		_, err := os.Stat(d.steamCmdCacheDir(id))
		if deleteData != os.IsNotExist(err) {
			t.Errorf("deleteData=%v: cache stat err %v", deleteData, err)
		}
		if _, err := os.Stat(other); err != nil {
			t.Errorf("another server's cache was touched: %v", err)
		}
	}
}
