package config

import (
	"os"
	"path/filepath"
	"testing"
)

// clearEnv unsets every KRAKEN_* variable Load consults, so a test starts from a
// known state regardless of the developer's shell.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"KRAKEN_ROOT", "KRAKEN_CONFIG", "KRAKEN_NODE_ID", "KRAKEN_NODE_OS", "KRAKEN_NODE_WINE",
		"KRAKEN_AGENT_ADDR", "KRAKEN_SFTP_ADDR", "KRAKEN_STATE_DIR", "KRAKEN_DATA_DIR",
		"KRAKEN_HOST_DATA_DIR", "KRAKEN_BACKUP_DIR", "KRAKEN_SFTP_HOST_KEY",
		"KRAKEN_TLS_CERT", "KRAKEN_TLS_KEY", "KRAKEN_TLS_CA", "KRAKEN_PANEL_URL",
		"KRAKEN_RUNTIME", "KRAKEN_WINDOWS_ISOLATION", "KRAKEN_ALLOW_INSECURE_GRPC",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	// Load looks for ./agent.yaml; run from a scratch dir so a stray file in the
	// repo can't leak into the result.
	t.Chdir(t.TempDir())
}

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestDefaults(t *testing.T) {
	clearEnv(t)
	cfg, _, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":9090" || cfg.SFTPAddr != ":2022" {
		t.Errorf("addrs = %q / %q, want :9090 / :2022", cfg.Addr, cfg.SFTPAddr)
	}
	if cfg.NodeOS != "linux" || cfg.Runtime != "docker" || !cfg.WineEnabled() {
		t.Errorf("got os=%q runtime=%q wine=%v", cfg.NodeOS, cfg.Runtime, cfg.WineEnabled())
	}
	// Node identity must not drift for hosts that never set it — that would
	// re-register them as new nodes.
	if cfg.NodeID != "abyss-node-01" {
		t.Errorf("NodeID = %q, want the historical default abyss-node-01", cfg.NodeID)
	}
	if cfg.Secure() || cfg.InsecureGRPCAllowed() {
		t.Error("expected no TLS bundle and no insecure opt-in by default")
	}
	// HostDataDir mirrors DataDir but must not count as explicitly configured,
	// or the runtime's containerized-Agent warning would never fire.
	if cfg.HostDataDir != cfg.DataDir {
		t.Errorf("HostDataDir = %q, want it to mirror DataDir %q", cfg.HostDataDir, cfg.DataDir)
	}
	if cfg.hostDataDirSet {
		t.Error("hostDataDirSet should be false when the default is used")
	}
}

func TestPrecedenceFlagOverEnvOverFile(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := write(t, dir, "agent.yaml", "node_id: from-file\naddr: \":1111\"\nsftp_addr: \":2222\"\n")

	// File only.
	cfg, _, err := Load([]string{"--config", path})
	if err != nil {
		t.Fatalf("Load(file): %v", err)
	}
	if cfg.NodeID != "from-file" || cfg.Addr != ":1111" {
		t.Errorf("file: node=%q addr=%q", cfg.NodeID, cfg.Addr)
	}

	// Env beats the file, so adding a config file to an env-driven host (compose,
	// systemd, nssm) doesn't silently change its settings.
	t.Setenv("KRAKEN_NODE_ID", "from-env")
	t.Setenv("KRAKEN_AGENT_ADDR", ":3333")
	cfg, _, err = Load([]string{"--config", path})
	if err != nil {
		t.Fatalf("Load(env): %v", err)
	}
	if cfg.NodeID != "from-env" || cfg.Addr != ":3333" {
		t.Errorf("env: node=%q addr=%q", cfg.NodeID, cfg.Addr)
	}
	// Untouched keys still come from the file.
	if cfg.SFTPAddr != ":2222" {
		t.Errorf("env: sftp_addr = %q, want the file's :2222", cfg.SFTPAddr)
	}

	// Flags beat both.
	cfg, _, err = Load([]string{"--config", path, "--node-id", "from-flag", "--addr", ":4444"})
	if err != nil {
		t.Fatalf("Load(flag): %v", err)
	}
	if cfg.NodeID != "from-flag" || cfg.Addr != ":4444" {
		t.Errorf("flag: node=%q addr=%q", cfg.NodeID, cfg.Addr)
	}
}

// A bool flag left off the command line must not outrank the file, which is why
// Load consults FlagSet.Visit rather than the parsed value.
func TestBoolFlagsOnlyWinWhenPassed(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := write(t, dir, "agent.yaml", "wine: false\nallow_insecure_grpc: true\n")

	cfg, _, err := Load([]string{"--config", path})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.WineEnabled() {
		t.Error("wine: the file said false; an unpassed --wine default must not override it")
	}
	if !cfg.InsecureGRPCAllowed() {
		t.Error("allow_insecure_grpc: the file said true; an unpassed flag must not override it")
	}

	cfg, _, err = Load([]string{"--config", path, "--wine", "--allow-insecure-grpc=false"})
	if err != nil {
		t.Fatalf("Load(flags): %v", err)
	}
	if !cfg.WineEnabled() || cfg.InsecureGRPCAllowed() {
		t.Errorf("passed flags ignored: wine=%v insecure=%v", cfg.WineEnabled(), cfg.InsecureGRPCAllowed())
	}
}

