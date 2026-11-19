package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/briggleman/kraken/internal/panel/store"
)

// ---- Server share links ----

const shareLinkCols = `id::text, server_id::text, token_hash, label, show_address, show_password,
	COALESCE(created_by::text, ''), created_at, expires_at, revoked_at, last_seen_at`

func (s *Store) CreateShareLink(ctx context.Context, l *store.ShareLink) error {
	if uuid.Validate(l.ServerID) != nil {
		return store.ErrNotFound
	}
	var createdBy any
	if validUser(l.CreatedBy) {
		createdBy = l.CreatedBy
	}
	// Inserting from the server row turns a missing server into no row at all
	// — ErrNotFound — rather than a foreign-key error.
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO server_share_links
		   (id, server_id, token_hash, label, show_address, show_password, created_by, created_at, expires_at)
		 SELECT $1, sv.id, $3, $4, $5, $6, $7::uuid, $8::timestamptz, $9::timestamptz FROM servers sv WHERE sv.id=$2`,
		l.ID, l.ServerID, l.TokenHash, l.Label, l.ShowAddress, l.ShowPassword, createdBy, l.CreatedAt, l.ExpiresAt)
	if err != nil {
		if isUniqueViolation(err) {
			return store.ErrConflict
		}
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

func (s *Store) GetShareLinkByHash(ctx context.Context, hash string) (*store.ShareLink, error) {
	return scanShareLink(s.pool.QueryRow(ctx,
		`SELECT `+shareLinkCols+` FROM server_share_links WHERE token_hash=$1`, hash))
}

func (s *Store) ListShareLinks(ctx context.Context, serverID string) ([]*store.ShareLink, error) {
	out := []*store.ShareLink{}
	if uuid.Validate(serverID) != nil {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+shareLinkCols+` FROM server_share_links
		  WHERE server_id=$1 AND revoked_at IS NULL ORDER BY created_at DESC, id DESC`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		l, err := scanShareLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) RevokeShareLink(ctx context.Context, serverID, id string, at time.Time) error {
	if uuid.Validate(serverID) != nil || uuid.Validate(id) != nil {
		return store.ErrNotFound
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE server_share_links SET revoked_at=$3 WHERE server_id=$1 AND id=$2 AND revoked_at IS NULL`,
		serverID, id, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// TouchShareLink only ever moves last_seen_at forward, so two fetches landing
// out of order cannot wind it back.
func (s *Store) TouchShareLink(ctx context.Context, id string, at time.Time) error {
	if uuid.Validate(id) != nil {
		return store.ErrNotFound
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE server_share_links SET last_seen_at=$2
		  WHERE id=$1 AND (last_seen_at IS NULL OR last_seen_at < $2)`, id, at)
	return err
}

func (s *Store) DeleteShareLinksBefore(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM server_share_links
		  WHERE (revoked_at IS NOT NULL AND revoked_at < $1) OR (expires_at IS NOT NULL AND expires_at < $1)`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func scanShareLink(row pgx.Row) (*store.ShareLink, error) {
	var l store.ShareLink
	err := row.Scan(&l.ID, &l.ServerID, &l.TokenHash, &l.Label, &l.ShowAddress, &l.ShowPassword,
		&l.CreatedBy, &l.CreatedAt, &l.ExpiresAt, &l.RevokedAt, &l.LastSeenAt)
	if notFoundErr(err) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}
