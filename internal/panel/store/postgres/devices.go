package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/briggleman/kraken/internal/panel/store"
)

// ---- Push-alert devices ----

const deviceCols = `id, user_id, platform, apns_token, apns_environment, public_key, name, rules,
	created_at, last_seen_at, last_sent_at, token_invalid_at`

// UpsertDevice is one statement, so two registrations of the same install
// racing each other cannot interleave a read and a write. Whether the row is a
// refresh or a takeover is decided inside it, against the row as it stands.
func (s *Store) UpsertDevice(ctx context.Context, d *store.Device) (*store.Device, error) {
	rules, err := marshalRules(d.Rules)
	if err != nil {
		return nil, err
	}
	return s.scanDevice(s.pool.QueryRow(ctx,
		`INSERT INTO device_registrations AS cur
		   (id, user_id, platform, apns_token, apns_environment, public_key, name, rules, created_at, last_seen_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10)
		 ON CONFLICT (id) DO UPDATE SET
		   platform=excluded.platform,
		   apns_token=excluded.apns_token,
		   apns_environment=excluded.apns_environment,
		   public_key=excluded.public_key,
		   name=excluded.name,
		   last_seen_at=excluded.last_seen_at,
		   token_invalid_at=NULL,
		   rules=CASE WHEN cur.user_id=excluded.user_id THEN cur.rules ELSE excluded.rules END,
		   created_at=CASE WHEN cur.user_id=excluded.user_id THEN cur.created_at ELSE excluded.created_at END,
		   last_sent_at=CASE WHEN cur.user_id=excluded.user_id THEN cur.last_sent_at ELSE NULL END,
		   user_id=excluded.user_id
		 RETURNING `+deviceCols,
		d.ID, d.UserID, d.Platform, s.sealToken(d.APNsToken), d.APNsEnvironment, d.PublicKey, d.Name, rules,
		d.CreatedAt, d.LastSeenAt))
}

func (s *Store) GetDevice(ctx context.Context, id string) (*store.Device, error) {
	return s.scanDevice(s.pool.QueryRow(ctx, `SELECT `+deviceCols+` FROM device_registrations WHERE id=$1`, id))
}

func (s *Store) ListDevicesByUser(ctx context.Context, userID string) ([]*store.Device, error) {
	// user_id is a uuid column: a malformed id would be a 22P02 error, and it
	// names nobody, so it has no devices.
	if uuid.Validate(userID) != nil {
		return []*store.Device{}, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+deviceCols+` FROM device_registrations WHERE user_id=$1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	return s.scanDevices(rows)
}

func (s *Store) ListDevices(ctx context.Context) ([]*store.Device, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+deviceCols+` FROM device_registrations ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	return s.scanDevices(rows)
}

func (s *Store) UpdateDeviceRules(ctx context.Context, id string, r store.DeviceRules) error {
	rules, err := marshalRules(r)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE device_registrations SET rules=$2::jsonb WHERE id=$1`, id, rules)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteDevice(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM device_registrations WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteDevicesByUser(ctx context.Context, userID string) (int64, error) {
	if uuid.Validate(userID) != nil {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM device_registrations WHERE user_id=$1`, userID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// MarkDeviceTokenInvalid compares the token in Go, inside a transaction that
// holds the row: the stored token is sealed with a fresh nonce each time, so
// SQL cannot compare it, and the lock keeps a registration from landing
// between the comparison and the write.
func (s *Store) MarkDeviceTokenInvalid(ctx context.Context, id, token string, at time.Time) (bool, error) {
	marked := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var stored string
		err := tx.QueryRow(ctx, `SELECT apns_token FROM device_registrations WHERE id=$1 FOR UPDATE`, id).Scan(&stored)
		if notFoundErr(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if s.openToken(stored) != token {
			return nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE device_registrations SET token_invalid_at=COALESCE(token_invalid_at, $2) WHERE id=$1`, id, at); err != nil {
			return err
		}
		marked = true
		return nil
	})
	return marked, err
}

func (s *Store) TouchDeviceSent(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE device_registrations SET last_sent_at=$2 WHERE id=$1`, id, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) scanDevice(row pgx.Row) (*store.Device, error) {
	var (
		d     store.Device
		token string
		rules []byte
	)
	err := row.Scan(&d.ID, &d.UserID, &d.Platform, &token, &d.APNsEnvironment, &d.PublicKey, &d.Name, &rules,
		&d.CreatedAt, &d.LastSeenAt, &d.LastSentAt, &d.TokenInvalidAt)
	if notFoundErr(err) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.APNsToken = s.openToken(token)
	// Decoded over the defaults, so a class added after this row was written
	// reads as on, the way a new registration would have it.
	d.Rules = store.DefaultDeviceRules()
	if err := json.Unmarshal(rules, &d.Rules); err != nil {
		return nil, err
	}
	if d.Rules.AliveMutedServers == nil {
		d.Rules.AliveMutedServers = []string{}
	}
	return &d, nil
}

func (s *Store) scanDevices(rows pgx.Rows) ([]*store.Device, error) {
	defer rows.Close()
	out := make([]*store.Device, 0)
	for rows.Next() {
		d, err := s.scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// marshalRules writes an empty mute list as [] rather than null, so the stored
// document always has the shape a client reads.
func marshalRules(r store.DeviceRules) (string, error) {
	if r.AliveMutedServers == nil {
		r.AliveMutedServers = []string{}
	}
	b, err := json.Marshal(r)
	return string(b), err
}

func (s *Store) sealToken(token string) string {
	if s.cipher == nil {
		return token
	}
	return s.cipher.EncryptString(token)
}

func (s *Store) openToken(stored string) string {
	if s.cipher == nil {
		return stored
	}
	return s.cipher.DecryptString(stored)
}

// ---- Panel identity ----

// PanelID writes a fresh id only when there is none, then reads whichever id
// is on record. Two Panels starting together against one database (a rolling
// restart) both read the id that won the insert.
func (s *Store) PanelID(ctx context.Context) (string, error) {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO panel_identity (id, panel_id) VALUES (1, $1) ON CONFLICT (id) DO NOTHING`,
		uuid.NewString()); err != nil {
		return "", err
	}
	var id string
	err := s.pool.QueryRow(ctx, `SELECT panel_id FROM panel_identity WHERE id=1`).Scan(&id)
	return id, err
}
