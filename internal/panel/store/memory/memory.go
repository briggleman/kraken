// Package memory is an in-memory implementation of store.Store for local
// development and tests. It is concurrency-safe but non-persistent: all data is
// lost on restart. Production uses a Postgres-backed store instead.
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// Store is a thread-safe in-memory store.
type Store struct {
	mu        sync.RWMutex
	users     map[string]*store.User
	roles     map[string]*rbac.Role
	specs     map[string]*spec.Spec
	nodes     map[string]*cluster.Node
	servers   map[string]*store.Server
	schedules map[string]*store.ScheduledTask
	auditLog  []*store.AuditEntry
	sessions  map[string]*store.Session

	// Self-generated Agent-enrollment CA (dev: regenerated each restart).
	caCert []byte
	caKey  []byte

	// Panel-global settings (single value).
	settings *store.Settings

	// Per-node config keyed by node ID.
	nodeConfig map[string]*store.NodeConfig
}

// maxAuditEntries caps the in-memory audit log (dev store only).
const maxAuditEntries = 2000

// New returns an empty in-memory store.
func New() *Store {
	return &Store{
		users:      make(map[string]*store.User),
		roles:      make(map[string]*rbac.Role),
		specs:      make(map[string]*spec.Spec),
		nodes:      make(map[string]*cluster.Node),
		servers:    make(map[string]*store.Server),
		schedules:  make(map[string]*store.ScheduledTask),
		sessions:   make(map[string]*store.Session),
		nodeConfig: make(map[string]*store.NodeConfig),
	}
}

var _ store.Store = (*Store)(nil)

// ---- Users ----

func (s *Store) CreateUser(_ context.Context, u *store.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.users {
		if existing.Username == u.Username {
			return store.ErrConflict
		}
	}
	if _, ok := s.users[u.ID]; ok {
		return store.ErrConflict
	}
	s.users[u.ID] = cloneUser(u)
	return nil
}

func (s *Store) GetUser(_ context.Context, id string) (*store.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return cloneUser(u), nil
}

func (s *Store) GetUserByUsername(_ context.Context, username string) (*store.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u.Username == username {
			return cloneUser(u), nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) ListUsers(_ context.Context) ([]*store.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*store.User, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, cloneUser(u))
	}
	return out, nil
}

func (s *Store) UpdateUser(_ context.Context, u *store.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[u.ID]; !ok {
		return store.ErrNotFound
	}
	s.users[u.ID] = cloneUser(u)
	return nil
}

func (s *Store) DeleteUser(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.users, id)
	return nil
}

func (s *Store) CountUsers(_ context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users), nil
}

// ---- Roles ----

func (s *Store) GetRole(_ context.Context, id string) (*rbac.Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.roles[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return cloneRole(r), nil
}

func (s *Store) ListRoles(_ context.Context) ([]*rbac.Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*rbac.Role, 0, len(s.roles))
	for _, r := range s.roles {
		out = append(out, cloneRole(r))
	}
	return out, nil
}

func (s *Store) UpsertRole(_ context.Context, r *rbac.Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles[r.ID] = cloneRole(r)
	return nil
}

// ---- Specs ----

func (s *Store) CreateSpec(_ context.Context, sp *spec.Spec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.specs {
		if existing.Slug == sp.Slug {
			return store.ErrConflict
		}
	}
	if _, ok := s.specs[sp.ID]; ok {
		return store.ErrConflict
	}
	s.specs[sp.ID] = cloneSpec(sp)
	return nil
}

