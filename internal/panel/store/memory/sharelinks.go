package memory

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
)

// ---- Server share links ----

// copyShareLink returns a deep copy, so a caller holding the result never
// shares the stored row's time pointers.
func copyShareLink(l *store.ShareLink) *store.ShareLink {
	c := *l
	for _, p := range []**time.Time{&c.ExpiresAt, &c.RevokedAt, &c.LastSeenAt} {
		if *p != nil {
			t := **p
			*p = &t
		}
	}
	return &c
}

func (s *Store) CreateShareLink(_ context.Context, l *store.ShareLink) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[l.ServerID]; !ok {
		return store.ErrNotFound
	}
	for _, x := range s.shares {
		if x.TokenHash == l.TokenHash || x.ID == l.ID {
			return store.ErrConflict
		}
	}
	s.shares[l.ID] = copyShareLink(l)
	return nil
}

func (s *Store) GetShareLinkByHash(_ context.Context, hash string) (*store.ShareLink, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, l := range s.shares {
		if l.TokenHash == hash {
			return copyShareLink(l), nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) ListShareLinks(_ context.Context, serverID string) ([]*store.ShareLink, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []*store.ShareLink{}
	for _, l := range s.shares {
		if l.ServerID == serverID && l.RevokedAt == nil {
			out = append(out, copyShareLink(l))
		}
	}
	// Newest first, ties broken by id descending, as the Postgres query orders.
	slices.SortFunc(out, func(a, b *store.ShareLink) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	return out, nil
}

func (s *Store) RevokeShareLink(_ context.Context, serverID, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.shares[id]
	if !ok || l.ServerID != serverID || l.RevokedAt != nil {
		return store.ErrNotFound
	}
	t := at
	l.RevokedAt = &t
	return nil
}

func (s *Store) TouchShareLink(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.shares[id]
	if !ok {
		return nil // a link deleted meanwhile has nothing to record, as in Postgres
	}
	if l.LastSeenAt == nil || l.LastSeenAt.Before(at) {
		t := at
		l.LastSeenAt = &t
	}
	return nil
}

func (s *Store) DeleteShareLinksBefore(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for k, l := range s.shares {
		if (l.RevokedAt != nil && l.RevokedAt.Before(before)) || (l.ExpiresAt != nil && l.ExpiresAt.Before(before)) {
			delete(s.shares, k)
			n++
		}
	}
	return n, nil
}
