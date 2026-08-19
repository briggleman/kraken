// Package config resolves the Agent's runtime configuration.
//
// Sources, in increasing order of precedence:
//
//  1. built-in defaults
//  2. paths derived from --root / KRAKEN_ROOT (one directory, sane layout)
//  3. a config file (YAML; JSON parses too, being a YAML subset)
//  4. the process environment (KRAKEN_*)
//  5. explicitly-passed command-line flags
//
// Environment sits above the file on purpose: an operator who adds a config
// file to a host already driven by compose / systemd / nssm env vars does not
// silently lose those settings. Flags sit above everything so a one-off
// override always wins.
package config

import (
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"sigs.k8s.io/yaml"
)

// Cert bundle filenames written by `krakenctl enroll`, looked up under
// <root>/certs when TLS paths aren't configured explicitly.
const (
	certName = "agent.pem"
	keyName  = "agent-key.pem"
	caName   = "ca.pem"
)

// Config is the Agent's fully-resolved configuration. Every field maps to one
// KRAKEN_* environment variable and one flag, so the three spellings of any
// setting stay recognizably the same setting.
type Config struct {
	// Root is a single directory the other paths default beneath:
	// <root>/state, <root>/server-data, <root>/backups, <root>/certs.
	Root string `json:"root,omitempty"`

	NodeID string `json:"node_id,omitempty"`
	NodeOS string `json:"node_os,omitempty"`
	Wine   *bool  `json:"wine,omitempty"`

	Addr     string `json:"addr,omitempty"`
	SFTPAddr string `json:"sftp_addr,omitempty"`

	StateDir    string `json:"state_dir,omitempty"`
	DataDir     string `json:"data_dir,omitempty"`
	HostDataDir string `json:"host_data_dir,omitempty"`
	BackupDir   string `json:"backup_dir,omitempty"`
	SFTPHostKey string `json:"sftp_host_key,omitempty"`

	TLSCert string `json:"tls_cert,omitempty"`
	TLSKey  string `json:"tls_key,omitempty"`
	TLSCA   string `json:"tls_ca,omitempty"`

	PanelURL          string `json:"panel_url,omitempty"`
	EnrollToken       string `json:"enroll_token,omitempty"`
	CAFingerprint     string `json:"ca_fingerprint,omitempty"`
	Runtime           string `json:"runtime,omitempty"`
	WindowsIsolation  string `json:"windows_isolation,omitempty"`
	AllowInsecureGRPC *bool  `json:"allow_insecure_grpc,omitempty"`

	// Tunnel enables reverse-connection mode: the Agent dials the Panel and
	// keeps a multiplexed session open, so the node needs no inbound gRPC
	// port. Requires an mTLS bundle (or a panel_url to enroll for one).
	Tunnel *bool `json:"tunnel,omitempty"`
	// TunnelAddr is the Panel's reverse-tunnel endpoint (host:port). Defaults
	// to the panel_url host on port 9443.
	TunnelAddr string `json:"tunnel_addr,omitempty"`

	// hostDataDirSet records whether HostDataDir was configured explicitly.
	// It defaults to DataDir, but the runtime warns when a containerized Agent
	// leaves it unset, so Export must not paper over that by exporting the
	// default. Not serialized.
	hostDataDirSet bool `json:"-"`

	// ConfigFile is the file this config was read from, if any. Not serialized.
	ConfigFile string `json:"-"`
}

// Flags describes the non-config command-line flags Load recognizes.
type Flags struct {
	ShowVersion bool
	PrintConfig bool
	// Service is a Windows service-control action: "install", "uninstall",
	// "start", or "stop". Empty means run normally.
	Service string
}

