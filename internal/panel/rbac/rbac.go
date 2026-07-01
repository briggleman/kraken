// Package rbac defines the permission vocabulary and the built-in roles for the
// Panel. Roles are sets of permissions; a user has exactly one role (for now),
// and authorization is a membership check against that role's permission set.
package rbac

// Permission is a single granular capability. The naming convention is
// "<domain>.<action>"; a trailing ".*" wildcard grants every action in a domain.
type Permission string

const (
	// Server lifecycle & access.
	PermServerView           Permission = "server.view"
	PermServerCreate         Permission = "server.create"
	PermServerDelete         Permission = "server.delete"
	PermServerPower          Permission = "server.power"
	PermServerConsoleRead    Permission = "server.console.read"
	PermServerConsoleCommand Permission = "server.console.command"
	PermServerFilesRead      Permission = "server.files.read"
	PermServerFilesWrite     Permission = "server.files.write"
	PermServerConfig         Permission = "server.config"
	// PermServerAny lets a role act on servers it does not own (all servers).
	// Held by Owner ("*") and Admin ("server.*") via wildcards; Operator/Read-only
	// do not have it, so they are scoped to servers they created.
	PermServerAny Permission = "server.any"

	// Backups.
	PermBackupManage Permission = "backup.manage"

	// Game spec catalog.
	PermSpecView   Permission = "spec.view"
	PermSpecManage Permission = "spec.manage"

	// Infrastructure & administration.
	PermNodeView   Permission = "node.view"
	PermNodeManage Permission = "node.manage"
	PermUserManage Permission = "user.manage"
	PermAuditView  Permission = "audit.view"

	// Panel-global settings (e.g. the Cloudflare DNS integration).
	PermSettingsView   Permission = "settings.view"
	PermSettingsManage Permission = "settings.manage"

	// PermAll is a superuser wildcard granting every permission.
	PermAll Permission = "*"
)

// AllPermissions is the canonical list of concrete permissions (excluding the
// PermAll wildcard). Used for validation and the role editor UI.
func AllPermissions() []Permission {
	return []Permission{
		PermServerView, PermServerCreate, PermServerDelete, PermServerPower,
		PermServerConsoleRead, PermServerConsoleCommand,
		PermServerFilesRead, PermServerFilesWrite, PermServerConfig, PermServerAny,
		PermBackupManage,
		PermSpecView, PermSpecManage,
		PermNodeView, PermNodeManage, PermUserManage, PermAuditView,
		PermSettingsView, PermSettingsManage,
	}
}

// Role is a named set of permissions. Built-in roles ship with the Panel and
// cannot be deleted (but custom roles may be added later).
type Role struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Builtin     bool         `json:"builtin"`
	Permissions []Permission `json:"permissions"`
}

// Has reports whether the role grants the given permission, honoring the "*"
// superuser wildcard and "<domain>.*" domain wildcards.
func (r *Role) Has(p Permission) bool {
	for _, have := range r.Permissions {
		if have == PermAll || have == p {
			return true
		}
		if domain, ok := wildcardDomain(have); ok && inDomain(p, domain) {
			return true
		}
	}
	return false
}

// wildcardDomain returns the domain prefix of a "<domain>.*" permission.
func wildcardDomain(p Permission) (string, bool) {
	s := string(p)
	if len(s) > 2 && s[len(s)-2:] == ".*" {
		return s[:len(s)-2], true
	}
	return "", false
}

func inDomain(p Permission, domain string) bool {
	s := string(p)
	return len(s) > len(domain)+1 && s[:len(domain)+1] == domain+"."
}

// Built-in role IDs.
const (
	RoleOwner    = "owner"
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleReadOnly = "readonly"
)

// BuiltinRoles returns the default role set described in the platform plan:
// Owner (everything), Admin (manage infra + specs + servers, not owners),
// Operator (operate granted servers), Read-only (view + read console).
func BuiltinRoles() []Role {
	return []Role{
		{
			ID: RoleOwner, Name: "Owner", Builtin: true,
			Permissions: []Permission{PermAll},
		},
		{
			ID: RoleAdmin, Name: "Admin", Builtin: true,
			Permissions: []Permission{
				"server.*", "spec.*", "node.*", "backup.*", "settings.*",
				PermUserManage, PermAuditView,
			},
		},
		{
			ID: RoleOperator, Name: "Operator", Builtin: true,
			Permissions: []Permission{
				PermServerView, PermServerCreate, PermServerPower,
				PermServerConsoleRead, PermServerConsoleCommand,
				PermServerFilesRead, PermServerFilesWrite, PermServerConfig,
				PermBackupManage, PermSpecView, PermNodeView,
			},
		},
		{
			ID: RoleReadOnly, Name: "Read-only", Builtin: true,
			Permissions: []Permission{
				PermServerView, PermServerConsoleRead, PermSpecView, PermNodeView,
			},
		},
	}
}