func TestRootLayout(t *testing.T) {
	clearEnv(t)
	root := t.TempDir()
	cfg, _, err := Load([]string{"--root", root})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, c := range []struct{ got, want, name string }{
		{cfg.StateDir, filepath.Join(root, "state"), "state"},
		{cfg.DataDir, filepath.Join(root, "server-data"), "data"},
		{cfg.BackupDir, filepath.Join(root, "backups"), "backups"},
		{cfg.SFTPHostKey, filepath.Join(root, "state", "sftp_host_key"), "sftp host key"},
	} {
		if c.got != c.want {
			t.Errorf("%s dir = %q, want %q", c.name, c.got, c.want)
		}
	}
	// An explicit path still wins over the derived one.
	custom := t.TempDir()
	cfg, _, err = Load([]string{"--root", root, "--data-dir", custom})
	if err != nil {
		t.Fatalf("Load(override): %v", err)
	}
	if cfg.DataDir != custom {
		t.Errorf("DataDir = %q, want the explicit %q", cfg.DataDir, custom)
	}
}

// The bundle under <root>/certs is adopted only once all three files exist:
// naming paths that don't exist yet would turn a pre-enrollment first run into a
// TLS load failure instead of the auto-enroll path.
func TestRootCertsAdoptedOnlyWhenComplete(t *testing.T) {
	clearEnv(t)
	root := t.TempDir()
	certs := filepath.Join(root, "certs")
	if err := os.MkdirAll(certs, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Load([]string{"--root", root})
	if err != nil {
		t.Fatalf("Load(empty certs): %v", err)
	}
	if cfg.Secure() {
		t.Error("no cert files present, but TLS was reported as configured")
	}

	write(t, certs, certName, "x")
	write(t, certs, keyName, "x")
	cfg, _, err = Load([]string{"--root", root})
	if err != nil {
		t.Fatalf("Load(partial certs): %v", err)
	}
	if cfg.Secure() {
		t.Error("2 of 3 cert files present, but TLS was reported as configured")
	}

	write(t, certs, caName, "x")
	cfg, _, err = Load([]string{"--root", root})
	if err != nil {
		t.Fatalf("Load(full bundle): %v", err)
	}
	if !cfg.Secure() {
		t.Fatal("complete bundle under <root>/certs was not adopted")
	}
	if cfg.TLSCert != filepath.Join(certs, certName) {
		t.Errorf("TLSCert = %q, want %q", cfg.TLSCert, filepath.Join(certs, certName))
	}
}

func TestConfigFileDiscoveryAndJSON(t *testing.T) {
	clearEnv(t)
	root := t.TempDir()
	write(t, root, "agent.yaml", "node_id: found-under-root\n")
	cfg, _, err := Load([]string{"--root", root})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.NodeID != "found-under-root" {
		t.Errorf("NodeID = %q, want the <root>/agent.yaml value", cfg.NodeID)
	}
	if cfg.ConfigFile == "" {
		t.Error("ConfigFile should record the file that was read")
	}

	// JSON parses through the same path (sigs.k8s.io/yaml routes via JSON).
	dir := t.TempDir()
	jp := write(t, dir, "agent.json", `{"node_id":"from-json","node_os":"windows"}`)
	cfg, _, err = Load([]string{"--config", jp})
	if err != nil {
		t.Fatalf("Load(json): %v", err)
	}
	if cfg.NodeID != "from-json" || cfg.NodeOS != "windows" {
		t.Errorf("json: node=%q os=%q", cfg.NodeID, cfg.NodeOS)
	}
}

func TestErrors(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()

	cases := []struct {
		name string
		args []string
		file string
	}{
		{name: "missing explicit config", args: []string{"--config", filepath.Join(dir, "nope.yaml")}},
		{name: "unknown key", file: "node_id: x\nnode_od: typo\n"},
		{name: "bad node_os", args: []string{"--node-os", "plan9"}},
		{name: "bad runtime", args: []string{"--runtime", "podman"}},
		{name: "bad isolation", args: []string{"--windows-isolation", "vm"}},
		{name: "addr without port", args: []string{"--addr", "9090"}},
		{name: "partial tls bundle", args: []string{"--tls-cert", "c.pem"}},
		{name: "positional argument", args: []string{"extra"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := c.args
			if c.file != "" {
				args = append([]string{"--config", write(t, t.TempDir(), "agent.yaml", c.file)}, args...)
			}
			if _, _, err := Load(args); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

// Export must publish resolved values for the components still reading env
// directly, without inventing a HostDataDir the operator never set.
func TestExport(t *testing.T) {
	clearEnv(t)
	root := t.TempDir()
	cfg, _, err := Load([]string{"--root", root, "--windows-isolation", "process"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Export(); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if got := os.Getenv("KRAKEN_DATA_DIR"); got != cfg.DataDir {
		t.Errorf("KRAKEN_DATA_DIR = %q, want %q", got, cfg.DataDir)
	}
	if got := os.Getenv("KRAKEN_BACKUP_DIR"); got != cfg.BackupDir {
		t.Errorf("KRAKEN_BACKUP_DIR = %q, want %q", got, cfg.BackupDir)
	}
	if got := os.Getenv("KRAKEN_WINDOWS_ISOLATION"); got != "process" {
		t.Errorf("KRAKEN_WINDOWS_ISOLATION = %q, want process", got)
	}
	if got, ok := os.LookupEnv("KRAKEN_HOST_DATA_DIR"); ok && got != "" {
		t.Errorf("KRAKEN_HOST_DATA_DIR was exported as %q; the default must stay unset so the runtime can warn", got)
	}

	// Explicitly configured, it is exported.
	host := t.TempDir()
	cfg, _, err = Load([]string{"--root", root, "--host-data-dir", host})
	if err != nil {
		t.Fatalf("Load(host-data-dir): %v", err)
	}
	if err := cfg.Export(); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if got := os.Getenv("KRAKEN_HOST_DATA_DIR"); got != host {
		t.Errorf("KRAKEN_HOST_DATA_DIR = %q, want %q", got, host)
	}
}