// Load resolves the configuration from args (typically os.Args[1:]) plus the
// environment and, if one is found, a config file.
func Load(args []string) (*Config, Flags, error) {
	var (
		fromFlags  Config
		modes      Flags
		cfgPath    string
		wine       bool
		insecure   bool
		tunnelFlag bool
	)

	fs := flag.NewFlagSet("kraken-agent", flag.ContinueOnError)
	fs.StringVar(&cfgPath, "config", "", "path to an agent config file (YAML or JSON); default: KRAKEN_CONFIG, <root>/agent.yaml, or the OS-conventional location")
	fs.StringVar(&fromFlags.Root, "root", "", "directory the other paths default beneath (state, server-data, backups, certs)")
	fs.StringVar(&fromFlags.NodeID, "node-id", "", "stable node identity reported to the Panel")
	fs.StringVar(&fromFlags.NodeOS, "node-os", "", `host OS for game containers: "linux" or "windows"`)
	fs.BoolVar(&wine, "wine", true, "advertise Wine support for Windows-only games on Linux")
	fs.StringVar(&fromFlags.Addr, "addr", "", "gRPC listen address (e.g. :9090)")
	fs.StringVar(&fromFlags.SFTPAddr, "sftp-addr", "", "SFTP listen address (e.g. :2022)")
	fs.StringVar(&fromFlags.StateDir, "state-dir", "", "Agent-owned state (mTLS bundle, SFTP host key)")
	fs.StringVar(&fromFlags.DataDir, "data-dir", "", "per-server game data root")
	fs.StringVar(&fromFlags.HostDataDir, "host-data-dir", "", "the data root as the Docker daemon sees it (containerized Agent only)")
	fs.StringVar(&fromFlags.BackupDir, "backup-dir", "", "local backup destination")
	fs.StringVar(&fromFlags.SFTPHostKey, "sftp-host-key", "", "SSH host key path (generated on first run)")
	fs.StringVar(&fromFlags.TLSCert, "tls-cert", "", "Agent certificate (PEM)")
	fs.StringVar(&fromFlags.TLSKey, "tls-key", "", "Agent private key (PEM)")
	fs.StringVar(&fromFlags.TLSCA, "tls-ca", "", "CA bundle Panel client certs must chain to (PEM)")
	fs.StringVar(&fromFlags.PanelURL, "panel-url", "", "Panel base URL for auto-enrollment when no TLS bundle exists")
	fs.StringVar(&fromFlags.EnrollToken, "enroll-token", "", "one-time bootstrap token for remote auto-enrollment (minted in the Panel's Add Node dialog)")
	fs.StringVar(&fromFlags.CAFingerprint, "ca-fingerprint", "", "pinned SHA-256 fingerprint of the Panel CA, verified during enrollment")
	fs.BoolVar(&tunnelFlag, "tunnel", false, "dial out to the Panel and serve over a reverse tunnel (no inbound gRPC port needed)")
	fs.StringVar(&fromFlags.TunnelAddr, "tunnel-addr", "", "Panel reverse-tunnel endpoint (host:port; default: panel-url host on port 9443)")
	fs.StringVar(&fromFlags.Runtime, "runtime", "", `container backend: "docker" or "fake"`)
	fs.StringVar(&fromFlags.WindowsIsolation, "windows-isolation", "", `Windows container isolation: "hyperv", "process", or "default"`)
	fs.BoolVar(&insecure, "allow-insecure-grpc", false, "serve plaintext gRPC on a non-loopback address (unsafe: exposes the Docker socket)")
	fs.StringVar(&modes.Service, "service", "", `Windows service control: "install", "uninstall", "start", or "stop"`)
	fs.BoolVar(&modes.ShowVersion, "version", false, "print version and exit")
	fs.BoolVar(&modes.PrintConfig, "print-config", false, "print the resolved configuration and exit")
	if err := fs.Parse(args); err != nil {
		return nil, modes, err
	}
	if n := fs.NArg(); n > 0 {
		return nil, modes, fmt.Errorf("config: unexpected argument %q", fs.Arg(0))
	}
	switch modes.Service {
	case "", "install", "uninstall", "start", "stop":
	default:
		return nil, modes, fmt.Errorf(`config: --service must be "install", "uninstall", "start", or "stop" (got %q)`, modes.Service)
	}

	// Which flags were actually typed — zero values must not outrank the file.
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["wine"] {
		fromFlags.Wine = &wine
	}
	if set["allow-insecure-grpc"] {
		fromFlags.AllowInsecureGRPC = &insecure
	}
	if set["tunnel"] {
		fromFlags.Tunnel = &tunnelFlag
	}

	// The root has to settle before the file is looked for, since <root>/agent.yaml
	// is one of the candidate locations.
	root := firstNonEmpty(fromFlags.Root, os.Getenv("KRAKEN_ROOT"))

	cfg := &Config{}
	if cfgPath == "" {
		cfgPath = os.Getenv("KRAKEN_CONFIG")
	}
	path, err := findConfigFile(cfgPath, root)
	if err != nil {
		return nil, modes, err
	}
	if path != "" {
		loaded, lerr := loadFile(path)
		if lerr != nil {
			return nil, modes, lerr
		}
		cfg = loaded
		cfg.ConfigFile = path
	}

	cfg.overlayEnv()
	cfg.overlay(&fromFlags)
	cfg.applyDefaults()
	return cfg, modes, cfg.validate()
}

