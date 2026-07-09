// Package config loads Panel configuration from the environment. Values are read
// once at startup; defaults make the Panel runnable out of the box for local dev.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds all Panel runtime configuration.
type Config struct {
	// Env is the deployment environment: "dev", "staging", or "prod".
	Env string
	// HTTPAddr is the listen address for the HTTP API, e.g. ":8080".
	HTTPAddr string
	// DatabaseURL is the Postgres DSN. Empty selects the in-memory store (dev only).
	DatabaseURL string
	// DatabaseURLFromEnv is true when DatabaseURL came from KRAKEN_DATABASE_URL
	// (env-managed). In that case the UI shows the connection as locked and the
	// file-backed override is ignored.
	DatabaseURLFromEnv bool
	// ConfigFile is the path to the JSON file holding UI-entered settings that must
	// live outside the database (currently just the database_url).
	ConfigFile string
	// SessionTTL is how long an authenticated session remains valid.
	SessionTTL time.Duration
	// SessionTTLFromEnv is true when KRAKEN_SESSION_TTL was set, which locks the
	// session-TTL field in the UI (env wins over the stored setting).
	SessionTTLFromEnv bool

	// Bootstrap admin — created on first start if no users exist. If the password
	// is left empty a strong random one is generated and logged once at startup,
	// so there is no weak default credential.
	BootstrapAdminUser     string
	BootstrapAdminPassword string
	// BootstrapFromEnv is true when either bootstrap env var was set. It forces
	// bootstrap-admin creation even when the stored setting disables it (and locks
	// the toggle in the UI).
	BootstrapFromEnv bool

	// AllowedOrigins is the WebSocket Origin allowlist (host[:port] patterns, "*"
	// wildcards allowed). Empty falls back to localhost dev origins. Same-origin
	// requests are always permitted regardless of this list.
	AllowedOrigins []string
	// AllowedOriginsFromEnv is true when KRAKEN_ALLOWED_ORIGINS was set, which
	// locks the WS-origins field in the UI (env wins over the stored setting).
	AllowedOriginsFromEnv bool

	// SetupAllowedCIDRs restricts the /setup/* API surface (first-run wizard,
	// datastore configuration, local enrollment) to requests whose real TCP
	// peer falls inside one of these CIDRs (single IPs allowed too). Defaults
	// to loopback + private ranges (RFC 1918, link-local, IPv6 ULA) so setup
	// is never drivable from the public internet, even with valid credentials.
	SetupAllowedCIDRs []string

	// Mutual-TLS for Panel→Agent gRPC. When all three are set the Panel dials
	// Agents over mTLS; otherwise it falls back to an insecure connection (dev).
	TLSCert string // path to the Panel's client certificate (PEM)
	TLSKey  string // path to the Panel's private key (PEM)
	TLSCA   string // path to the CA certificate (PEM) that signs Panel + Agent certs

	// CA signing material. When set, the Panel acts as the certificate authority
	// for Agent enrollment: it issues short-lived Agent certs in response to a
	// one-time bootstrap token. Usually CACert == TLSCA.
	CACert string // path to the CA certificate (PEM) used to sign Agent certs
	CAKey  string // path to the CA private key (PEM)

	// Quickstart enables the single-host convenience path: on startup the Panel
	// auto-registers a co-located Agent as the "local" node so a fresh install
	// reaches a running server with no CLI. Defaults on in dev, off otherwise.
	Quickstart bool
	// LocalAgentAddr is the gRPC address of the co-located Agent used by quickstart.
	LocalAgentAddr string

	// StateDir groups Panel-owned state (config file, generated CA, auto-issued
	// Panel client cert). Defaults to "data" (cwd-relative) so existing dev
	// workflows keep working; production deploys set this to /var/lib/kraken.
	StateDir string
}

// MutualTLS reports whether Panel→Agent mTLS is fully configured.
func (c *Config) MutualTLS() bool {
	return c.TLSCert != "" && c.TLSKey != "" && c.TLSCA != ""
}

// CASigning reports whether the Panel can sign Agent enrollment requests.
func (c *Config) CASigning() bool { return c.CACert != "" && c.CAKey != "" }

// DefaultSetupAllowedCIDRs is the built-in internal-network allowlist for the
// /setup/* API surface: loopback, RFC 1918 private ranges, link-local, and the
// IPv6 loopback/ULA/link-local equivalents.
func DefaultSetupAllowedCIDRs() []string {
	return []string{
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
	}
}

