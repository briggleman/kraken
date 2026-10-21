package migrate

import (
	"database/sql"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
)

// 00011 moves a server's owner out of its JSON document into owner_id (#146).
// An upgrade that lost the owner would hide every Operator's own servers from
// them, so the backfill is checked against a database migrated to 00010 and
// seeded the way a live Panel has it: an owner that is a user, an owner that
// was deleted long ago, and a server from before ownership existed.
func TestOwnerBackfill_Integration(t *testing.T) {
	baseDSN := os.Getenv("KRAKEN_TEST_DATABASE_URL")
	if baseDSN == "" {
		t.Skip("set KRAKEN_TEST_DATABASE_URL to run the Postgres integration test")
	}
	dbName := fmt.Sprintf("kraken_backfill_test_%d", os.Getpid())
	dsn, err := swapDBName(baseDSN, dbName)
	if err != nil {
		t.Fatalf("swap dsn: %v", err)
	}
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
	if err := EnsureDatabase(dsn); err != nil {
		t.Fatalf("EnsureDatabase: %v", err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, "sql", 10); err != nil {
		t.Fatalf("migrate to 00010: %v", err)
	}

	user := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO roles (id, name, builtin, permissions) VALUES ('operator', 'Operator', true, '[]')`); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, username, password_hash, role_id) VALUES ($1, 'olga', 'h', 'operator')`, user); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	owned, orphaned, legacy := uuid.NewString(), uuid.NewString(), uuid.NewString()
	seed := map[string]string{
		owned:    fmt.Sprintf(`{"id":%q,"owner_id":%q}`, owned, user),
		orphaned: fmt.Sprintf(`{"id":%q,"owner_id":%q}`, orphaned, uuid.NewString()),
		legacy:   fmt.Sprintf(`{"id":%q}`, legacy),
	}
	for id, data := range seed {
		if _, err := db.Exec(`INSERT INTO servers (id, name, spec_id, node_id, state, data) VALUES ($1, 'x', $2, $3, 'offline', $4::jsonb)`,
			id, uuid.NewString(), uuid.NewString(), data); err != nil {
			t.Fatalf("seed server: %v", err)
		}
	}

	if err := Up(dsn); err != nil {
		t.Fatalf("migrate to head: %v", err)
	}
	want := map[string]string{owned: user, orphaned: "", legacy: ""}
	for id, w := range want {
		var got sql.NullString
		if err := db.QueryRow(`SELECT owner_id::text FROM servers WHERE id=$1`, id).Scan(&got); err != nil {
			t.Fatalf("read owner_id: %v", err)
		}
		if got.String != w {
			t.Errorf("server %s owner_id = %q, want %q", id, got.String, w)
		}
	}
}