func (s *Store) GetSpec(_ context.Context, id string) (*spec.Spec, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sp, ok := s.specs[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return cloneSpec(sp), nil
}

func (s *Store) GetSpecBySlug(_ context.Context, slug string) (*spec.Spec, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sp := range s.specs {
		if sp.Slug == slug {
			return cloneSpec(sp), nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) ListSpecs(_ context.Context) ([]*spec.Spec, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*spec.Spec, 0, len(s.specs))
	for _, sp := range s.specs {
		out = append(out, cloneSpec(sp))
	}
	return out, nil
}

func (s *Store) UpdateSpec(_ context.Context, sp *spec.Spec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.specs[sp.ID]; !ok {
		return store.ErrNotFound
	}
	s.specs[sp.ID] = cloneSpec(sp)
	return nil
}

func (s *Store) DeleteSpec(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.specs[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.specs, id)
	return nil
}

// ---- Nodes ----

func (s *Store) CreateNode(_ context.Context, n *cluster.Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[n.ID]; ok {
		return store.ErrConflict
	}
	s.nodes[n.ID] = cloneNode(n)
	return nil
}

func (s *Store) GetNode(_ context.Context, id string) (*cluster.Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.nodes[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return cloneNode(n), nil
}

func (s *Store) ListNodes(_ context.Context) ([]*cluster.Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*cluster.Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		out = append(out, cloneNode(n))
	}
	return out, nil
}

func (s *Store) UpdateNode(_ context.Context, n *cluster.Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[n.ID]; !ok {
		return store.ErrNotFound
	}
	s.nodes[n.ID] = cloneNode(n)
	return nil
}

func (s *Store) DeleteNode(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.nodes, id)
	delete(s.nodeConfig, id)
	return nil
}

// ---- Servers ----

func (s *Store) CreateServer(_ context.Context, sv *store.Server) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[sv.ID]; ok {
		return store.ErrConflict
	}
	s.servers[sv.ID] = cloneServer(sv)
	return nil
}

func (s *Store) GetServer(_ context.Context, id string) (*store.Server, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sv, ok := s.servers[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return cloneServer(sv), nil
}

func (s *Store) ListServers(_ context.Context) ([]*store.Server, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*store.Server, 0, len(s.servers))
	for _, sv := range s.servers {
		out = append(out, cloneServer(sv))
	}
	return out, nil
}

func (s *Store) UpdateServer(_ context.Context, sv *store.Server) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[sv.ID]; !ok {
		return store.ErrNotFound
	}
	s.servers[sv.ID] = cloneServer(sv)
	return nil
}

func (s *Store) DeleteServer(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.servers, id)
	return nil
}

// ---- Schedules ----

func (s *Store) CreateSchedule(_ context.Context, t *store.ScheduledTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.schedules[t.ID]; ok {
		return store.ErrConflict
	}
	s.schedules[t.ID] = cloneSchedule(t)
	return nil
}

func (s *Store) GetSchedule(_ context.Context, id string) (*store.ScheduledTask, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.schedules[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return cloneSchedule(t), nil
}

func (s *Store) ListSchedules(_ context.Context) ([]*store.ScheduledTask, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*store.ScheduledTask, 0, len(s.schedules))
	for _, t := range s.schedules {
		out = append(out, cloneSchedule(t))
	}
	return out, nil
}

func (s *Store) ListSchedulesByServer(_ context.Context, serverID string) ([]*store.ScheduledTask, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*store.ScheduledTask, 0)
	for _, t := range s.schedules {
		if t.ServerID == serverID {
			out = append(out, cloneSchedule(t))
		}
	}
	return out, nil
}

func (s *Store) UpdateSchedule(_ context.Context, t *store.ScheduledTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.schedules[t.ID]; !ok {
		return store.ErrNotFound
	}
	s.schedules[t.ID] = cloneSchedule(t)
	return nil
}

func (s *Store) DeleteSchedule(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.schedules[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.schedules, id)
	return nil
}

// ---- Audit ----

func (s *Store) AppendAudit(_ context.Context, e *store.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *e
	s.auditLog = append(s.auditLog, &c)
	if len(s.auditLog) > maxAuditEntries {
		s.auditLog = s.auditLog[len(s.auditLog)-maxAuditEntries:]
	}
	return nil
}

func (s *Store) ListAudit(_ context.Context, limit int) ([]*store.AuditEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.auditLog) {
		limit = len(s.auditLog)
	}
	// Newest first.
	out := make([]*store.AuditEntry, 0, limit)
	for i := len(s.auditLog) - 1; i >= 0 && len(out) < limit; i-- {
		c := *s.auditLog[i]
		out = append(out, &c)
	}
	return out, nil
}

// ---- Sessions ----

func (s *Store) CreateSession(_ context.Context, sess *store.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := store.HashToken(sess.Token)
	s.sessions[h] = &store.Session{Token: h, UserID: sess.UserID, ExpiresAt: sess.ExpiresAt}
	return nil
}

