package agent

import (
	"path/filepath"
	"testing"
)

// TestBindSourceVsLocalDir pins the distinction that a containerized Agent
// depends on: native file ops run against the Agent's own view of the data root,
// while bind-mount sources must be the path the Docker daemon resolves on the
// host. Collapsing the two makes game servers read and write a directory the
// Agent cannot see.
func TestBindSourceVsLocalDir(t *testing.T) {
	agentView := filepath.Join(string(filepath.Separator), "data")
	hostView := filepath.Join(string(filepath.Separator), "srv", "kraken", "data")

	d := &DockerRuntime{dataDir: agentView, hostDataDir: hostView, osType: "linux"}

	if got, want := d.localDir("s1"), filepath.Join(agentView, "s1"); got != want {
		t.Errorf("localDir = %q, want %q (the Agent's own view)", got, want)
	}
	if got, want := d.bindSource("s1"), filepath.Join(hostView, "s1"); got != want {
		t.Errorf("bindSource = %q, want %q (the daemon's view)", got, want)
	}

	// File ops resolve through localDir, never the host view.
	if got, want := d.localOf("s1", "/data/save/world.db"), filepath.Join(agentView, "s1", "save", "world.db"); got != want {
		t.Errorf("localOf = %q, want %q", got, want)
	}
	if root := d.localDir("s1"); !d.withinHostDir("s1", filepath.Join(root, "save")) {
		t.Error("withinHostDir rejected a path inside the Agent's data dir")
	}
	if d.withinHostDir("s1", filepath.Join(hostView, "s1", "save")) {
		t.Error("withinHostDir accepted a daemon-view path; the jail must use the Agent's view")
	}
}

// TestResolveHostDataDir covers the default (share the host's view) and the
// explicit override.
func TestResolveHostDataDir(t *testing.T) {
	dataDir := filepath.Join(string(filepath.Separator), "var", "lib", "kraken", "server-data")

	t.Setenv("KRAKEN_HOST_DATA_DIR", "")
	if got := resolveHostDataDir(dataDir); got != dataDir {
		t.Errorf("unset override: got %q, want the data dir %q", got, dataDir)
	}

	// The override is absolutized the same way KRAKEN_DATA_DIR is, which on
	// Windows means a rooted path picks up the current drive letter.
	host := filepath.Join(string(filepath.Separator), "srv", "games")
	wantHost, err := filepath.Abs(host)
	if err != nil {
		t.Fatalf("Abs(%q): %v", host, err)
	}
	t.Setenv("KRAKEN_HOST_DATA_DIR", host)
	if got := resolveHostDataDir(dataDir); got != wantHost {
		t.Errorf("explicit override: got %q, want %q", got, wantHost)
	}

	// Whitespace-only is treated as unset, not as a relative path.
	t.Setenv("KRAKEN_HOST_DATA_DIR", "   ")
	if got := resolveHostDataDir(dataDir); got != dataDir {
		t.Errorf("blank override: got %q, want the data dir %q", got, dataDir)
	}
}
