package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
)

// The invite table against a real Postgres (CI sets KRAKEN_TEST_DATABASE_URL):
// the partial unique index, the single-shot consume, latest-per-user, the
// revoke, the cascade from users, the prune, and sessions ended by user.
func TestPostgresInvites(t *testing.T) {
	st := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	role := &rbac.Role{ID: "operator", Name: "Operator", Builtin: true, Permissions: []rbac.Permission{rbac.PermServerView}}
	if err := st.UpsertRole(ctx, role); err != nil {
		t.Fatalf("UpsertRole: %v", err)
	}
	mkUser := func() *store.User {
		u := &store.User{ID: uuid.NewString(), Username: "inv-" + uuid.NewString()[:8], PasswordHash: "h", RoleID: "operator", CreatedAt: now}
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		return u
	}
	admin := mkUser()
	u1 := mkUser()
	u2 := mkUser()

	mkInvite := func(user *store.User, created time.Time) *store.Invite {
		return &store.Invite{
			ID: uuid.NewString(), UserID: user.ID, TokenHash: store.HashToken(uuid.NewString()),
			SentTo: user.Username + "@example.org", CreatedBy: admin.ID,
			CreatedAt: created, ExpiresAt: created.Add(24 * time.Hour),
		}
	}

	// Create, mark sent, read back by digest and as the user's latest.
	a := mkInvite(u1, now)
	if err := st.CreateInvite(ctx, a); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := st.MarkInviteSent(ctx, a.ID, now); err != nil {
		t.Fatalf("MarkInviteSent: %v", err)
	}
	got, err := st.GetInviteByHash(ctx, a.TokenHash)
	if err != nil || got.ID != a.ID || got.UserID != u1.ID || got.CreatedBy != admin.ID || got.SentAt == nil || got.SentTo != a.SentTo {
		t.Fatalf("GetInviteByHash: %+v err=%v", got, err)
	}
	if !got.ExpiresAt.Equal(a.ExpiresAt) {
		t.Fatalf("expires_at %s != %s", got.ExpiresAt, a.ExpiresAt)
	}
	if !got.Live(now.Add(time.Hour)) {
		t.Fatal("a sent, unexpired invite is not live")
	}

	// One open invite per user: the partial unique index refuses a second.
	if err := st.CreateInvite(ctx, mkInvite(u1, now.Add(time.Second))); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second open invite: %v, want ErrConflict", err)
	}
	// Revoke, then a new one is fine and becomes the latest.
	if n, err := st.RevokeInvitesByUser(ctx, u1.ID, now.Add(time.Second)); err != nil || n != 1 {
		t.Fatalf("RevokeInvitesByUser: n=%d err=%v", n, err)
	}
	b := mkInvite(u1, now.Add(2*time.Second))
	if err := st.CreateInvite(ctx, b); err != nil {
		t.Fatalf("CreateInvite after revoke: %v", err)
	}
	if err := st.MarkInviteSent(ctx, b.ID, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	latest, err := st.LatestInviteByUser(ctx, u1.ID)
	if err != nil || latest.ID != b.ID {
		t.Fatalf("LatestInviteByUser: %+v err=%v", latest, err)
	}
	if _, err := st.LatestInviteByUser(ctx, u2.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("latest for a user with none: %v", err)
	}
	if _, err := st.LatestInviteByUser(ctx, "not-a-uuid"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("latest for a malformed id: %v", err)
	}

	// The revoked one is no longer live or open; the list of latest invites
	// has one row for u1 (b) and, once u2 has one, one for u2.
	if old, _ := st.GetInviteByHash(ctx, a.TokenHash); old.Open() || old.Live(now.Add(time.Hour)) {
		t.Fatalf("revoked invite still open/live: %+v", old)
	}
	c := mkInvite(u2, now)
	c.SentAt = nil // a send that failed: never redeemable
	if err := st.CreateInvite(ctx, c); err != nil {
		t.Fatal(err)
	}
	all, err := st.ListLatestInvites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, inv := range all {
		seen[inv.UserID] = inv.ID
	}
	if seen[u1.ID] != b.ID || seen[u2.ID] != c.ID {
		t.Fatalf("ListLatestInvites: %v", seen)
	}

	// An unsent invite cannot be consumed; neither can an expired one.
	if err := st.ConsumeInvite(ctx, c.TokenHash, now.Add(time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unsent invite consumed: %v", err)
	}
	if err := st.ConsumeInvite(ctx, b.TokenHash, now.Add(25*time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired invite consumed: %v", err)
	}

	// Sixteen redemptions of b at once: exactly one wins.
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := st.ConsumeInvite(ctx, b.TokenHash, now.Add(time.Hour)); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d redemptions won, want 1", wins)
	}
	if done, _ := st.GetInviteByHash(ctx, b.TokenHash); done.ConsumedAt == nil || done.Open() {
		t.Fatalf("consumed invite: %+v", done)
	}

	// Sessions end by user, and only that user's.
	for _, s := range []struct{ tok, user string }{{"s1-" + u1.ID, u1.ID}, {"s2-" + u1.ID, u1.ID}, {"s3-" + u2.ID, u2.ID}} {
		if err := st.CreateSession(ctx, &store.Session{Token: s.tok, UserID: s.user, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DeleteSessionsByUser(ctx, u1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSession(ctx, "s1-"+u1.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("u1's session survived DeleteSessionsByUser")
	}
	if _, err := st.GetSession(ctx, "s3-"+u2.ID); err != nil {
		t.Fatal("u2's session was ended with u1's")
	}

	// Prune: closed rows older than the cutoff go; an open, unexpired one
	// stays whatever the cutoff. u1 has two closed rows (a revoked, b
	// consumed); u2's is open and not yet expired.
	if n, err := st.DeleteInvitesBefore(ctx, now.Add(time.Hour)); err != nil || n != 2 {
		t.Fatalf("DeleteInvitesBefore: n=%d err=%v, want 2", n, err)
	}
	if _, err := st.GetInviteByHash(ctx, c.TokenHash); err != nil {
		t.Fatalf("open invite pruned: %v", err)
	}

	// Deleting the user takes their invite with them (ON DELETE CASCADE),
	// and the sender's deletion leaves the record (created_by is a plain
	// reference).
	if err := st.DeleteUser(ctx, admin.ID); err != nil {
		t.Fatal(err)
	}
	if kept, err := st.GetInviteByHash(ctx, c.TokenHash); err != nil || kept.CreatedBy != admin.ID {
		t.Fatalf("invite after its sender was deleted: %+v err=%v", kept, err)
	}
	if err := st.DeleteUser(ctx, u2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetInviteByHash(ctx, c.TokenHash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("invite outlived its user: %v", err)
	}
	_ = st.DeleteUser(ctx, u1.ID)
}
