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