// findConfigFile resolves which file to read. An explicitly requested path that
// does not exist is an error — silently ignoring it would start the Agent with
// a configuration the operator never intended.
func findConfigFile(explicit, root string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("config: %s: %w", explicit, err)
		}
		return explicit, nil
	}
	for _, c := range defaultConfigPaths(root) {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", nil
}

// defaultConfigPaths lists candidate config locations, nearest first.
func defaultConfigPaths(root string) []string {
	var out []string
	if root != "" {
		out = append(out, filepath.Join(root, "agent.yaml"), filepath.Join(root, "agent.yml"))
	}
	out = append(out, "agent.yaml", "agent.yml")
	if dir := osConfigDir(); dir != "" {
		out = append(out, filepath.Join(dir, "agent.yaml"), filepath.Join(dir, "agent.yml"))
	}
	return out
}

func loadFile(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var c Config
	// Strict so a typo'd key is reported instead of silently ignored — a
	// wrong-but-quiet config is the failure mode this package exists to avoid.
	// (sigs.k8s.io/yaml routes through JSON, so a .json file parses here too.)
	if err := yaml.UnmarshalStrict(b, &c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if c.HostDataDir != "" {
		c.hostDataDirSet = true
	}
	return &c, nil
}

// overlayEnv applies KRAKEN_* variables over the file-provided values.
func (c *Config) overlayEnv() {
	str := func(key string, dst *string) {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			*dst = v
		}
	}
	str("KRAKEN_ROOT", &c.Root)
	str("KRAKEN_NODE_ID", &c.NodeID)
	str("KRAKEN_NODE_OS", &c.NodeOS)
	str("KRAKEN_AGENT_ADDR", &c.Addr)
	str("KRAKEN_SFTP_ADDR", &c.SFTPAddr)
	str("KRAKEN_STATE_DIR", &c.StateDir)
	str("KRAKEN_DATA_DIR", &c.DataDir)
	str("KRAKEN_BACKUP_DIR", &c.BackupDir)
	str("KRAKEN_SFTP_HOST_KEY", &c.SFTPHostKey)
	str("KRAKEN_TLS_CERT", &c.TLSCert)
	str("KRAKEN_TLS_KEY", &c.TLSKey)
	str("KRAKEN_TLS_CA", &c.TLSCA)
	str("KRAKEN_PANEL_URL", &c.PanelURL)
	str("KRAKEN_ENROLL_TOKEN", &c.EnrollToken)
	str("KRAKEN_CA_FINGERPRINT", &c.CAFingerprint)
	str("KRAKEN_RUNTIME", &c.Runtime)
	str("KRAKEN_WINDOWS_ISOLATION", &c.WindowsIsolation)
	str("KRAKEN_TUNNEL_ADDR", &c.TunnelAddr)
	if v, ok := os.LookupEnv("KRAKEN_TUNNEL"); ok && v != "" {
		b := v == "1" || strings.EqualFold(v, "true")
		c.Tunnel = &b
	}

	if v, ok := os.LookupEnv("KRAKEN_HOST_DATA_DIR"); ok && strings.TrimSpace(v) != "" {
		c.HostDataDir = v
		c.hostDataDirSet = true
	}
	if v, ok := os.LookupEnv("KRAKEN_NODE_WINE"); ok && v != "" {
		b := v == "true"
		c.Wine = &b
	}
	if v, ok := os.LookupEnv("KRAKEN_ALLOW_INSECURE_GRPC"); ok && v != "" {
		b := v == "1"
		c.AllowInsecureGRPC = &b
	}
}

