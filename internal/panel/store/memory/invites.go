package memory

import (
	"context"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
)

// ---- Account invites ----

func cloneInvite(inv *store.Invite) *store.Invite {
	c := *inv
	c.SentAt = cloneTime(inv.SentAt)
	c.ConsumedAt = cloneTime(inv.ConsumedAt)
	c.RevokedAt = cloneTime(inv.RevokedAt)
	return &c
}

// newer reports whether a should be read as the more recent of two invites:
// by creation time, then by id, the order the Postgres store uses.
func newer(a, b *store.Invite) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID > b.ID
}

func (s *Store) CreateInvite(_ context.Context, inv *store.Invite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.invites[inv.ID]; ok {
		return store.ErrConflict
	}
	for _, cur := range s.invites {
		// The partial unique index: one open invite per user, and one row
		// per token digest.
		if cur.TokenHash == inv.TokenHash || (cur.UserID == inv.UserID && cur.Open()) {
			return store.ErrConflict
		}
	}
	s.invites[inv.ID] = cloneInvite(inv)
	return nil
}

func (s *Store) MarkInviteSent(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.invites[id]
	if !ok {
		return store.ErrNotFound
	}
	inv.SentAt = cloneTime(&at)
	return nil
}

func (s *Store) GetInviteByHash(_ context.Context, hash string) (*store.Invite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, inv := range s.invites {
		if inv.TokenHash == hash {
			return cloneInvite(inv), nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) LatestInviteByUser(_ context.Context, userID string) (*store.Invite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var latest *store.Invite
	for _, inv := range s.invites {
		if inv.UserID == userID && (latest == nil || newer(inv, latest)) {
			latest = inv
		}
	}
	if latest == nil {
		return nil, store.ErrNotFound
	}
	return cloneInvite(latest), nil
}

func (s *Store) ListLatestInvites(_ context.Context) ([]*store.Invite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	latest := map[string]*store.Invite{}
	for _, inv := range s.invites {
		if cur, ok := latest[inv.UserID]; !ok || newer(inv, cur) {
			latest[inv.UserID] = inv
		}
	}
	out := make([]*store.Invite, 0, len(latest))
	for _, inv := range latest {
		out = append(out, cloneInvite(inv))
	}
	return out, nil
}

func (s *Store) ConsumeInvite(_ context.Context, hash string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, inv := range s.invites {
		if inv.TokenHash == hash && inv.Live(at) {
			inv.ConsumedAt = cloneTime(&at)
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *Store) RevokeInvitesByUser(_ context.Context, userID string, at time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, inv := range s.invites {
		if inv.UserID == userID && inv.Open() {
			inv.RevokedAt = cloneTime(&at)
			n++
		}
	}
	return n, nil
}

func (s *Store) DeleteInvitesBefore(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for id, inv := range s.invites {
		if inv.CreatedAt.Before(before) && (!inv.Open() || inv.ExpiresAt.Before(before)) {
			delete(s.invites, id)
			n++
		}
	}
	return n, nil
}
