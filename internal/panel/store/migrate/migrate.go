// Package migrate applies the Panel's embedded SQL migrations to Postgres.
package migrate

import (
	"database/sql"
	"embed"
	"fmt"
	"net/url"
	"strings"

	"github.com/pressly/goose/v3"

	_ "github.com/jackc/pgx/v5/stdlib" // register the "pgx" database/sql driver
)

//go:embed sql/*.sql
var migrationsFS embed.FS

// splitDSN returns the target database name and a maintenance DSN (same server,
// connected to the "postgres" database) so we can create the target if needed.
func splitDSN(databaseURL string) (dbName, maintenanceDSN string, err error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", "", fmt.Errorf("migrate: parse DSN: %w", err)
	}
	dbName = strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		return "", "", fmt.Errorf("migrate: DSN has no database name")
	}
	u.Path = "/postgres"
	return dbName, u.String(), nil
}

// quoteIdent safely double-quotes a Postgres identifier for use in DDL.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Probe connects to the server (maintenance DB) and reports whether the target
// database already exists and whether the role may create databases. Used by the
// "Test connection" preflight before committing a new DSN.
func Probe(databaseURL string) (canCreateDB, dbExists bool, err error) {
	name, maint, err := splitDSN(databaseURL)
	if err != nil {
		return false, false, err
	}
	db, err := sql.Open("pgx", maint)
	if err != nil {
		return false, false, fmt.Errorf("migrate: open: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Ping(); err != nil {
		return false, false, fmt.Errorf("migrate: connect: %w", err)
	}
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, name).Scan(&dbExists); err != nil {
		return false, false, fmt.Errorf("migrate: check database: %w", err)
	}
	// Best-effort privilege check; ignore errors (some providers restrict pg_roles).
	_ = db.QueryRow(`SELECT rolcreatedb OR rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&canCreateDB)
	return canCreateDB, dbExists, nil
}

// EnsureDatabase creates the target database if it does not already exist. The
// role must have CREATEDB (or the database must be pre-created); a clear error is
// returned otherwise.
func EnsureDatabase(databaseURL string) error {
	name, maint, err := splitDSN(databaseURL)
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", maint)
	if err != nil {
		return fmt.Errorf("migrate: open maintenance db: %w", err)
	}
	defer func() { _ = db.Close() }()

	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, name).Scan(&exists); err != nil {
		return fmt.Errorf("migrate: check database: %w", err)
	}
	if exists {
		return nil
	}
	// CREATE DATABASE cannot be parameterized; the identifier is safely quoted.
	if _, err := db.Exec(`CREATE DATABASE ` + quoteIdent(name)); err != nil {
		return fmt.Errorf("migrate: create database %q (role needs CREATEDB, or pre-create it): %w", name, err)
	}
	return nil
}

// Up opens databaseURL and applies all pending migrations.
func Up(databaseURL string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("migrate: open db: %w", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("migrate: set dialect: %w", err)
	}
	if err := goose.Up(db, "sql"); err != nil {
		return fmt.Errorf("migrate: up: %w", err)
	}
	return nil
}