// Load reads configuration from the environment, applying defaults.
func Load() (*Config, error) {
	// KRAKEN_STATE_DIR groups all Panel-owned state (config file, secrets
	// key, generated CA) under one directory so a systemd unit or a
	// container just needs to point at /var/lib/kraken. Legacy default is
	// "data" (cwd-relative) so existing dev setups keep working unchanged.
	stateDir := env("KRAKEN_STATE_DIR", "data")
	c := &Config{
		StateDir:               stateDir,
		Env:                    env("KRAKEN_ENV", "dev"),
		HTTPAddr:               env("KRAKEN_HTTP_ADDR", ":8080"),
		DatabaseURL:            env("KRAKEN_DATABASE_URL", ""),
		ConfigFile:             env("KRAKEN_CONFIG_FILE", filepath.Join(stateDir, "panel.json")),
		BootstrapAdminUser:     env("KRAKEN_BOOTSTRAP_ADMIN_USER", "admin"),
		BootstrapAdminPassword: env("KRAKEN_BOOTSTRAP_ADMIN_PASSWORD", ""),
		TLSCert:                env("KRAKEN_TLS_CERT", ""),
		TLSKey:                 env("KRAKEN_TLS_KEY", ""),
		TLSCA:                  env("KRAKEN_TLS_CA", ""),
		CACert:                 env("KRAKEN_CA_CERT", ""),
		CAKey:                  env("KRAKEN_CA_KEY", ""),
		LocalAgentAddr:         env("KRAKEN_LOCAL_AGENT_ADDR", "127.0.0.1:9090"),
		AllowedOrigins:         envList("KRAKEN_ALLOWED_ORIGINS"),
		SetupAllowedCIDRs:      envList("KRAKEN_SETUP_ALLOWED_CIDRS"),
	}
	if len(c.SetupAllowedCIDRs) == 0 {
		c.SetupAllowedCIDRs = DefaultSetupAllowedCIDRs()
	}
	_, c.AllowedOriginsFromEnv = os.LookupEnv("KRAKEN_ALLOWED_ORIGINS")
	_, c.SessionTTLFromEnv = os.LookupEnv("KRAKEN_SESSION_TTL")
	_, userSet := os.LookupEnv("KRAKEN_BOOTSTRAP_ADMIN_USER")
	_, pwSet := os.LookupEnv("KRAKEN_BOOTSTRAP_ADMIN_PASSWORD")
	c.BootstrapFromEnv = userSet || pwSet
	// Quickstart defaults on in dev; override explicitly with KRAKEN_QUICKSTART.
	c.Quickstart = boolEnv("KRAKEN_QUICKSTART", c.Env == "dev")

	// DSN precedence: env wins (and locks the UI); otherwise the file-backed value
	// the setup wizard writes; otherwise empty → in-memory store.
	c.DatabaseURLFromEnv = c.DatabaseURL != ""
	if c.DatabaseURL == "" {
		if fc, ferr := LoadFile(c.ConfigFile); ferr == nil {
			c.DatabaseURL = fc.DatabaseURL
		}
	}

	ttl, err := durationEnv("KRAKEN_SESSION_TTL", 24*time.Hour)
	if err != nil {
		return nil, err
	}
	c.SessionTTL = ttl

	return c, nil
}

// FileConfig is the on-disk JSON for settings that must persist outside the
// database: the DSN that selects the database, and the master key that encrypts
// secrets at rest in it.
type FileConfig struct {
	DatabaseURL string `json:"database_url,omitempty"`
	SecretsKey  string `json:"secrets_key,omitempty"` // base64 of a 32-byte AES key
}

// LoadFile reads the config file. A missing file is not an error (returns zero value).
func LoadFile(path string) (FileConfig, error) {
	var fc FileConfig
	if path == "" {
		return fc, nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- operator-controlled config path
	if errors.Is(err, os.ErrNotExist) {
		return fc, nil
	}
	if err != nil {
		return fc, err
	}
	if err := json.Unmarshal(data, &fc); err != nil {
		return fc, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return fc, nil
}

// writeFileConfig persists fc atomically with 0600 perms (it holds secrets).
func writeFileConfig(path string, fc FileConfig) error {
	if path == "" {
		return fmt.Errorf("config: no config file path set")
	}
	data, err := json.MarshalIndent(fc, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SaveDatabaseURL persists the DSN to the config file (merging existing values).
func SaveDatabaseURL(path, dsn string) error {
	fc, _ := LoadFile(path)
	fc.DatabaseURL = dsn
	return writeFileConfig(path, fc)
}

// ResolveSecretsKey returns the 32-byte master key used to encrypt at-rest DB
// secrets. Precedence: KRAKEN_SECRETS_KEY (base64) → the config file → a freshly
// generated key persisted to the config file. fromEnv reports the first case.
func ResolveSecretsKey(configFile string) (key []byte, fromEnv bool, err error) {
	if v := strings.TrimSpace(os.Getenv("KRAKEN_SECRETS_KEY")); v != "" {
		k, derr := base64.StdEncoding.DecodeString(v)
		if derr != nil || len(k) != 32 {
			return nil, true, fmt.Errorf("config: KRAKEN_SECRETS_KEY must be base64 of 32 bytes")
		}
		return k, true, nil
	}
	fc, _ := LoadFile(configFile)
	if fc.SecretsKey != "" {
		if k, derr := base64.StdEncoding.DecodeString(fc.SecretsKey); derr == nil && len(k) == 32 {
			return k, false, nil
		}
	}
	k := make([]byte, 32)
	if _, rerr := rand.Read(k); rerr != nil {
		return nil, false, rerr
	}
	fc.SecretsKey = base64.StdEncoding.EncodeToString(k)
	if werr := writeFileConfig(configFile, fc); werr != nil {
		return nil, false, fmt.Errorf("config: persist generated secrets key: %w", werr)
	}
	return k, false, nil
}

// UsesMemoryStore reports whether the Panel should use the in-memory store
// (no DatabaseURL configured). Only appropriate for local development.
func (c *Config) UsesMemoryStore() bool { return c.DatabaseURL == "" }

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// envList reads a comma-separated env var into a trimmed, non-empty slice.
func envList(key string) []string {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// boolEnv reads a boolean env var, falling back to def when unset/blank/unparseable.
func boolEnv(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return b
}

func durationEnv(key string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	// Allow either a Go duration ("24h") or a plain number of seconds.
	if d, err := time.ParseDuration(v); err == nil {
		return d, nil
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second, nil
	}
	return 0, fmt.Errorf("config: %s=%q is not a valid duration", key, v)
}
