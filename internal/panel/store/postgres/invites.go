package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/briggleman/kraken/internal/panel/store"
)

// ---- Account invites ----

const inviteCols = `id, user_id, token_hash, sent_to, COALESCE(created_by::text, ''), created_at, expires_at,
	sent_at, consumed_at, revoked_at`

func (s *Store) CreateInvite(ctx context.Context, inv *store.Invite) error {
	var createdBy any
	if validUser(inv.CreatedBy) {
		createdBy = inv.CreatedBy
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_invites (id, user_id, token_hash, sent_to, created_by, created_at, expires_at, sent_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		inv.ID, inv.UserID, inv.TokenHash, inv.SentTo, createdBy, inv.CreatedAt, inv.ExpiresAt, inv.SentAt)
	if isUniqueViolation(err) {
		return store.ErrConflict
	}
	return err
}

func (s *Store) MarkInviteSent(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE user_invites SET sent_at=$2 WHERE id=$1`, id, at)
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

func (s *Store) GetInviteByHash(ctx context.Context, hash string) (*store.Invite, error) {
	return scanInvite(s.pool.QueryRow(ctx, `SELECT `+inviteCols+` FROM user_invites WHERE token_hash=$1`, hash))
}

func (s *Store) LatestInviteByUser(ctx context.Context, userID string) (*store.Invite, error) {
	if !validUser(userID) {
		return nil, store.ErrNotFound
	}
	return scanInvite(s.pool.QueryRow(ctx,
		`SELECT `+inviteCols+` FROM user_invites WHERE user_id=$1 ORDER BY created_at DESC, id DESC LIMIT 1`, userID))
}

func (s *Store) ListLatestInvites(ctx context.Context) ([]*store.Invite, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT ON (user_id) `+inviteCols+` FROM user_invites ORDER BY user_id, created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*store.Invite{}
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// ConsumeInvite is one UPDATE whose WHERE is the whole liveness test, so two
// redemptions racing each other cannot both succeed: the second finds no
// row left to update. Expiry is judged against the Panel's clock (at), as
// sessions are, not the database's.
func (s *Store) ConsumeInvite(ctx context.Context, hash string, at time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE user_invites SET consumed_at=$2
		  WHERE token_hash=$1 AND sent_at IS NOT NULL AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > $2`,
		hash, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) RevokeInvitesByUser(ctx context.Context, userID string, at time.Time) (int64, error) {
	if !validUser(userID) {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE user_invites SET revoked_at=$2 WHERE user_id=$1 AND consumed_at IS NULL AND revoked_at IS NULL`, userID, at)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *Store) DeleteInvitesBefore(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM user_invites
		  WHERE created_at < $1 AND (consumed_at IS NOT NULL OR revoked_at IS NOT NULL OR expires_at < $1)`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func scanInvite(row pgx.Row) (*store.Invite, error) {
	var inv store.Invite
	err := row.Scan(&inv.ID, &inv.UserID, &inv.TokenHash, &inv.SentTo, &inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt,
		&inv.SentAt, &inv.ConsumedAt, &inv.RevokedAt)
	if notFoundErr(err) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

// DeleteSessionsByUser ends every session of the user.
func (s *Store) DeleteSessionsByUser(ctx context.Context, userID string) error {
	if !validUser(userID) {
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID)
	return err
}
