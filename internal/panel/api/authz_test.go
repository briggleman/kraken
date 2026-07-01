package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
)

// TestServerOwnershipIsolation is the object-level-authz (IDOR) regression: a
// user without server.any (here Read-only) may only see/act on servers they own,
// while an owner/admin (server.any via wildcard) reaches every server.
func TestServerOwnershipIsolation(t *testing.T) {
	h, st := newTestServerStore(t)
	adminTok := login(t, h) // bootstrap admin → owner role (has server.any)
	ctx := context.Background()

	mkServer := func(id, owner string) {
		if err := st.CreateServer(ctx, &store.Server{ID: id, Name: id, OwnerID: owner, NodeID: "n1", State: store.StateOffline, CreatedAt: time.Now()}); err != nil {
			t.Fatalf("seed server %s: %v", id, err)
		}
	}
	mkServer("srv-other", "someone-else") // owned by another user
	mkServer("srv-bob", "bob")            // owned by bob

	// A Read-only user (has server.view but NOT server.any) with a live session.
	if err := st.CreateUser(ctx, &store.User{ID: "bob", Username: "bob", RoleID: rbac.RoleReadOnly, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := st.CreateSession(ctx, &store.Session{Token: "bob-token", UserID: "bob", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	get := func(tok, id string) int {
		return do(t, h, http.MethodGet, "/api/v1/servers/"+id, tok, nil).Code
	}

	// Bob may not read another user's server → 404 (not 403, so existence isn't leaked).
	if code := get("bob-token", "srv-other"); code != http.StatusNotFound {
		t.Fatalf("bob GET srv-other: got %d, want 404", code)
	}
	// Bob may read his own server.
	if code := get("bob-token", "srv-bob"); code != http.StatusOK {
		t.Fatalf("bob GET srv-bob (owned): got %d, want 200", code)
	}
	// Admin (server.any) reaches any server.
	if code := get(adminTok, "srv-other"); code != http.StatusOK {
		t.Fatalf("admin GET srv-other: got %d, want 200", code)
	}

	// Bob's server list contains only his own server.
	rec := do(t, h, http.MethodGet, "/api/v1/servers", "bob-token", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("bob list: got %d", rec.Code)
	}
	var out struct {
		Servers []struct {
			ID string `json:"id"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(out.Servers) != 1 || out.Servers[0].ID != "srv-bob" {
		t.Fatalf("bob list = %+v, want only srv-bob", out.Servers)
	}
}
