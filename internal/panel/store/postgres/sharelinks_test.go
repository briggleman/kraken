package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/store"
)

// Share links (#475) against the real schema: never consumed by a read,
// listed newest first without the revoked ones, revoked once, last_seen_at
// only moves forward, and the foreign key takes a link with its server.
func TestPostgresShareLinks(t *testing.T) {
	st := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	mkServer := func() string {
		id := uuid.NewString()
		if err := st.CreateServer(ctx, &store.Server{ID: id, Name: "share", SpecID: uuid.NewString(), NodeID: uuid.NewString(), State: store.StateOffline, CreatedAt: now}); err != nil {
			t.Fatalf("CreateServer: %v", err)
		}
		t.Cleanup(func() { _ = st.DeleteServer(context.Background(), id) })
		return id
	}
	s1, s2 := mkServer(), mkServer()
	exp := now.Add(time.Hour)
	tok := func() string { return store.HashToken(uuid.NewString()) }
	a := &store.ShareLink{ID: uuid.NewString(), ServerID: s1, TokenHash: tok(), Label: "general", ShowAddress: true, CreatedBy: uuid.NewString(), CreatedAt: now}
	b := &store.ShareLink{ID: uuid.NewString(), ServerID: s1, TokenHash: tok(), CreatedAt: now.Add(time.Second), ExpiresAt: &exp}
	for _, l := range []*store.ShareLink{a, b} {
		if err := st.CreateShareLink(ctx, l); err != nil {
			t.Fatalf("CreateShareLink: %v", err)
		}
	}
	if err := st.CreateShareLink(ctx, &store.ShareLink{ID: uuid.NewString(), ServerID: uuid.NewString(), TokenHash: tok(), CreatedAt: now}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("link on a missing server: %v, want ErrNotFound", err)
	}
	if err := st.CreateShareLink(ctx, &store.ShareLink{ID: uuid.NewString(), ServerID: "not-a-uuid", TokenHash: tok(), CreatedAt: now}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("link on a malformed server id: %v, want ErrNotFound", err)
	}
	if err := st.CreateShareLink(ctx, &store.ShareLink{ID: uuid.NewString(), ServerID: s1, TokenHash: a.TokenHash, CreatedAt: now}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate token hash: %v, want ErrConflict", err)
	}

	got, err := st.GetShareLinkByHash(ctx, a.TokenHash)
	if err != nil || got.ID != a.ID || got.ServerID != s1 || got.Label != "general" || !got.ShowAddress || got.ShowPassword || got.CreatedBy != a.CreatedBy || !got.CreatedAt.Equal(now) || got.ExpiresAt != nil {
		t.Fatalf("GetShareLinkByHash = %+v, %v", got, err)
	}
	if _, err := st.GetShareLinkByHash(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown hash: %v", err)
	}
	if b2, _ := st.GetShareLinkByHash(ctx, b.TokenHash); b2.ExpiresAt == nil || !b2.ExpiresAt.Equal(exp) {
		t.Fatalf("expires_at round trip: %+v", b2)
	}

	list, err := st.ListShareLinks(ctx, s1)
	if err != nil || len(list) != 2 || list[0].ID != b.ID || list[1].ID != a.ID {
		t.Fatalf("list = %v, %v; want b then a", list, err)
	}
	if l, _ := st.ListShareLinks(ctx, "not-a-uuid"); len(l) != 0 {
		t.Fatalf("malformed server id lists nothing, got %d", len(l))
	}

	later := now.Add(time.Minute)
	if err := st.TouchShareLink(ctx, a.ID, later); err != nil {
		t.Fatalf("TouchShareLink: %v", err)
	}
	_ = st.TouchShareLink(ctx, a.ID, now)
	if got, _ := st.GetShareLinkByHash(ctx, a.TokenHash); got.LastSeenAt == nil || !got.LastSeenAt.Equal(later) {
		t.Fatalf("last_seen_at = %v, want %v", got.LastSeenAt, later)
	}

	if err := st.RevokeShareLink(ctx, s2, a.ID, later); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke through the wrong server: %v", err)
	}
	if err := st.RevokeShareLink(ctx, s1, a.ID, later); err != nil {
		t.Fatalf("RevokeShareLink: %v", err)
	}
	if err := st.RevokeShareLink(ctx, s1, a.ID, later); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second revoke: %v", err)
	}
	if l, _ := st.ListShareLinks(ctx, s1); len(l) != 1 || l[0].ID != b.ID {
		t.Fatalf("list after revoke has %d", len(l))
	}

	// Housekeeping, scoped to this test's rows by checking them afterwards.
	if _, err := st.DeleteShareLinksBefore(ctx, exp.Add(time.Second)); err != nil {
		t.Fatalf("DeleteShareLinksBefore: %v", err)
	}
	for _, h := range []string{a.TokenHash, b.TokenHash} {
		if _, err := st.GetShareLinkByHash(ctx, h); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("closed link survived housekeeping: %v", err)
		}
	}

	c := &store.ShareLink{ID: uuid.NewString(), ServerID: s2, TokenHash: tok(), CreatedAt: now}
	if err := st.CreateShareLink(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteServer(ctx, s2); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetShareLinkByHash(ctx, c.TokenHash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("link outlived its server: %v", err)
	}
}
