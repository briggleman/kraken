package memory

import (
	"cmp"
	"context"
	"slices"

	"github.com/briggleman/kraken/internal/panel/store"
)

// ---- Server ownership and grants ----

// grantKey is a grant's identity: its server and its user together, as the
// Postgres primary key has it.
type grantKey struct{ serverID, userID string }

func (s *Store) SetServerOwner(_ context.Context, id, ownerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sv, ok := s.servers[id]
	if !ok {
		return store.ErrNotFound
	}
	if _, ok := s.users[ownerID]; !ok {
		return store.ErrNotFound
	}
	sv.OwnerID = ownerID
	return nil
}

func (s *Store) GrantServer(_ context.Context, g *store.ServerGrant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[g.ServerID]; !ok {
		return store.ErrNotFound
	}
	if _, ok := s.users[g.UserID]; !ok {
		return store.ErrNotFound
	}
	k := grantKey{g.ServerID, g.UserID}
	if _, ok := s.grants[k]; ok {
		return nil // the first grant stands
	}
	c := *g
	s.grants[k] = &c
	return nil
}

func (s *Store) RevokeServerGrant(_ context.Context, serverID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := grantKey{serverID, userID}
	if _, ok := s.grants[k]; !ok {
		return store.ErrNotFound
	}
	delete(s.grants, k)
	return nil
}

func (s *Store) ListServerGrants(_ context.Context, serverID string) ([]*store.ServerGrant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []*store.ServerGrant{}
	for k, g := range s.grants {
		if k.serverID == serverID {
			c := *g
			out = append(out, &c)
		}
	}
	slices.SortFunc(out, func(a, b *store.ServerGrant) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.UserID, b.UserID))
	})
	return out, nil
}

func (s *Store) ServerIDsGrantedTo(_ context.Context, userID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []string{}
	for k := range s.grants {
		if k.userID == userID {
			out = append(out, k.serverID)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (s *Store) HasServerGrant(_ context.Context, serverID, userID string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.grants[grantKey{serverID, userID}]
	return ok, nil
}
