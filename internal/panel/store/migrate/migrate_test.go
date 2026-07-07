package migrate

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
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
//
// It uses a per-process dedicated database rather than the shared one named in
// KRAKEN_TEST_DATABASE_URL: the postgres_test package (which owns tests for
// the store implementation) also calls migrate.Up against that shared DB, and
// `go test ./...` runs packages in parallel — two Up() calls racing mid-way
// through 00001_init.sql produce a "duplicate key ... pg_type_typname_nsp_index"
// error on `CREATE TABLE roles`. Isolating this test's DB removes the race.
func TestEnsureDatabase_Integration(t *testing.T) {
	baseDSN := os.Getenv("KRAKEN_TEST_DATABASE_URL")
	if baseDSN == "" {
		t.Skip("set KRAKEN_TEST_DATABASE_URL to run the Postgres integration test")
	}

	dbName := fmt.Sprintf("kraken_migrate_test_%d", os.Getpid())
	dsn, err := swapDBName(baseDSN, dbName)
	if err != nil {
		t.Fatalf("swap dsn: %v", err)
	}
	// Best-effort drop before and after so a prior aborted run doesn't leave
	// stale state behind and block EnsureDatabase's CREATE (unlikely, since
	// EnsureDatabase already handles "exists", but the cleanup guarantees this
	// test is fully self-contained).
	dropTestDB := func() {
		_, maint, splitErr := splitDSN(dsn)
		if splitErr != nil {
			return
		}
		db, oerr := sql.Open("pgx", maint)
		if oerr != nil {
			return
		}
		defer func() { _ = db.Close() }()
		_, _ = db.Exec(`DROP DATABASE IF EXISTS ` + quoteIdent(dbName) + ` WITH (FORCE)`)
	}
	dropTestDB()
	t.Cleanup(dropTestDB)

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

// swapDBName returns dsn with its database name replaced.
func swapDBName(dsn, dbName string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	u.Path = "/" + dbName
	return u.String(), nil
}
