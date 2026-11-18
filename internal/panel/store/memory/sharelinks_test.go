package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

// Share links (#475), as the Postgres store has them: never consumed by a
// read, listed newest first without the revoked ones, revoked once, and gone
// with their server.
func TestShareLinks(t *testing.T) {
	st := memory.New()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, id := range []string{"s1", "s2"} {
		if err := st.CreateServer(ctx, &store.Server{ID: id, Name: id, State: store.StateOffline, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	exp := now.Add(time.Hour)
	a := &store.ShareLink{ID: "a", ServerID: "s1", TokenHash: "ha", Label: "general", ShowAddress: true, CreatedBy: "ann", CreatedAt: now}
	b := &store.ShareLink{ID: "b", ServerID: "s1", TokenHash: "hb", CreatedAt: now.Add(time.Second), ExpiresAt: &exp}
	for _, l := range []*store.ShareLink{a, b} {
		if err := st.CreateShareLink(ctx, l); err != nil {
			t.Fatalf("CreateShareLink %s: %v", l.ID, err)
		}
	}
	if err := st.CreateShareLink(ctx, &store.ShareLink{ID: "c", ServerID: "missing", TokenHash: "hc", CreatedAt: now}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("link on a missing server: %v, want ErrNotFound", err)
	}
	if err := st.CreateShareLink(ctx, &store.ShareLink{ID: "d", ServerID: "s1", TokenHash: "ha", CreatedAt: now}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate token hash: %v, want ErrConflict", err)
	}

	got, err := st.GetShareLinkByHash(ctx, "ha")
	if err != nil || got.ID != "a" || got.Label != "general" || !got.ShowAddress || got.ShowPassword || got.CreatedBy != "ann" {
		t.Fatalf("GetShareLinkByHash = %+v, %v", got, err)
	}
	if !got.Live(now) || got.ExpiresAt != nil {
		t.Fatalf("a link with no expiry must be live: %+v", got)
	}
	if _, err := st.GetShareLinkByHash(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown hash: %v", err)
	}

	list, _ := st.ListShareLinks(ctx, "s1")
	if len(list) != 2 || list[0].ID != "b" || list[1].ID != "a" {
		t.Fatalf("list = %v, want b then a", ids(list))
	}
	if l, _ := st.ListShareLinks(ctx, "s2"); len(l) != 0 {
		t.Fatalf("s2 has no links, got %v", ids(l))
	}

	// Touch only moves forward.
	later := now.Add(time.Minute)
	_ = st.TouchShareLink(ctx, "a", later)
	_ = st.TouchShareLink(ctx, "a", now)
	if got, _ := st.GetShareLinkByHash(ctx, "ha"); got.LastSeenAt == nil || !got.LastSeenAt.Equal(later) {
		t.Fatalf("last_seen_at = %v, want %v", got.LastSeenAt, later)
	}
	if err := st.TouchShareLink(ctx, "gone", later); err != nil {
		t.Fatalf("touching a missing link: %v, want nil", err)
	}

	if err := st.RevokeShareLink(ctx, "s2", "a", later); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke through the wrong server: %v, want ErrNotFound", err)
	}
	if err := st.RevokeShareLink(ctx, "s1", "a", later); err != nil {
		t.Fatalf("RevokeShareLink: %v", err)
	}
	if err := st.RevokeShareLink(ctx, "s1", "a", later); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second revoke: %v, want ErrNotFound", err)
	}
	if got, _ := st.GetShareLinkByHash(ctx, "ha"); got.Live(later) {
		t.Fatal("a revoked link must not be live")
	}
	if l, _ := st.ListShareLinks(ctx, "s1"); len(l) != 1 || l[0].ID != "b" {
		t.Fatalf("list after revoke = %v, want b", ids(l))
	}
	if b2, _ := st.GetShareLinkByHash(ctx, "hb"); b2.Live(exp) || !b2.Live(now) {
		t.Fatal("an expiring link is live before its expiry and not at it")
	}

	// Housekeeping takes the revoked and the expired, not the open.
	if n, _ := st.DeleteShareLinksBefore(ctx, now); n != 0 {
		t.Fatalf("nothing is closed before now, deleted %d", n)
	}
	if n, _ := st.DeleteShareLinksBefore(ctx, exp.Add(time.Second)); n != 2 {
		t.Fatalf("deleted %d, want 2 (one revoked, one expired)", n)
	}

	// A link goes with its server.
	c := &store.ShareLink{ID: "e", ServerID: "s2", TokenHash: "he", CreatedAt: now}
	if err := st.CreateShareLink(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteServer(ctx, "s2"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetShareLinkByHash(ctx, "he"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("link outlived its server: %v", err)
	}
}

func ids(ls []*store.ShareLink) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.ID
	}
	return out
}
