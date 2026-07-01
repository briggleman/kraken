package postgres_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/secrets"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/migrate"
	"github.com/briggleman/kraken/internal/panel/store/postgres"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// testDB returns a connected Postgres store, applying migrations first. It skips
// the test if KRAKEN_TEST_DATABASE_URL is unset or the DB is unreachable.
func testDB(t *testing.T) *postgres.Store {
	t.Helper()
	url := os.Getenv("KRAKEN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("KRAKEN_TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	if err := migrate.Up(url); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := postgres.New(ctx, url)
	if err != nil {
		t.Skipf("postgres unreachable: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestPostgresUsersRolesSessions(t *testing.T) {
	st := testDB(t)
	ctx := context.Background()

	// Seed a role, then a user with a unique username.
	role := &rbac.Role{ID: "owner", Name: "Owner", Builtin: true, Permissions: []rbac.Permission{rbac.PermAll}}
	if err := st.UpsertRole(ctx, role); err != nil {
		t.Fatalf("UpsertRole: %v", err)
	}
	got, err := st.GetRole(ctx, "owner")
	if err != nil || len(got.Permissions) != 1 || got.Permissions[0] != rbac.PermAll {
		t.Fatalf("GetRole roundtrip failed: %+v err=%v", got, err)
	}

	u := &store.User{ID: uuid.NewString(), Username: "pg-" + uuid.NewString()[:8], PasswordHash: "h", RoleID: "owner", CreatedAt: time.Now().UTC()}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteUser(ctx, u.ID) })

	// Duplicate username → ErrConflict.
	dup := &store.User{ID: uuid.NewString(), Username: u.Username, PasswordHash: "h", RoleID: "owner", CreatedAt: time.Now().UTC()}
	if err := st.CreateUser(ctx, dup); err != store.ErrConflict {
		t.Fatalf("expected ErrConflict on duplicate username, got %v", err)
	}

	by, err := st.GetUserByUsername(ctx, u.Username)
	if err != nil || by.ID != u.ID {
		t.Fatalf("GetUserByUsername: %+v err=%v", by, err)
	}

	// Missing user → ErrNotFound.
	if _, err := st.GetUser(ctx, uuid.NewString()); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// Session lifecycle.
	sess := &store.Session{Token: uuid.NewString(), UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour).UTC()}
	if err := st.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if gs, err := st.GetSession(ctx, sess.Token); err != nil || gs.UserID != u.ID {
		t.Fatalf("GetSession: %+v err=%v", gs, err)
	}
	if err := st.DeleteSession(ctx, sess.Token); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := st.GetSession(ctx, sess.Token); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

// TestPostgresMalformedIDIsNotFound guards the pen-test finding that a non-UUID id
// on a lookup-by-id must map to ErrNotFound (→ HTTP 404), not a raw Postgres
// 22P02 error (which surfaced as a 500). Covers both read and write paths.
func TestPostgresMalformedIDIsNotFound(t *testing.T) {
	st := testDB(t)
	ctx := context.Background()

	if _, err := st.GetServer(ctx, "not-a-uuid"); err != store.ErrNotFound {
		t.Fatalf("GetServer(malformed): expected ErrNotFound, got %v", err)
	}
	if _, err := st.GetUser(ctx, "'; DROP TABLE users;--"); err != store.ErrNotFound {
		t.Fatalf("GetUser(malformed): expected ErrNotFound, got %v", err)
	}
	if _, err := st.GetNode(ctx, "../../etc"); err != store.ErrNotFound {
		t.Fatalf("GetNode(malformed): expected ErrNotFound, got %v", err)
	}
	if err := st.DeleteServer(ctx, "not-a-uuid"); err != store.ErrNotFound {
		t.Fatalf("DeleteServer(malformed): expected ErrNotFound, got %v", err)
	}
}

func TestPostgresSpecRoundTrip(t *testing.T) {
	st := testDB(t)
	ctx := context.Background()

	sp := &spec.Spec{
		ID: uuid.NewString(), Name: "Valheim", Slug: "valheim-" + uuid.NewString()[:8], Version: 1,
		SteamAppIDs: map[string]int{"linux": 896660},
		Platforms:   []spec.Platform{{Kind: spec.LinuxNative, Image: "img"}},
		Install:     spec.Install{Script: "steamcmd"},
		Startup:     spec.Startup{Command: "./run", Stop: spec.Stop{Type: spec.StopSignal, Value: "SIGINT"}},
		Ports:       []spec.Port{{Name: "game", Protocol: spec.UDP, Default: 2456, Required: true}},
		Resources:   spec.Resources{MinMemoryMB: 2048},
	}
	if err := st.CreateSpec(ctx, sp); err != nil {
		t.Fatalf("CreateSpec: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteSpec(ctx, sp.ID) })

	got, err := st.GetSpecBySlug(ctx, sp.Slug)
	if err != nil {
		t.Fatalf("GetSpecBySlug: %v", err)
	}
	if got.Name != "Valheim" || got.SteamAppIDs["linux"] != 896660 || len(got.Ports) != 1 || got.Ports[0].Default != 2456 {
		t.Fatalf("spec did not round-trip through JSONB: %+v", got)
	}
}

func TestPostgresNodeRoundTrip(t *testing.T) {
	st := testDB(t)
	ctx := context.Background()

	n := &cluster.Node{
		ID: uuid.NewString(), Name: "leviathan", OS: cluster.OSLinux, WineEnabled: true,
		Status: cluster.NodeOnline, Address: "127.0.0.1:9090", TotalMemoryMB: 16384,
		Ports: cluster.NewPortPool(cluster.PortRange{Start: 27000, End: 27100}),
	}
	// Reserve a port so we verify allocations persist through JSONB.
	if _, err := n.Reserve(1024, []cluster.PortRequest{{Name: "game", Preferred: 27015}}); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := st.CreateNode(ctx, n); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteNode(ctx, n.ID) })

	got, err := st.GetNode(ctx, n.ID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.OS != cluster.OSLinux || !got.WineEnabled || got.TotalMemoryMB != 16384 {
		t.Fatalf("node fields did not round-trip: %+v", got)
	}
	if got.Ports == nil || got.Ports.IsFree(27015) {
		t.Fatalf("port reservation did not persist (27015 should be allocated)")
	}
	if got.AllocatedMemoryMB != 1024 {
		t.Fatalf("allocated memory did not persist: %d", got.AllocatedMemoryMB)
	}
}

// TestPostgresEncryptionAtRest verifies that reversible secrets are stored as
// ciphertext (not plaintext) and that session tokens are stored as SHA-256
// digests. It reads the raw columns through a separate connection to confirm
// what actually lands on disk, then confirms the cipher round-trips on read.
func TestPostgresEncryptionAtRest(t *testing.T) {
	url := os.Getenv("KRAKEN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("KRAKEN_TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	if err := migrate.Up(url); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()

	cipher, err := secrets.New(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	st, err := postgres.New(ctx, url, postgres.WithCipher(cipher))
	if err != nil {
		t.Skipf("postgres unreachable: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// A raw connection to inspect on-disk bytes, bypassing the store's cipher.
	raw, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("raw connect: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close(ctx) })

	// ---- Settings: API tokens must be sealed at rest, clear on read. ----
	const cfToken = "cf-plaintext-token-value"
	const unifiKey = "unifi-plaintext-api-key"
	in := &store.Settings{CloudflareAPIToken: cfToken, UnifiURL: "https://10.0.0.1", UnifiAPIKey: unifiKey, UnifiSite: "default"}
	if err := st.SaveSettings(ctx, in); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	var rawSettings string
	if err := raw.QueryRow(ctx, `SELECT data::text FROM panel_settings WHERE id=1`).Scan(&rawSettings); err != nil {
		t.Fatalf("read raw settings: %v", err)
	}
	if strings.Contains(rawSettings, cfToken) || strings.Contains(rawSettings, unifiKey) {
		t.Fatalf("plaintext secret found in panel_settings.data: %s", rawSettings)
	}
	if !strings.Contains(rawSettings, "enc:v1:") {
		t.Fatalf("expected enc:v1: marker in stored settings, got: %s", rawSettings)
	}
	out, err := st.GetSettings(ctx)
	if err != nil || out.CloudflareAPIToken != cfToken || out.UnifiAPIKey != unifiKey {
		t.Fatalf("settings did not decrypt on read: %+v err=%v", out, err)
	}

	// ---- CA private key must be sealed at rest, clear on read. ----
	cert := []byte("-----BEGIN CERTIFICATE-----\npublic\n-----END CERTIFICATE-----\n")
	caKey := []byte("-----BEGIN EC PRIVATE KEY-----\ntop-secret-key-material\n-----END EC PRIVATE KEY-----\n")
	if err := st.SaveCA(ctx, cert, caKey); err != nil {
		t.Fatalf("SaveCA: %v", err)
	}
	var rawKey []byte
	if err := raw.QueryRow(ctx, `SELECT key_pem FROM cluster_ca WHERE id=1`).Scan(&rawKey); err != nil {
		t.Fatalf("read raw CA key: %v", err)
	}
	if bytes.Contains(rawKey, []byte("top-secret-key-material")) {
		t.Fatalf("plaintext CA key found at rest")
	}
	if !bytes.HasPrefix(rawKey, []byte("ENC1")) {
		t.Fatalf("expected ENC1 marker on stored CA key, got prefix %q", rawKey[:min(4, len(rawKey))])
	}
	gotCert, gotKey, err := st.GetCA(ctx)
	if err != nil || !bytes.Equal(gotCert, cert) || !bytes.Equal(gotKey, caKey) {
		t.Fatalf("CA did not decrypt on read: err=%v", err)
	}

	// ---- Session token must be stored as a SHA-256 digest, never the bearer. ----
	const bearer = "raw-bearer-token-should-never-persist"
	sess := &store.Session{Token: bearer, UserID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
	if err := st.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteSession(ctx, bearer) })
	var rawTok string
	if err := raw.QueryRow(ctx, `SELECT token FROM sessions WHERE token=$1`, store.HashToken(bearer)).Scan(&rawTok); err != nil {
		t.Fatalf("read raw session token: %v", err)
	}
	if rawTok == bearer {
		t.Fatalf("raw bearer token persisted to sessions.token")
	}
	if rawTok != store.HashToken(bearer) {
		t.Fatalf("session token not stored as its SHA-256 digest")
	}
	if got, err := st.GetSession(ctx, bearer); err != nil || got.UserID != sess.UserID {
		t.Fatalf("GetSession by bearer failed: %+v err=%v", got, err)
	}
}

// TestPostgresNodeConfigEncryptionAtRest verifies a node's SFTP backup
// credentials (password, private key) are sealed at rest and round-trip on read,
// while non-secret fields persist as plaintext.
func TestPostgresNodeConfigEncryptionAtRest(t *testing.T) {
	url := os.Getenv("KRAKEN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("KRAKEN_TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	if err := migrate.Up(url); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()

	cipher, err := secrets.New(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	st, err := postgres.New(ctx, url, postgres.WithCipher(cipher))
	if err != nil {
		t.Skipf("postgres unreachable: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	raw, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("raw connect: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close(ctx) })

	const (
		sftpPass  = "sftp-password-plaintext"
		sftpKey   = "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret-pem-material\n-----END OPENSSH PRIVATE KEY-----\n"
		steamPass = "steam-password-plaintext"
	)
	nodeID := uuid.NewString()
	in := &store.NodeConfig{
		BackupTarget: "sftp", SftpHost: "backup.example.com:22", SftpUser: "kraken",
		SftpPassword: sftpPass, SftpPrivateKey: sftpKey, SftpBasePath: "/backups",
		ReplicateToSftp: true,
		SteamUsername:   "kraken-steam", SteamPassword: steamPass,
	}
	if err := st.SaveNodeConfig(ctx, nodeID, in); err != nil {
		t.Fatalf("SaveNodeConfig: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteNodeConfig(ctx, nodeID) })

	var rawData string
	if err := raw.QueryRow(ctx, `SELECT data::text FROM node_config WHERE node_id=$1`, nodeID).Scan(&rawData); err != nil {
		t.Fatalf("read raw node_config: %v", err)
	}
	for _, secret := range []string{sftpPass, "secret-pem-material", steamPass} {
		if strings.Contains(rawData, secret) {
			t.Fatalf("plaintext secret %q found in node_config.data: %s", secret, rawData)
		}
	}
	if !strings.Contains(rawData, "enc:v1:") {
		t.Fatalf("expected enc:v1: marker in stored node_config, got: %s", rawData)
	}
	// Non-secret fields stay readable so operators can inspect on-disk config.
	if !strings.Contains(rawData, "backup.example.com:22") {
		t.Fatalf("expected plaintext sftp_host in stored node_config, got: %s", rawData)
	}

	out, err := st.GetNodeConfig(ctx, nodeID)
	if err != nil {
		t.Fatalf("GetNodeConfig: %v", err)
	}
	if out.SteamPassword != steamPass || out.SteamUsername != "kraken-steam" {
		t.Fatalf("steam creds did not round-trip: %+v", out)
	}
	if out.SftpPassword != sftpPass || out.SftpPrivateKey != sftpKey {
		t.Fatalf("node config secrets did not decrypt on read: %+v", out)
	}
	if out.BackupTarget != "sftp" || out.SftpHost != "backup.example.com:22" || !out.ReplicateToSftp {
		t.Fatalf("node config non-secret fields did not round-trip: %+v", out)
	}
}
