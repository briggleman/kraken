package config

import (
	"path/filepath"
	"testing"
)

const sampleDSN = "postgres://u:p@h:5432/kraken?sslmode=disable"

func TestSaveAndLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.json")
	if err := SaveDatabaseURL(path, sampleDSN); err != nil {
		t.Fatalf("save: %v", err)
	}
	fc, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if fc.DatabaseURL != sampleDSN {
		t.Fatalf("round-trip mismatch: %q", fc.DatabaseURL)
	}
}

func TestLoadFile_MissingIsNotError(t *testing.T) {
	fc, err := LoadFile(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if fc.DatabaseURL != "" {
		t.Fatalf("expected empty, got %q", fc.DatabaseURL)
	}
}

func TestLoad_EnvWinsAndLocks(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "panel.json")
	if err := SaveDatabaseURL(cfgPath, "postgres://file/db"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_CONFIG_FILE", cfgPath)
	t.Setenv("KRAKEN_DATABASE_URL", "postgres://env/db")

	c, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.DatabaseURL != "postgres://env/db" {
		t.Fatalf("env should win, got %q", c.DatabaseURL)
	}
	if !c.DatabaseURLFromEnv {
		t.Fatal("expected DatabaseURLFromEnv=true when env is set")
	}
}

func TestLoad_StateDirDrivesConfigFileDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KRAKEN_STATE_DIR", dir)
	// Explicit KRAKEN_CONFIG_FILE must be unset for the default to apply.
	t.Setenv("KRAKEN_CONFIG_FILE", "")
	t.Setenv("KRAKEN_DATABASE_URL", "")

	c, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := filepath.Join(dir, "panel.json")
	if c.ConfigFile != want {
		t.Fatalf("state-dir default: got %q, want %q", c.ConfigFile, want)
	}
}

func TestLoad_ExplicitConfigFileWinsOverStateDir(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "custom.json")
	t.Setenv("KRAKEN_STATE_DIR", t.TempDir())
	t.Setenv("KRAKEN_CONFIG_FILE", explicit)
	t.Setenv("KRAKEN_DATABASE_URL", "")

	c, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.ConfigFile != explicit {
		t.Fatalf("explicit path should win, got %q", c.ConfigFile)
	}
}

func TestLoad_FileFallbackWhenEnvUnset(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "panel.json")
	if err := SaveDatabaseURL(cfgPath, "postgres://file/db"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_CONFIG_FILE", cfgPath)
	t.Setenv("KRAKEN_DATABASE_URL", "") // treated as unset

	c, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.DatabaseURL != "postgres://file/db" {
		t.Fatalf("file value should be used, got %q", c.DatabaseURL)
	}
	if c.DatabaseURLFromEnv {
		t.Fatal("expected DatabaseURLFromEnv=false when only the file is set")
	}
	if !c.UsesMemoryStore() == (c.DatabaseURL == "") {
		// sanity: with a DSN present, UsesMemoryStore must be false
		if c.UsesMemoryStore() {
			t.Fatal("UsesMemoryStore should be false when a DSN is present")
		}
	}
}

// A typo in KRAKEN_TRUSTED_PROXIES has to stop the process. Skipping the entry
// with a warning would leave a Panel that starts, serves, and is quietly wrong:
// behind a tunnel, /setup/* would see loopback for the entire internet and both
// rate limiters would share one bucket — a misconfiguration that looks exactly
// like a working deployment.
func TestLoadRejectsAnUnparseableTrustedProxy(t *testing.T) {
	t.Setenv("KRAKEN_TRUSTED_PROXIES", "127.0.0.0/8,proxy.internal")
	if _, err := Load(); err == nil {
		t.Fatal("Load accepted a trusted-proxy entry that is not an address")
	}
	t.Setenv("KRAKEN_TRUSTED_PROXIES", "127.0.0.0/8,10.0.0.5,::1/128")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load rejected a valid list: %v", err)
	}
	if len(cfg.TrustedProxies) != 3 {
		t.Fatalf("parsed %d entries, want 3", len(cfg.TrustedProxies))
	}
	// Unset is the default and is not an error: no proxy is trusted.
	t.Setenv("KRAKEN_TRUSTED_PROXIES", "")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load rejected an empty list: %v", err)
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Fatalf("empty list parsed as %v", cfg.TrustedProxies)
	}
}

// The setup allowlist keeps its skip-and-warn behaviour on purpose: that list
// fails CLOSED, so a dropped entry denies access rather than granting it.
func TestLoadDoesNotRejectAnUnparseableSetupCIDR(t *testing.T) {
	t.Setenv("KRAKEN_SETUP_ALLOWED_CIDRS", "10.0.0.0/8,not-a-cidr")
	if _, err := Load(); err != nil {
		t.Fatalf("Load rejected a setup allowlist entry: %v", err)
	}
}

func TestRateLimitsAndLogLevelDefaults(t *testing.T) {
	c := &Config{}
	if !c.RateLimitsEnabled() {
		t.Fatal("limiters are off by default; they must be on unless disabled")
	}
	if (&Config{RateLimits: "OFF"}).RateLimitsEnabled() {
		t.Fatal("RateLimits=OFF did not disable the limiters")
	}
	if lvl := (&Config{LogLevel: "debug"}).SlogLevel(); lvl.String() != "DEBUG" {
		t.Fatalf("LogLevel=debug gave %s", lvl)
	}
	if lvl := (&Config{LogLevel: "nonsense"}).SlogLevel(); lvl.String() != "INFO" {
		t.Fatalf("an unrecognized level gave %s, want INFO", lvl)
	}
}

// The retention window decides what gets deleted, so a value Load cannot make
// sense of has to stop the process rather than fall back to the default.
func TestLoadValidatesAuditRetention(t *testing.T) {
	// Unset is the shipped window, and matches what the console used to claim.
	t.Setenv("KRAKEN_AUDIT_RETENTION_DAYS", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load rejected the default: %v", err)
	}
	if cfg.AuditRetentionDays != 90 {
		t.Fatalf("default retention is %d days, want 90", cfg.AuditRetentionDays)
	}

	// 0 is a real answer, not an error: keep every entry.
	t.Setenv("KRAKEN_AUDIT_RETENTION_DAYS", "0")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load rejected 0: %v", err)
	}
	if cfg.AuditRetentionDays != 0 {
		t.Fatalf("retention 0 parsed as %d", cfg.AuditRetentionDays)
	}

	t.Setenv("KRAKEN_AUDIT_RETENTION_DAYS", "14")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load rejected 14: %v", err)
	}
	if cfg.AuditRetentionDays != 14 {
		t.Fatalf("retention 14 parsed as %d", cfg.AuditRetentionDays)
	}

	for _, bad := range []string{"-1", "90d", "ninety", "1.5"} {
		t.Setenv("KRAKEN_AUDIT_RETENTION_DAYS", bad)
		if _, err := Load(); err == nil {
			t.Errorf("Load accepted KRAKEN_AUDIT_RETENTION_DAYS=%q", bad)
		}
	}
}
