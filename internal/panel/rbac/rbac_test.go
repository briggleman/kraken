package rbac

import "testing"

func TestRoleHas_Wildcards(t *testing.T) {
	owner := role(RoleOwner)
	admin := role(RoleAdmin)
	operator := role(RoleOperator)
	readonly := role(RoleReadOnly)

	tests := []struct {
		name string
		role *Role
		perm Permission
		want bool
	}{
		{"owner has everything via *", owner, PermUserManage, true},
		{"owner has arbitrary perm", owner, PermNodeManage, true},
		{"admin server.* covers power", admin, PermServerPower, true},
		{"admin spec.* covers manage", admin, PermSpecManage, true},
		{"admin has user.manage explicitly", admin, PermUserManage, true},
		{"operator can power", operator, PermServerPower, true},
		{"operator cannot manage specs", operator, PermSpecManage, false},
		{"operator cannot manage users", operator, PermUserManage, false},
		{"operator cannot manage nodes", operator, PermNodeManage, false},
		{"readonly can view servers", readonly, PermServerView, true},
		{"readonly can read console", readonly, PermServerConsoleRead, true},
		{"readonly cannot send console command", readonly, PermServerConsoleCommand, false},
		{"readonly cannot power", readonly, PermServerPower, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.role.Has(tt.perm); got != tt.want {
				t.Fatalf("%s.Has(%s) = %v, want %v", tt.role.ID, tt.perm, got, tt.want)
			}
		})
	}
}

// wildcard on one domain must not leak into another.
func TestRoleHas_DomainIsolation(t *testing.T) {
	r := &Role{ID: "x", Permissions: []Permission{"server.*"}}
	if r.Has(PermSpecManage) {
		t.Fatal("server.* must not grant spec.manage")
	}
	if !r.Has(PermServerConsoleCommand) {
		t.Fatal("server.* should grant nested server.console.command")
	}
}

func role(id string) *Role {
	for _, r := range BuiltinRoles() {
		if r.ID == id {
			rr := r
			return &rr
		}
	}
	panic("unknown role " + id)
}
