package memory_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

func newInvite(user, hash string, created time.Time) *store.Invite {
	sent := created
	return &store.Invite{
		ID: "inv-" + hash, UserID: user, TokenHash: hash, SentTo: user + "@example.org",
		CreatedAt: created, ExpiresAt: created.Add(24 * time.Hour), SentAt: &sent,
	}
}

// One open invite per user, one row per digest.
func TestInvites_OneOpenPerUser(t *testing.T) {
	st := memory.New()
	ctx := context.Background()
	now := time.Now()
	if err := st.CreateInvite(ctx, newInvite("u1", "h1", now)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInvite(ctx, newInvite("u1", "h2", now)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second open invite for the same user: %v", err)
	}
	if err := st.CreateInvite(ctx, newInvite("u2", "h1", now)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate digest: %v", err)
	}
	if n, _ := st.RevokeInvitesByUser(ctx, "u1", now); n != 1 {
		t.Fatalf("revoked %d, want 1", n)
	}
	if err := st.CreateInvite(ctx, newInvite("u1", "h2", now.Add(time.Second))); err != nil {
		t.Fatalf("after revoke: %v", err)
	}
	latest, err := st.LatestInviteByUser(ctx, "u1")
	if err != nil || latest.TokenHash != "h2" {
		t.Fatalf("latest = %+v (%v)", latest, err)
	}
	all, _ := st.ListLatestInvites(ctx)
	if len(all) != 1 || all[0].TokenHash != "h2" {
		t.Fatalf("list latest = %+v", all)
	}
}

// Many redemptions of one link, at once: exactly one wins.
func TestInvites_ConsumeIsSingleShot(t *testing.T) {
	st := memory.New()
	ctx := context.Background()
	now := time.Now()
	if err := st.CreateInvite(ctx, newInvite("u1", "h1", now)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := st.ConsumeInvite(ctx, "h1", now.Add(time.Minute)); err == nil {
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
	// Not live any more, in every reading.
	inv, _ := st.GetInviteByHash(ctx, "h1")
	if inv.Live(now.Add(time.Minute)) || inv.Open() {
		t.Fatalf("consumed invite still live/open: %+v", inv)
	}
}

// An undelivered or expired invite is never redeemable.
func TestInvites_UnsentAndExpiredCannotBeConsumed(t *testing.T) {
	st := memory.New()
	ctx := context.Background()
	now := time.Now()
	unsent := newInvite("u1", "h1", now)
	unsent.SentAt = nil
	if err := st.CreateInvite(ctx, unsent); err != nil {
		t.Fatal(err)
	}
	if err := st.ConsumeInvite(ctx, "h1", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unsent invite consumed: %v", err)
	}
	if err := st.MarkInviteSent(ctx, unsent.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := st.ConsumeInvite(ctx, "h1", now.Add(25*time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired invite consumed: %v", err)
	}
	if err := st.ConsumeInvite(ctx, "h1", now.Add(time.Hour)); err != nil {
		t.Fatalf("live invite refused: %v", err)
	}
}

func TestInvites_DeleteWithUserAndPrune(t *testing.T) {
	st := memory.New()
	ctx := context.Background()
	now := time.Now()
	for _, id := range []string{"u1", "u2"} {
		if err := st.CreateUser(ctx, &store.User{ID: id, Username: id, PasswordHash: "x", RoleID: "operator", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateInvite(ctx, newInvite("u1", "h1", now.Add(-48*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInvite(ctx, newInvite("u2", "h2", now)); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetInviteByHash(ctx, "h1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("invite outlived its user: %v", err)
	}
	// u2's invite is open and fresh: not pruned, whatever the window.
	if n, _ := st.DeleteInvitesBefore(ctx, now.Add(time.Hour)); n != 0 {
		t.Fatalf("pruned an open fresh invite (%d)", n)
	}
	if _, err := st.RevokeInvitesByUser(ctx, "u2", now); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.DeleteInvitesBefore(ctx, now.Add(-time.Hour)); n != 0 {
		t.Fatalf("pruned a row newer than the window (%d)", n)
	}
	if n, _ := st.DeleteInvitesBefore(ctx, now.Add(time.Hour)); n != 1 {
		t.Fatalf("closed row not pruned (%d)", n)
	}
}

func TestSessions_DeleteByUser(t *testing.T) {
	st := memory.New()
	ctx := context.Background()
	for _, s := range []struct{ tok, user string }{{"a", "u1"}, {"b", "u1"}, {"c", "u2"}} {
		if err := st.CreateSession(ctx, &store.Session{Token: s.tok, UserID: s.user, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DeleteSessionsByUser(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"a", "b"} {
		if _, err := st.GetSession(ctx, tok); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("session %s of u1 survived", tok)
		}
	}
	if _, err := st.GetSession(ctx, "c"); err != nil {
		t.Fatal("another user's session was ended")
	}
}
