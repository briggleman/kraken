// Package postgres implements store.Store on top of Postgres using pgx. Nested
// structures (role permissions, game specs, node capacity) are stored as JSONB;
// frequently-queried fields (username, slug, status, …) are promoted to columns.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/secrets"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// Store is a Postgres-backed store.Store.
type Store struct {
	pool   *pgxpool.Pool
	cipher *secrets.Cipher // encrypts at-rest secrets (nil = store plaintext)
}

// Option customizes a Store at construction.
type Option func(*Store)

// WithCipher enables encryption-at-rest for reversible secrets (API tokens, the
// CA private key) using the given cipher.
func WithCipher(c *secrets.Cipher) Option {
	return func(s *Store) { s.cipher = c }
}

// New connects to Postgres at databaseURL and returns a Store.
func New(ctx context.Context, databaseURL string, opts ...Option) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	s := &Store{pool: pool}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Close releases the connection pool.
func (s *Store) Close() error { s.pool.Close(); return nil }

var _ store.Store = (*Store)(nil)

// isUniqueViolation reports whether err is a Postgres unique-constraint error.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// notFoundErr reports whether err means "no such row" for a lookup by id: either
// no rows matched, or the supplied id was not a valid UUID (Postgres 22P02,
// invalid_text_representation). Every id column is a uuid, so a malformed id is
// indistinguishable from a missing row and must map to ErrNotFound — not a 500.
func notFoundErr(err error) bool {
	if errors.Is(err, pgx.ErrNoRows) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02"
}

// ---- Users ----

func (s *Store) CreateUser(ctx context.Context, u *store.User) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO users (id, username, email, password_hash, role_id, disabled, must_change_password, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		u.ID, u.Username, u.Email, u.PasswordHash, u.RoleID, u.Disabled, u.MustChangePassword, u.CreatedAt)
	if isUniqueViolation(err) {
		return store.ErrConflict
	}
	return err
}

func (s *Store) scanUser(row pgx.Row) (*store.User, error) {
	var u store.User
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.RoleID, &u.Disabled, &u.MustChangePassword, &u.CreatedAt)
	if notFoundErr(err) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

const userCols = `id, username, email, password_hash, role_id, disabled, must_change_password, created_at`

func (s *Store) GetUser(ctx context.Context, id string) (*store.User, error) {
	return s.scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id=$1`, id))
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (*store.User, error) {
	return s.scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE username=$1`, username))
}

func (s *Store) ListUsers(ctx context.Context) ([]*store.User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.User
	for rows.Next() {
		u, err := s.scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) UpdateUser(ctx context.Context, u *store.User) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET username=$2, email=$3, password_hash=$4, role_id=$5, disabled=$6, must_change_password=$7 WHERE id=$1`,
		u.ID, u.Username, u.Email, u.PasswordHash, u.RoleID, u.Disabled, u.MustChangePassword)
	if err != nil {
		if notFoundErr(err) {
			return store.ErrNotFound
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, id)
	if err != nil {
		if notFoundErr(err) {
			return store.ErrNotFound
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// ---- Roles ----

func (s *Store) GetRole(ctx context.Context, id string) (*rbac.Role, error) {
	var r rbac.Role
	var perms []byte
	err := s.pool.QueryRow(ctx, `SELECT id, name, builtin, permissions FROM roles WHERE id=$1`, id).
		Scan(&r.ID, &r.Name, &r.Builtin, &perms)
	if notFoundErr(err) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(perms, &r.Permissions); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) ListRoles(ctx context.Context) ([]*rbac.Role, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, builtin, permissions FROM roles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*rbac.Role
	for rows.Next() {
		var r rbac.Role
		var perms []byte
		if err := rows.Scan(&r.ID, &r.Name, &r.Builtin, &perms); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(perms, &r.Permissions); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

func (s *Store) UpsertRole(ctx context.Context, r *rbac.Role) error {
	perms, err := json.Marshal(r.Permissions)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO roles (id, name, builtin, permissions) VALUES ($1,$2,$3,$4::jsonb)
		 ON CONFLICT (id) DO UPDATE SET name=excluded.name, builtin=excluded.builtin, permissions=excluded.permissions`,
		r.ID, r.Name, r.Builtin, string(perms))
	return err
}

