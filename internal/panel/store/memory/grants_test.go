package memory_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

// Ownership and grants (#146), as the Postgres store has them: the owner is
// SetServerOwner's alone, a grant is idempotent and goes with its server or its
// user, and a deleted owner leaves the server ownerless.
func TestServerOwnershipAndGrants(t *testing.T) {
	st := memory.New()
	ctx := context.Background()
	now := time.Now()
	for _, id := range []string{"ann", "ben", "cat"} {
		if err := st.CreateUser(ctx, &store.User{ID: id, Username: id, RoleID: "operator", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateServer(ctx, &store.Server{ID: "s1", Name: "s1", OwnerID: "ann", State: store.StateOffline, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	owner := func() string {
		t.Helper()
		sv, err := st.GetServer(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}
		return sv.OwnerID
	}

	// A stale copy written back cannot move the owner; only SetServerOwner can.
	stale, _ := st.GetServer(ctx, "s1")
	if err := st.SetServerOwner(ctx, "s1", "ben"); err != nil {
		t.Fatalf("SetServerOwner: %v", err)
	}
	if err := st.UpdateServer(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if got := owner(); got != "ben" {
		t.Fatalf("owner after a stale UpdateServer = %q, want ben", got)
	}
	if err := st.SetServerOwner(ctx, "s1", "nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetServerOwner to an unknown user: %v, want ErrNotFound", err)
	}
	if err := st.SetServerOwner(ctx, "nope", "ann"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetServerOwner on an unknown server: %v, want ErrNotFound", err)
	}

	// Granting twice keeps the first grant.
	if err := st.GrantServer(ctx, &store.ServerGrant{ServerID: "s1", UserID: "cat", GrantedBy: "ben", CreatedAt: now}); err != nil {
		t.Fatalf("GrantServer: %v", err)
	}
	if err := st.GrantServer(ctx, &store.ServerGrant{ServerID: "s1", UserID: "cat", GrantedBy: "ann", CreatedAt: now.Add(time.Minute)}); err != nil {
		t.Fatalf("GrantServer again: %v", err)
	}
	gs, _ := st.ListServerGrants(ctx, "s1")
	if len(gs) != 1 || gs[0].GrantedBy != "ben" {
		t.Fatalf("grants = %+v, want the one first grant by ben", gs)
	}
	if ok, _ := st.HasServerGrant(ctx, "s1", "cat"); !ok {
		t.Fatal("HasServerGrant(cat) = false after the grant")
	}
	if ids, _ := st.ServerIDsGrantedTo(ctx, "cat"); !slices.Equal(ids, []string{"s1"}) {
		t.Fatalf("ServerIDsGrantedTo(cat) = %v, want [s1]", ids)
	}
	if err := st.GrantServer(ctx, &store.ServerGrant{ServerID: "s1", UserID: "nobody"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("grant to an unknown user: %v, want ErrNotFound", err)
	}
	if err := st.GrantServer(ctx, &store.ServerGrant{ServerID: "nope", UserID: "cat"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("grant on an unknown server: %v, want ErrNotFound", err)
	}
	if err := st.RevokeServerGrant(ctx, "s1", "ann"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke a grant that is not there: %v, want ErrNotFound", err)
	}

	// The grantee's deletion takes the grant; the owner's leaves no owner.
	if err := st.DeleteUser(ctx, "cat"); err != nil {
		t.Fatal(err)
	}
	if gs, _ := st.ListServerGrants(ctx, "s1"); len(gs) != 0 {
		t.Fatalf("grants after the grantee was deleted = %+v, want none", gs)
	}
	if err := st.DeleteUser(ctx, "ben"); err != nil {
		t.Fatal(err)
	}
	if got := owner(); got != "" {
		t.Fatalf("owner after the owner was deleted = %q, want none", got)
	}

	// The server's deletion takes its grants.
	if err := st.GrantServer(ctx, &store.ServerGrant{ServerID: "s1", UserID: "ann"}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteServer(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if ids, _ := st.ServerIDsGrantedTo(ctx, "ann"); len(ids) != 0 {
		t.Fatalf("ServerIDsGrantedTo(ann) after the server was deleted = %v, want none", ids)
	}
}
