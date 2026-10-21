package store

import "github.com/briggleman/kraken/internal/panel/rbac"

// MayAccessServer is the object-level rule for servers: a role with
// PermServerAny (Owner/Admin, through the "*" and "server.*" wildcards) reaches
// every server; everyone else reaches the servers they own and the servers
// granted to them (ServerGrant, #146). A server with no owner (created before
// ownership existed, or whose owner was deleted) is reachable only through
// PermServerAny or a grant. Reaching a server is all this decides — what the
// user may do on it is the role's permissions, checked separately.
//
// granted reports whether the user holds a grant on the server. It is asked
// only when ownership and PermServerAny have not already settled the answer,
// so a caller backed by a database pays the lookup only for the users who need
// it; nil means no grant.
//
// It is a pure function of its inputs, so the API's request middleware, the
// console stream and the push-alert dispatcher — which has no request to read
// a user from — apply one rule and can never disagree about who may see a
// server.
func MayAccessServer(u *User, role *rbac.Role, ownerID string, granted func() bool) bool {
	if role == nil {
		return false
	}
	if role.Has(rbac.PermServerAny) {
		return true
	}
	if u == nil {
		return false
	}
	if ownerID != "" && ownerID == u.ID {
		return true
	}
	return granted != nil && granted()
}