func (s *Store) GetSession(_ context.Context, token string) (*store.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[store.HashToken(token)]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &store.Session{Token: sess.Token, UserID: sess.UserID, ExpiresAt: sess.ExpiresAt}, nil
}

func (s *Store) DeleteSession(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, store.HashToken(token))
	return nil
}

// ---- Cluster CA ----

func (s *Store) GetCA(_ context.Context) (cert, key []byte, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.caCert == nil || s.caKey == nil {
		return nil, nil, store.ErrNotFound
	}
	return append([]byte(nil), s.caCert...), append([]byte(nil), s.caKey...), nil
}

func (s *Store) SaveCA(_ context.Context, cert, key []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.caCert = append([]byte(nil), cert...)
	s.caKey = append([]byte(nil), key...)
	return nil
}

// ---- Panel settings ----

func (s *Store) GetSettings(_ context.Context) (*store.Settings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.settings == nil {
		return nil, store.ErrNotFound
	}
	c := *s.settings
	return &c, nil
}

func (s *Store) SaveSettings(_ context.Context, st *store.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *st
	s.settings = &c
	return nil
}

func (s *Store) GetNodeConfig(_ context.Context, nodeID string) (*store.NodeConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.nodeConfig[nodeID]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (s *Store) SaveNodeConfig(_ context.Context, nodeID string, c *store.NodeConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *c
	s.nodeConfig[nodeID] = &cp
	return nil
}

func (s *Store) DeleteNodeConfig(_ context.Context, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.nodeConfig, nodeID)
	return nil
}

// PurgeExpiredSessions removes sessions that expired before now. Returns the
// number removed. Intended to be called periodically by a background sweeper.
func (s *Store) PurgeExpiredSessions(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for tok, sess := range s.sessions {
		if sess.Expired(now) {
			delete(s.sessions, tok)
			n++
		}
	}
	return n
}

// ---- defensive copies (callers must not mutate stored objects) ----

func cloneUser(u *store.User) *store.User {
	c := *u
	return &c
}

func cloneRole(r *rbac.Role) *rbac.Role {
	c := *r
	c.Permissions = append([]rbac.Permission(nil), r.Permissions...)
	return &c
}

func cloneServer(sv *store.Server) *store.Server {
	c := *sv
	if sv.Vars != nil {
		c.Vars = make(map[string]string, len(sv.Vars))
		for k, v := range sv.Vars {
			c.Vars[k] = v
		}
	}
	if sv.Settings != nil {
		c.Settings = make(map[string]string, len(sv.Settings))
		for k, v := range sv.Settings {
			c.Settings[k] = v
		}
	}
	if sv.Ports != nil {
		c.Ports = make(map[string]int, len(sv.Ports))
		for k, v := range sv.Ports {
			c.Ports[k] = v
		}
	}
	if sv.DNS != nil {
		dns := *sv.DNS
		dns.RecordIDs = append([]string(nil), sv.DNS.RecordIDs...)
		c.DNS = &dns
	}
	if sv.Forwards != nil {
		c.Forwards = make(map[string]store.PortForward, len(sv.Forwards))
		for k, v := range sv.Forwards {
			c.Forwards[k] = v
		}
	}
	return &c
}

func cloneSchedule(t *store.ScheduledTask) *store.ScheduledTask {
	c := *t
	if t.LastRunAt != nil {
		v := *t.LastRunAt
		c.LastRunAt = &v
	}
	if t.NextRunAt != nil {
		v := *t.NextRunAt
		c.NextRunAt = &v
	}
	return &c
}

func cloneNode(n *cluster.Node) *cluster.Node {
	c := *n
	if n.Ports != nil {
		c.Ports = n.Ports.Clone()
	}
	return &c
}

func cloneSpec(sp *spec.Spec) *spec.Spec {
	c := *sp
	c.Platforms = append([]spec.Platform(nil), sp.Platforms...)
	c.Variables = append([]spec.Variable(nil), sp.Variables...)
	c.Ports = append([]spec.Port(nil), sp.Ports...)
	c.ConfigFiles = append([]spec.ConfigFile(nil), sp.ConfigFiles...)
	if sp.SteamAppIDs != nil {
		c.SteamAppIDs = make(map[string]int, len(sp.SteamAppIDs))
		for k, v := range sp.SteamAppIDs {
			c.SteamAppIDs[k] = v
		}
	}
	return &c
}
