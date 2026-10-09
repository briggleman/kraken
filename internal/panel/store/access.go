package store

import "github.com/briggleman/kraken/internal/panel/rbac"

// MayAccessServer is the object-level rule for servers: a role with
// PermServerAny (Owner/Admin, through the "*" and "server.*" wildcards) reaches
// every server, and everyone else reaches only the servers they own. A server
// with no owner (created before ownership existed) is reachable only through
// PermServerAny.
//
// It is a pure function of the user, the role and the server's owner, so the
// API's request middleware and the push-alert dispatcher — which has no request
// to read a user from — apply one rule and can never disagree about who may see
// a server.
func MayAccessServer(u *User, role *rbac.Role, ownerID string) bool {
	if role == nil {
		return false
	}
	if role.Has(rbac.PermServerAny) {
		return true
	}
	return u != nil && ownerID != "" && ownerID == u.ID
}