// overlay applies explicitly-set flag values over everything below them.
func (c *Config) overlay(f *Config) {
	str := func(src string, dst *string) {
		if src != "" {
			*dst = src
		}
	}
	str(f.Root, &c.Root)
	str(f.NodeID, &c.NodeID)
	str(f.NodeOS, &c.NodeOS)
	str(f.Addr, &c.Addr)
	str(f.SFTPAddr, &c.SFTPAddr)
	str(f.StateDir, &c.StateDir)
	str(f.DataDir, &c.DataDir)
	str(f.BackupDir, &c.BackupDir)
	str(f.SFTPHostKey, &c.SFTPHostKey)
	str(f.TLSCert, &c.TLSCert)
	str(f.TLSKey, &c.TLSKey)
	str(f.TLSCA, &c.TLSCA)
	str(f.PanelURL, &c.PanelURL)
	str(f.EnrollToken, &c.EnrollToken)
	str(f.CAFingerprint, &c.CAFingerprint)
	str(f.Runtime, &c.Runtime)
	str(f.WindowsIsolation, &c.WindowsIsolation)
	str(f.TunnelAddr, &c.TunnelAddr)
	if f.Tunnel != nil {
		c.Tunnel = f.Tunnel
	}
	if f.HostDataDir != "" {
		c.HostDataDir = f.HostDataDir
		c.hostDataDirSet = true
	}
	if f.Wine != nil {
		c.Wine = f.Wine
	}
	if f.AllowInsecureGRPC != nil {
		c.AllowInsecureGRPC = f.AllowInsecureGRPC
	}
}

// applyDefaults fills anything still unset: paths beneath Root when one is
// configured, then the historical defaults so an env-only host is unaffected.
func (c *Config) applyDefaults() {
	if c.Root != "" {
		c.Root = abs(c.Root)
		if c.StateDir == "" {
			c.StateDir = filepath.Join(c.Root, "state")
		}
		if c.DataDir == "" {
			c.DataDir = filepath.Join(c.Root, "server-data")
		}
		if c.BackupDir == "" {
			c.BackupDir = filepath.Join(c.Root, "backups")
		}
		// Adopt an enrolled bundle under <root>/certs, but only once all three
		// files are present: naming paths that don't exist yet would turn the
		// pre-enrollment first run into a TLS load failure instead of the
		// auto-enroll / loopback path it should take.
		if c.TLSCert == "" && c.TLSKey == "" && c.TLSCA == "" {
			certs := filepath.Join(c.Root, "certs")
			cert, key, ca := filepath.Join(certs, certName), filepath.Join(certs, keyName), filepath.Join(certs, caName)
			if allExist(cert, key, ca) {
				c.TLSCert, c.TLSKey, c.TLSCA = cert, key, ca
			}
		}
	}

	if c.NodeID == "" {
		c.NodeID = defaultNodeID()
	}
	if c.NodeOS == "" {
		c.NodeOS = "linux"
	}
	if c.Addr == "" {
		c.Addr = ":9090"
	}
	if c.SFTPAddr == "" {
		c.SFTPAddr = ":2022"
	}
	if c.StateDir == "" {
		c.StateDir = "."
	}
	if c.DataDir == "" {
		c.DataDir = "server-data"
	}
	if c.BackupDir == "" {
		c.BackupDir = "backups"
	}
	if c.Runtime == "" {
		c.Runtime = "docker"
	}
	if c.Wine == nil {
		t := true
		c.Wine = &t
	}
	if c.AllowInsecureGRPC == nil {
		f := false
		c.AllowInsecureGRPC = &f
	}
	c.StateDir, c.DataDir, c.BackupDir = abs(c.StateDir), abs(c.DataDir), abs(c.BackupDir)
	if c.HostDataDir == "" {
		c.HostDataDir = c.DataDir
	} else {
		c.HostDataDir = abs(c.HostDataDir)
	}
	if c.SFTPHostKey == "" {
		c.SFTPHostKey = filepath.Join(c.StateDir, "sftp_host_key")
	}
}

func (c *Config) validate() error {
	switch c.NodeOS {
	case "linux", "windows":
	default:
		return fmt.Errorf("config: node_os must be \"linux\" or \"windows\", got %q", c.NodeOS)
	}
	switch c.Runtime {
	case "docker", "fake":
	default:
		return fmt.Errorf("config: runtime must be \"docker\" or \"fake\", got %q", c.Runtime)
	}
	switch strings.ToLower(c.WindowsIsolation) {
	case "", "hyperv", "process", "default":
	default:
		return fmt.Errorf("config: windows_isolation must be \"hyperv\", \"process\", or \"default\", got %q", c.WindowsIsolation)
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		return fmt.Errorf("config: addr %q is not host:port: %w", c.Addr, err)
	}
	if _, _, err := net.SplitHostPort(c.SFTPAddr); err != nil {
		return fmt.Errorf("config: sftp_addr %q is not host:port: %w", c.SFTPAddr, err)
	}
	// A half-configured bundle fails the TLS handshake later with a much less
	// obvious message than saying so now.
	n := 0
	for _, v := range []string{c.TLSCert, c.TLSKey, c.TLSCA} {
		if v != "" {
			n++
		}
	}
	if n != 0 && n != 3 {
		return fmt.Errorf("config: tls_cert, tls_key and tls_ca must be set together (got %d of 3)", n)
	}
	if c.EnrollToken != "" && c.PanelURL == "" {
		return fmt.Errorf("config: enroll_token is set but panel_url is not — the token can only be redeemed against a Panel")
	}
	if c.TunnelAddr != "" {
		if _, _, err := net.SplitHostPort(c.TunnelAddr); err != nil {
			return fmt.Errorf("config: tunnel_addr %q is not host:port: %w", c.TunnelAddr, err)
		}
	}
	if c.TunnelEnabled() && c.TunnelAddr == "" && c.PanelURL == "" {
		return fmt.Errorf("config: tunnel mode needs tunnel_addr or panel_url to know where the Panel is")
	}
	return nil
}

