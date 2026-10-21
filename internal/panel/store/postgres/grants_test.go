package postgres_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
)

// Ownership and grants (#146) against the real schema: the owner column is
// SetServerOwner's alone and survives a stale full-document write, grants are
// idempotent, and the foreign keys take a grant with its server or its user
// and clear a deleted owner.
func TestPostgresServerOwnershipAndGrants(t *testing.T) {
	st := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	if err := st.UpsertRole(ctx, &rbac.Role{ID: "operator", Name: "Operator", Builtin: true, Permissions: []rbac.Permission{rbac.PermServerView}}); err != nil {
		t.Fatalf("UpsertRole: %v", err)
	}
	mkUser := func() string {
		u := &store.User{ID: uuid.NewString(), Username: "grant-" + uuid.NewString()[:8], PasswordHash: "h", RoleID: "operator", CreatedAt: now}
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		return u.ID
	}
	ann, ben, cat := mkUser(), mkUser(), mkUser()
	sid := uuid.NewString()
	if err := st.CreateServer(ctx, &store.Server{ID: sid, Name: "grants", OwnerID: ann, SpecID: uuid.NewString(), NodeID: uuid.NewString(), State: store.StateOffline, CreatedAt: now}); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteServer(context.Background(), sid) })
	owner := func() string {
		t.Helper()
		sv, err := st.GetServer(ctx, sid)
		if err != nil {
			t.Fatalf("GetServer: %v", err)
		}
		return sv.OwnerID
	}
	if got := owner(); got != ann {
		t.Fatalf("owner after create = %q, want ann", got)
	}

	stale, _ := st.GetServer(ctx, sid)
	if err := st.SetServerOwner(ctx, sid, ben); err != nil {
		t.Fatalf("SetServerOwner: %v", err)
	}
	if err := st.UpdateServer(ctx, stale); err != nil {
		t.Fatalf("UpdateServer: %v", err)
	}
	if got := owner(); got != ben {
		t.Fatalf("owner after a stale UpdateServer = %q, want ben", got)
	}
	if err := st.SetServerOwner(ctx, sid, uuid.NewString()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetServerOwner to an unknown user: %v, want ErrNotFound", err)
	}
	if err := st.SetServerOwner(ctx, uuid.NewString(), ann); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetServerOwner on an unknown server: %v, want ErrNotFound", err)
	}

	if err := st.GrantServer(ctx, &store.ServerGrant{ServerID: sid, UserID: cat, GrantedBy: ben, CreatedAt: now}); err != nil {
		t.Fatalf("GrantServer: %v", err)
	}
	if err := st.GrantServer(ctx, &store.ServerGrant{ServerID: sid, UserID: cat, GrantedBy: ann, CreatedAt: now.Add(time.Minute)}); err != nil {
		t.Fatalf("GrantServer again: %v", err)
	}
	gs, err := st.ListServerGrants(ctx, sid)
	if err != nil || len(gs) != 1 || gs[0].GrantedBy != ben || !gs[0].CreatedAt.Equal(now) {
		t.Fatalf("grants = %+v (%v), want the one first grant by ben", gs, err)
	}
	if ok, _ := st.HasServerGrant(ctx, sid, cat); !ok {
		t.Fatal("HasServerGrant(cat) = false after the grant")
	}
	if ids, _ := st.ServerIDsGrantedTo(ctx, cat); !slices.Equal(ids, []string{sid}) {
		t.Fatalf("ServerIDsGrantedTo(cat) = %v, want [%s]", ids, sid)
	}
	if err := st.GrantServer(ctx, &store.ServerGrant{ServerID: sid, UserID: uuid.NewString()}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("grant to an unknown user: %v, want ErrNotFound", err)
	}
	if err := st.GrantServer(ctx, &store.ServerGrant{ServerID: uuid.NewString(), UserID: cat}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("grant on an unknown server: %v, want ErrNotFound", err)
	}
	if err := st.GrantServer(ctx, &store.ServerGrant{ServerID: "not-a-uuid", UserID: cat}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("grant on a malformed server id: %v, want ErrNotFound", err)
	}
	if err := st.RevokeServerGrant(ctx, sid, ann); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke a grant that is not there: %v, want ErrNotFound", err)
	}

	if err := st.DeleteUser(ctx, cat); err != nil {
		t.Fatalf("DeleteUser(cat): %v", err)
	}
	if gs, _ := st.ListServerGrants(ctx, sid); len(gs) != 0 {
		t.Fatalf("grants after the grantee was deleted = %+v, want none", gs)
	}
	if err := st.DeleteUser(ctx, ben); err != nil {
		t.Fatalf("DeleteUser(ben): %v", err)
	}
	if got := owner(); got != "" {
		t.Fatalf("owner after the owner was deleted = %q, want none", got)
	}

	if err := st.GrantServer(ctx, &store.ServerGrant{ServerID: sid, UserID: ann, CreatedAt: now}); err != nil {
		t.Fatalf("GrantServer(ann): %v", err)
	}
	if err := st.DeleteServer(ctx, sid); err != nil {
		t.Fatalf("DeleteServer: %v", err)
	}
	if ids, _ := st.ServerIDsGrantedTo(ctx, ann); len(ids) != 0 {
		t.Fatalf("ServerIDsGrantedTo(ann) after the server was deleted = %v, want none", ids)
	}
}
