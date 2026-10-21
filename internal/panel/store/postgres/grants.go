package postgres

import (
	"context"

	"github.com/briggleman/kraken/internal/panel/store"
)

// ---- Server ownership and grants ----

// SetServerOwner writes the owner column alone; the row's JSON is never
// touched, so it races no other writer of the server (see store.Server.OwnerID).
func (s *Store) SetServerOwner(ctx context.Context, id, ownerID string) error {
	if !validUser(ownerID) {
		return store.ErrNotFound
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE servers SET owner_id = u.id FROM users u WHERE servers.id=$1 AND u.id=$2`, id, ownerID)
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

func (s *Store) GrantServer(ctx context.Context, g *store.ServerGrant) error {
	if !validUser(g.UserID) {
		return store.ErrNotFound
	}
	var grantedBy any
	if validUser(g.GrantedBy) {
		grantedBy = g.GrantedBy
	}
	// Inserting from the join is what turns a missing server or user into no
	// row at all — ErrNotFound — rather than a foreign-key error; ON CONFLICT
	// keeps the first grant when it already exists, which is still a row the
	// join found, so it reads as success.
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO server_grants (server_id, user_id, granted_by, created_at)
		 SELECT sv.id, u.id, $3::uuid, $4::timestamptz FROM servers sv, users u WHERE sv.id=$1 AND u.id=$2
		 ON CONFLICT (server_id, user_id) DO UPDATE SET server_id = EXCLUDED.server_id`,
		g.ServerID, g.UserID, grantedBy, g.CreatedAt)
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

func (s *Store) RevokeServerGrant(ctx context.Context, serverID, userID string) error {
	if !validUser(userID) {
		return store.ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM server_grants WHERE server_id=$1 AND user_id=$2`, serverID, userID)
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

func (s *Store) ListServerGrants(ctx context.Context, serverID string) ([]*store.ServerGrant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT server_id::text, user_id::text, COALESCE(granted_by::text, ''), created_at
		 FROM server_grants WHERE server_id=$1 ORDER BY created_at, user_id`, serverID)
	if err != nil {
		if notFoundErr(err) {
			return []*store.ServerGrant{}, nil
		}
		return nil, err
	}
	defer rows.Close()
	out := []*store.ServerGrant{}
	for rows.Next() {
		var g store.ServerGrant
		if err := rows.Scan(&g.ServerID, &g.UserID, &g.GrantedBy, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &g)
	}
	return out, rows.Err()
}

func (s *Store) ServerIDsGrantedTo(ctx context.Context, userID string) ([]string, error) {
	out := []string{}
	if !validUser(userID) {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT server_id::text FROM server_grants WHERE user_id=$1 ORDER BY server_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) HasServerGrant(ctx context.Context, serverID, userID string) (bool, error) {
	if !validUser(userID) {
		return false, nil
	}
	var ok bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM server_grants WHERE server_id=$1 AND user_id=$2)`, serverID, userID).Scan(&ok)
	if notFoundErr(err) {
		return false, nil
	}
	return ok, err
}