// ---- Specs ----

func (s *Store) CreateSpec(ctx context.Context, sp *spec.Spec) error {
	data, err := json.Marshal(sp)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO specs (id, slug, version, data) VALUES ($1,$2,$3,$4::jsonb)`,
		sp.ID, sp.Slug, sp.Version, string(data))
	if isUniqueViolation(err) {
		return store.ErrConflict
	}
	return err
}

func (s *Store) scanSpec(row pgx.Row) (*spec.Spec, error) {
	var data []byte
	err := row.Scan(&data)
	if notFoundErr(err) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var sp spec.Spec
	if err := json.Unmarshal(data, &sp); err != nil {
		return nil, err
	}
	return &sp, nil
}

func (s *Store) GetSpec(ctx context.Context, id string) (*spec.Spec, error) {
	return s.scanSpec(s.pool.QueryRow(ctx, `SELECT data FROM specs WHERE id=$1`, id))
}

func (s *Store) GetSpecBySlug(ctx context.Context, slug string) (*spec.Spec, error) {
	return s.scanSpec(s.pool.QueryRow(ctx, `SELECT data FROM specs WHERE slug=$1`, slug))
}

func (s *Store) ListSpecs(ctx context.Context) ([]*spec.Spec, error) {
	rows, err := s.pool.Query(ctx, `SELECT data FROM specs ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*spec.Spec
	for rows.Next() {
		sp, err := s.scanSpec(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

func (s *Store) UpdateSpec(ctx context.Context, sp *spec.Spec) error {
	data, err := json.Marshal(sp)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE specs SET slug=$2, version=$3, data=$4::jsonb, updated_at=now() WHERE id=$1`,
		sp.ID, sp.Slug, sp.Version, string(data))
	if err != nil {
		if notFoundErr(err) {
			return store.ErrNotFound
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteSpec(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM specs WHERE id=$1`, id)
	if err != nil {
		if notFoundErr(err) {
			return store.ErrNotFound
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ---- Nodes ----

func (s *Store) CreateNode(ctx context.Context, n *cluster.Node) error {
	data, err := json.Marshal(n)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO nodes (id, name, os, status, address, data) VALUES ($1,$2,$3,$4,$5,$6::jsonb)`,
		n.ID, n.Name, string(n.OS), string(n.Status), n.Address, string(data))
	if isUniqueViolation(err) {
		return store.ErrConflict
	}
	return err
}

func (s *Store) scanNode(row pgx.Row) (*cluster.Node, error) {
	var data []byte
	err := row.Scan(&data)
	if notFoundErr(err) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var n cluster.Node
	if err := json.Unmarshal(data, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

func (s *Store) GetNode(ctx context.Context, id string) (*cluster.Node, error) {
	return s.scanNode(s.pool.QueryRow(ctx, `SELECT data FROM nodes WHERE id=$1`, id))
}

func (s *Store) ListNodes(ctx context.Context) ([]*cluster.Node, error) {
	rows, err := s.pool.Query(ctx, `SELECT data FROM nodes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*cluster.Node
	for rows.Next() {
		n, err := s.scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) UpdateNode(ctx context.Context, n *cluster.Node) error {
	data, err := json.Marshal(n)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE nodes SET name=$2, os=$3, status=$4, address=$5, data=$6::jsonb WHERE id=$1`,
		n.ID, n.Name, string(n.OS), string(n.Status), n.Address, string(data))
	if err != nil {
		if notFoundErr(err) {
			return store.ErrNotFound
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteNode(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM nodes WHERE id=$1`, id)
	if err != nil {
		if notFoundErr(err) {
			return store.ErrNotFound
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	// Drop any per-node config so a recreated node doesn't inherit stale secrets.
	_, _ = s.pool.Exec(ctx, `DELETE FROM node_config WHERE node_id=$1`, id)
	return nil
}

// ---- Servers ----

func (s *Store) CreateServer(ctx context.Context, sv *store.Server) error {
	data, err := json.Marshal(sv)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO servers (id, name, spec_id, node_id, state, data, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7)`,
		sv.ID, sv.Name, sv.SpecID, sv.NodeID, string(sv.State), string(data), sv.CreatedAt)
	if isUniqueViolation(err) {
		return store.ErrConflict
	}
	return err
}

func (s *Store) scanServer(row pgx.Row) (*store.Server, error) {
	var data []byte
	err := row.Scan(&data)
	if notFoundErr(err) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var sv store.Server
	if err := json.Unmarshal(data, &sv); err != nil {
		return nil, err
	}
	return &sv, nil
}

func (s *Store) GetServer(ctx context.Context, id string) (*store.Server, error) {
	return s.scanServer(s.pool.QueryRow(ctx, `SELECT data FROM servers WHERE id=$1`, id))
}

func (s *Store) ListServers(ctx context.Context) ([]*store.Server, error) {
	rows, err := s.pool.Query(ctx, `SELECT data FROM servers ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.Server
	for rows.Next() {
		sv, err := s.scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}

func (s *Store) UpdateServer(ctx context.Context, sv *store.Server) error {
	data, err := json.Marshal(sv)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE servers SET name=$2, state=$3, data=$4::jsonb WHERE id=$1`,
		sv.ID, sv.Name, string(sv.State), string(data))
	if err != nil {
		if notFoundErr(err) {
			return store.ErrNotFound
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteServer(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM servers WHERE id=$1`, id)
	if err != nil {
		if notFoundErr(err) {
			return store.ErrNotFound
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ---- Schedules ----

func (s *Store) CreateSchedule(ctx context.Context, t *store.ScheduledTask) error {
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO schedules (id, server_id, action, enabled, data, created_at)
		 VALUES ($1,$2,$3,$4,$5::jsonb,$6)`,
		t.ID, t.ServerID, string(t.Action), t.Enabled, string(data), t.CreatedAt)
	if isUniqueViolation(err) {
		return store.ErrConflict
	}
	return err
}

func (s *Store) scanSchedule(row pgx.Row) (*store.ScheduledTask, error) {
	var data []byte
	err := row.Scan(&data)
	if notFoundErr(err) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var t store.ScheduledTask
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) GetSchedule(ctx context.Context, id string) (*store.ScheduledTask, error) {
	return s.scanSchedule(s.pool.QueryRow(ctx, `SELECT data FROM schedules WHERE id=$1`, id))
}

func (s *Store) scanSchedules(rows pgx.Rows) ([]*store.ScheduledTask, error) {
	defer rows.Close()
	var out []*store.ScheduledTask
	for rows.Next() {
		t, err := s.scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) ListSchedules(ctx context.Context) ([]*store.ScheduledTask, error) {
	rows, err := s.pool.Query(ctx, `SELECT data FROM schedules ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	return s.scanSchedules(rows)
}

func (s *Store) ListSchedulesByServer(ctx context.Context, serverID string) ([]*store.ScheduledTask, error) {
	rows, err := s.pool.Query(ctx, `SELECT data FROM schedules WHERE server_id=$1 ORDER BY created_at`, serverID)
	if err != nil {
		return nil, err
	}
	return s.scanSchedules(rows)
}

func (s *Store) UpdateSchedule(ctx context.Context, t *store.ScheduledTask) error {
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE schedules SET action=$2, enabled=$3, data=$4::jsonb WHERE id=$1`,
		t.ID, string(t.Action), t.Enabled, string(data))
	if err != nil {
		if notFoundErr(err) {
			return store.ErrNotFound
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteSchedule(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM schedules WHERE id=$1`, id)
	if err != nil {
		if notFoundErr(err) {
			return store.ErrNotFound
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ---- Audit ----

func (s *Store) AppendAudit(ctx context.Context, e *store.AuditEntry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO audit_log (id, ts, actor, data) VALUES ($1,$2,$3,$4::jsonb)`,
		e.ID, e.Time, e.Actor, string(data))
	return err
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]*store.AuditEntry, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `SELECT data FROM audit_log ORDER BY ts DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.AuditEntry
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var e store.AuditEntry
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// ---- Sessions ----

// Sessions are keyed by a SHA-256 hash of the bearer token, never the token
// itself, so a database dump can't be replayed as live sessions.
func (s *Store) CreateSession(ctx context.Context, sess *store.Session) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (token, user_id, expires_at) VALUES ($1,$2,$3)`,
		store.HashToken(sess.Token), sess.UserID, sess.ExpiresAt)
	return err
}

func (s *Store) GetSession(ctx context.Context, token string) (*store.Session, error) {
	var sess store.Session
	err := s.pool.QueryRow(ctx, `SELECT token, user_id, expires_at FROM sessions WHERE token=$1`, store.HashToken(token)).
		Scan(&sess.Token, &sess.UserID, &sess.ExpiresAt)
	if notFoundErr(err) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token=$1`, store.HashToken(token))
	return err
}

// ---- Cluster CA ----

func (s *Store) GetCA(ctx context.Context) (cert, key []byte, err error) {
	err = s.pool.QueryRow(ctx, `SELECT cert_pem, key_pem FROM cluster_ca WHERE id=1`).Scan(&cert, &key)
	if notFoundErr(err) {
		return nil, nil, store.ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if s.cipher != nil {
		if key, err = s.cipher.DecryptBytes(key); err != nil {
			return nil, nil, fmt.Errorf("postgres: decrypt CA key: %w", err)
		}
	}
	return cert, key, nil
}

func (s *Store) SaveCA(ctx context.Context, cert, key []byte) error {
	if s.cipher != nil {
		key = s.cipher.EncryptBytes(key) // the cert is public; only the key is sealed
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO cluster_ca (id, cert_pem, key_pem) VALUES (1,$1,$2)
		 ON CONFLICT (id) DO UPDATE SET cert_pem=excluded.cert_pem, key_pem=excluded.key_pem, created_at=now()`,
		cert, key)
	return err
}

// ---- Panel settings ----

func (s *Store) GetSettings(ctx context.Context) (*store.Settings, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM panel_settings WHERE id=1`).Scan(&data)
	if notFoundErr(err) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var st store.Settings
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	if s.cipher != nil {
		st.CloudflareAPIToken = s.cipher.DecryptString(st.CloudflareAPIToken)
		st.UnifiAPIKey = s.cipher.DecryptString(st.UnifiAPIKey)
	}
	return &st, nil
}

func (s *Store) SaveSettings(ctx context.Context, st *store.Settings) error {
	enc := *st // copy so we don't mutate the caller's struct
	if s.cipher != nil {
		enc.CloudflareAPIToken = s.cipher.EncryptString(st.CloudflareAPIToken)
		enc.UnifiAPIKey = s.cipher.EncryptString(st.UnifiAPIKey)
	}
	data, err := json.Marshal(&enc)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO panel_settings (id, data) VALUES (1,$1::jsonb)
		 ON CONFLICT (id) DO UPDATE SET data=excluded.data, updated_at=now()`,
		string(data))
	return err
}

// ---- Per-node config ----

func (s *Store) GetNodeConfig(ctx context.Context, nodeID string) (*store.NodeConfig, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM node_config WHERE node_id=$1`, nodeID).Scan(&data)
	if notFoundErr(err) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var c store.NodeConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if s.cipher != nil {
		c.SftpPassword = s.cipher.DecryptString(c.SftpPassword)
		c.SftpPrivateKey = s.cipher.DecryptString(c.SftpPrivateKey)
		c.SteamPassword = s.cipher.DecryptString(c.SteamPassword)
	}
	return &c, nil
}

func (s *Store) SaveNodeConfig(ctx context.Context, nodeID string, c *store.NodeConfig) error {
	enc := *c // copy so we don't mutate the caller's struct
	if s.cipher != nil {
		enc.SftpPassword = s.cipher.EncryptString(c.SftpPassword)
		enc.SftpPrivateKey = s.cipher.EncryptString(c.SftpPrivateKey)
		enc.SteamPassword = s.cipher.EncryptString(c.SteamPassword)
	}
	data, err := json.Marshal(&enc)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO node_config (node_id, data) VALUES ($1,$2::jsonb)
		 ON CONFLICT (node_id) DO UPDATE SET data=excluded.data, updated_at=now()`,
		nodeID, string(data))
	return err
}

func (s *Store) DeleteNodeConfig(ctx context.Context, nodeID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM node_config WHERE node_id=$1`, nodeID)
	return err
}
