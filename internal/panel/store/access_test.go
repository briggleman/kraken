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
		name  string
		u     *User
		role  *rbac.Role
		owner string
		want  bool
	}{
		{"wildcard reaches anything", bob, owner, "carl", true},
		{"server.any reaches anything", bob, admin, "carl", true},
		{"server.any reaches an ownerless server", bob, admin, "", true},
		{"own server", bob, operator, "bob", true},
		{"someone else's server", bob, operator, "carl", false},
		{"an ownerless server needs server.any", bob, operator, "", false},
		{"no role", bob, nil, "bob", false},
		{"no user", nil, operator, "bob", false},
	}
	for _, c := range cases {
		if got := MayAccessServer(c.u, c.role, c.owner); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