// TunnelEnabled reports whether the Agent should serve over a reverse tunnel.
func (c *Config) TunnelEnabled() bool { return c.Tunnel != nil && *c.Tunnel }

// ResolveTunnelAddr returns the Panel's reverse-tunnel endpoint: tunnel_addr
// when set, else the panel_url host on the default tunnel port.
func (c *Config) ResolveTunnelAddr() (string, error) {
	if c.TunnelAddr != "" {
		return c.TunnelAddr, nil
	}
	u, err := url.Parse(c.PanelURL)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("config: cannot derive the tunnel address from panel_url %q — set tunnel_addr explicitly", c.PanelURL)
	}
	return net.JoinHostPort(u.Hostname(), "9443"), nil
}

// Secure reports whether a complete mTLS bundle is configured.
func (c *Config) Secure() bool { return c.TLSCert != "" && c.TLSKey != "" && c.TLSCA != "" }

// WineEnabled reports the resolved Wine setting.
func (c *Config) WineEnabled() bool { return c.Wine != nil && *c.Wine }

// InsecureGRPCAllowed reports the resolved plaintext-gRPC opt-in.
func (c *Config) InsecureGRPCAllowed() bool {
	return c.AllowInsecureGRPC != nil && *c.AllowInsecureGRPC
}

// Export materializes the resolved configuration into the process environment.
//
// Parts of the runtime still read KRAKEN_* directly (the Docker runtime's data,
// backup, and isolation settings), and they must observe the same values the
// rest of the Agent resolved — otherwise a --data-dir flag would silently apply
// to file ops but not to the container the Agent creates. Writing the resolved
// value back is always correct: it either came from the environment already, or
// from a source that outranks it.
//
// HostDataDir is exported only when it was configured explicitly, so the
// runtime's "containerized Agent with no KRAKEN_HOST_DATA_DIR" warning still
// fires for operators who haven't set one.
func (c *Config) Export() error {
	vars := map[string]string{
		"KRAKEN_DATA_DIR":   c.DataDir,
		"KRAKEN_BACKUP_DIR": c.BackupDir,
		"KRAKEN_STATE_DIR":  c.StateDir,
	}
	if c.WindowsIsolation != "" {
		vars["KRAKEN_WINDOWS_ISOLATION"] = c.WindowsIsolation
	}
	if c.hostDataDirSet {
		vars["KRAKEN_HOST_DATA_DIR"] = c.HostDataDir
	}
	for k, v := range vars {
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("config: export %s: %w", k, err)
		}
	}
	return nil
}

// YAML renders the resolved configuration, for --print-config.
func (c *Config) YAML() string {
	// The enroll token is a (short-lived) credential; keep it out of
	// --print-config output, which operators paste into issues and chats.
	render := *c
	if render.EnrollToken != "" {
		render.EnrollToken = "<redacted>"
	}
	b, err := yaml.Marshal(&render)
	if err != nil {
		return fmt.Sprintf("# could not render config: %v\n", err)
	}
	return string(b)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func abs(p string) string {
	if p == "" {
		return p
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func allExist(paths ...string) bool {
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	return true
}

// defaultNodeID keeps the historical placeholder deliberately. The node ID is
// the Agent's identity in the Panel's store, so switching the default to the
// hostname would re-register existing env-only hosts as new nodes on upgrade and
// orphan their installed servers.
func defaultNodeID() string { return "abyss-node-01" }

// osConfigDir is the conventional system-wide config directory for the Agent,
// searched last when no config file is named explicitly.
func osConfigDir() string {
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "Kraken")
		}
		return ""
	}
	return "/etc/kraken"
}
