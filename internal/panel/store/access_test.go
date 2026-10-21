package store

import (
	"testing"

	"github.com/briggleman/kraken/internal/panel/rbac"
)

func TestMayAccessServer(t *testing.T) {
	bob := &User{ID: "bob"}
	owner := &rbac.Role{Permissions: []rbac.Permission{rbac.PermAll}}
	admin := &rbac.Role{Permissions: []rbac.Permission{rbac.PermServerAny}}
	operator := &rbac.Role{Permissions: []rbac.Permission{rbac.PermServerView}}
	cases := []struct {
		name    string
		u       *User
		role    *rbac.Role
		owner   string
		granted bool
		want    bool
	}{
		{"wildcard reaches anything", bob, owner, "carl", false, true},
		{"server.any reaches anything", bob, admin, "carl", false, true},
		{"server.any reaches an ownerless server", bob, admin, "", false, true},
		{"own server", bob, operator, "bob", false, true},
		{"someone else's server", bob, operator, "carl", false, false},
		{"someone else's server, granted", bob, operator, "carl", true, true},
		{"an ownerless server needs server.any", bob, operator, "", false, false},
		{"an ownerless server, granted", bob, operator, "", true, true},
		{"no role, even granted", bob, nil, "bob", true, false},
		{"no user, even granted", nil, operator, "bob", true, false},
	}
	for _, c := range cases {
		asked := false
		granted := func() bool { asked = true; return c.granted }
		if got := MayAccessServer(c.u, c.role, c.owner, granted); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		// The grant is a lookup a database pays for: asked only when neither
		// server.any nor ownership already settled it.
		settled := c.role == nil || c.u == nil || c.role.Has(rbac.PermServerAny) || (c.owner != "" && c.owner == c.u.ID)
		if settled && asked {
			t.Errorf("%s: asked for the grant though the answer was already settled", c.name)
		}
	}
	if MayAccessServer(bob, operator, "carl", nil) {
		t.Error("a nil grant lookup must grant nothing")
	}
}
