package migrate

import (
	"os"
	"testing"
)

func TestSplitDSN(t *testing.T) {
	name, maint, err := splitDSN("postgres://kraken:secret@db.host:5432/kraken?sslmode=require")
	if err != nil {
		t.Fatalf("splitDSN: %v", err)
	}
	if name != "kraken" {
		t.Fatalf("db name = %q, want kraken", name)
	}
	if maint != "postgres://kraken:secret@db.host:5432/postgres?sslmode=require" {
		t.Fatalf("maintenance DSN = %q", maint)
	}
}

func TestSplitDSN_NoDatabase(t *testing.T) {
	if _, _, err := splitDSN("postgres://u:p@host:5432/"); err == nil {
		t.Fatal("expected error when DSN has no database name")
	}
}

func TestQuoteIdent(t *testing.T) {
	if got := quoteIdent(`weird"name`); got != `"weird""name"` {
		t.Fatalf("quoteIdent escaped wrong: %s", got)
	}
}

// TestEnsureDatabase_Integration runs against a real Postgres when
// KRAKEN_TEST_DATABASE_URL is set (skipped otherwise, like the docker tests).
func TestEnsureDatabase_Integration(t *testing.T) {
	dsn := os.Getenv("KRAKEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set KRAKEN_TEST_DATABASE_URL to run the Postgres integration test")
	}
	if _, _, err := Probe(dsn); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if err := EnsureDatabase(dsn); err != nil {
		t.Fatalf("EnsureDatabase: %v", err)
	}
	if err := Up(dsn); err != nil {
		t.Fatalf("Up: %v", err)
	}
}
