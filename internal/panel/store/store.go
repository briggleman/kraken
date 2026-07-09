// Package store defines the Panel's persistence contracts: domain entities,
// sentinel errors, and repository interfaces. Implementations live in
// subpackages (e.g. store/memory for development, a pgx-backed store for prod).
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// HashToken derives the at-rest lookup key for a session token. Sessions store
// only this SHA-256 digest, never the bearer token itself, so a database dump
// cannot be replayed as live sessions.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Sentinel errors returned by store implementations.
var (
	// ErrNotFound is returned when a requested entity does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict is returned on a uniqueness violation (e.g. duplicate username/slug).
	ErrConflict = errors.New("store: conflict")
)

// User is a built-in account.
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"` // never serialized to clients
	RoleID       string `json:"role_id"`
	Disabled     bool   `json:"disabled"`
	// MustChangePassword forces a password rotation before the account can do
	// anything else. Set on the bootstrap admin so admin/admin can't persist.
	MustChangePassword bool      `json:"must_change_password"`
	CreatedAt          time.Time `json:"created_at"`
}

// Settings holds Panel-global configuration editable at runtime via the admin
// Settings page. Persisted as a single JSONB row; add fields freely (no migration).
type Settings struct {
	// CloudflareAPIToken is a scoped Cloudflare API token (DNS edit) used to push
	// per-server DNS records. Empty means the Cloudflare integration is unconfigured.
	CloudflareAPIToken string `json:"cloudflare_api_token,omitempty"`

	// UniFi gateway integration for opening per-server port forwards. UnifiAPIKey
	// empty (or URL empty) means the UniFi integration is unconfigured.
	UnifiURL    string `json:"unifi_url,omitempty"`     // controller base URL, e.g. https://192.168.1.1
	UnifiAPIKey string `json:"unifi_api_key,omitempty"` // UniFi OS API key (X-API-KEY)
	UnifiSite   string `json:"unifi_site,omitempty"`    // controller site, default "default"
	// UnifiVerifyTLS turns on standard TLS certificate verification when talking
	// to the controller. Default false because UniFi gateways ship self-signed;
	// operators who install a trusted cert should enable this to defeat MITM.
	UnifiVerifyTLS bool `json:"unifi_verify_tls,omitempty"`

	// Global runtime settings. The equivalent env var overrides these and locks
	// the field in the UI; a zero value means "use the built-in/config default".
	SessionTTLSeconds int      `json:"session_ttl_seconds,omitempty"` // session lifetime; 0 → config default
	AllowedOrigins    []string `json:"allowed_origins,omitempty"`     // WebSocket Origin allow-list; empty → dev defaults
	// BootstrapDisabled stops Seed from re-creating the bootstrap admin when the
	// instance has no users (a post-setup lockdown). Ignored when the bootstrap
	// env vars are set.
	BootstrapDisabled bool `json:"bootstrap_disabled,omitempty"`

	// SetupDismissed latches once first-run onboarding finishes (the operator
	// clicked Finish, or setup computed complete once). It keeps the Setup
	// shortcut out of the nav permanently — without it the computed state
	// regresses (and the wizard resurfaces) whenever a node dips offline.
	SetupDismissed bool `json:"setup_dismissed,omitempty"`
}

// NodeConfig is the Panel-managed per-node configuration. It selects where a
// node stores backups (delivered to the Agent via ApplyNodeConfig) and holds the
// Steam credentials the Panel injects into authenticated installs (these are
// Panel-only — never pushed to the Agent). Persisted as one JSONB row per node;
// the SFTP and Steam credential fields are encrypted at rest.
type NodeConfig struct {
	BackupTarget string `json:"backup_target,omitempty"` // "local" | "sftp" (empty → local)
	BackupDir    string `json:"backup_dir,omitempty"`    // node-local archive dir (local target)

	SftpHost       string `json:"sftp_host,omitempty"` // "host:port" (default port 22)
	SftpUser       string `json:"sftp_user,omitempty"`
	SftpPassword   string `json:"sftp_password,omitempty"`    // encrypted at rest
	SftpPrivateKey string `json:"sftp_private_key,omitempty"` // encrypted at rest (PEM)
	SftpBasePath   string `json:"sftp_base_path,omitempty"`
	// SftpKnownHostKey pins the SSH host key of the SFTP remote (authorized_keys
	// format: "<algo> <base64-key> [comment]"). Empty → the Agent accepts any key
	// on connect and logs a MITM-vulnerability warning. Pin this for real
	// verification.
	SftpKnownHostKey string `json:"sftp_known_host_key,omitempty"`

	// ReplicateToSftp mirrors every new backup (and the scheduled replicate
	// action) to the SFTP remote, regardless of the primary target.
	ReplicateToSftp bool `json:"replicate_to_sftp,omitempty"`

	// Steam account used to install games whose dedicated server is not
	// anonymous-downloadable (spec Install.RequiresSteamLogin). The Panel injects
	// these into the install container's env at deploy time; they are never sent
	// to the Agent's node config. SteamPassword is encrypted at rest.
	SteamUsername string `json:"steam_username,omitempty"`
	SteamPassword string `json:"steam_password,omitempty"` // encrypted at rest
}

// ServerDNS records a server's Cloudflare DNS assignment so the Panel can update
// or remove the exact records it created.
type ServerDNS struct {
	Name      string   `json:"name"`                // FQDN, e.g. play.example.com
	ZoneID    string   `json:"zone_id"`             // Cloudflare zone the records live in
	Service   string   `json:"service,omitempty"`   // SRV service label (e.g. "minecraft"); empty = no SRV
	PortName  string   `json:"port_name,omitempty"` // spec port advertised by the SRV record
	RecordIDs []string `json:"record_ids"`          // Cloudflare record IDs to update/delete
}

// PortForward records a UniFi port-forward rule the Panel created for a server
// port, so it can be toggled or removed later.
type PortForward struct {
	RuleID  string `json:"rule_id"`
	Enabled bool   `json:"enabled"`
}

// ServerSFTP holds a server's per-server SFTP credentials. The Panel generates
// and stores these and pushes them to the Agent (in the runtime spec) so the
// Agent's SFTP server can authenticate a login and jail it to the data dir.
// PasswordHash is a bcrypt hash (one-way); Keys are public. It is stripped from
// general server API responses and surfaced only via the dedicated SFTP endpoint.
type ServerSFTP struct {
	Enabled      bool     `json:"enabled,omitempty"`
	PasswordHash string   `json:"password_hash,omitempty"` // bcrypt; empty → password auth off
	Keys         []string `json:"keys,omitempty"`          // authorized OpenSSH public keys
}

// ServerState is the Panel's view of a game server's lifecycle state.
type ServerState string

const (
	StateInstalling    ServerState = "installing"
	StateInstallFailed ServerState = "install_failed"
	StateOffline       ServerState = "offline"
	StateStarting      ServerState = "starting"
	StateRunning       ServerState = "running"
	StateStopping      ServerState = "stopping"
	StateCrashed       ServerState = "crashed"
)

// Server is a provisioned game server: a spec deployed onto a node with resolved
// variables and allocated ports.
type Server struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// OwnerID is the user who created the server. Access to a server is scoped to
	// its owner unless the caller's role holds PermServerAny (Owner/Admin). Empty
	// on servers created before ownership existed → treated as PermServerAny-only.
	OwnerID  string                 `json:"owner_id,omitempty"`
	SpecID   string                 `json:"spec_id"`
	NodeID   string                 `json:"node_id"`
	Kind     spec.PlatformKind      `json:"kind"`
	State    ServerState            `json:"state"`
	Vars     map[string]string      `json:"vars"`
	Settings map[string]string      `json:"settings"` // game settings values (key → value)
	Ports    map[string]int         `json:"ports"`    // spec port name → allocated host port
	MemoryMB int                    `json:"memory_mb"`
	DNS      *ServerDNS             `json:"dns,omitempty"`      // Cloudflare DNS assignment (nil = none)
	Forwards map[string]PortForward `json:"forwards,omitempty"` // spec port name → UniFi forward rule
	// BepInEx records that this server was deployed with BepInEx mod support, so
	// every install/start uses the spec's modded install append + loader command.
	BepInEx bool `json:"bepinex,omitempty"`
	// Players / MaxPlayers / PlayersKnown are the last-known online-player count,
	// refreshed by the reconciler from the Agent so the fleet list can show it
	// without an open stats stream. PlayersKnown separates "0 online" from "unknown".
	Players      int32       `json:"players,omitempty"`
	MaxPlayers   int32       `json:"max_players,omitempty"`
	PlayersKnown bool        `json:"players_known,omitempty"`
	SFTP         *ServerSFTP `json:"sftp,omitempty"` // per-server SFTP creds (stripped from API responses)
	CreatedAt    time.Time   `json:"created_at"`
}

// ScheduleAction is the operation a scheduled task performs on its server.
type ScheduleAction string

const (
	// ScheduleRestart restarts the server.
	ScheduleRestart ScheduleAction = "restart"
	// ScheduleBackup creates a backup of the server's data volume.
	ScheduleBackup ScheduleAction = "backup"
	// ScheduleCommand sends a console command to the running server.
	ScheduleCommand ScheduleAction = "command"
	// ScheduleReplicate mirrors the server's existing backups to the node's
	// configured SFTP remote.
	ScheduleReplicate ScheduleAction = "replicate"
)

// Valid reports whether a is a recognized schedule action.
func (a ScheduleAction) Valid() bool {
	switch a {
	case ScheduleRestart, ScheduleBackup, ScheduleCommand, ScheduleReplicate:
		return true
	default:
		return false
	}
}

// ScheduledTask is a cron-scheduled action against a server (restart, backup, or
// console command), run by the Panel's background scheduler.
type ScheduledTask struct {
	ID        string         `json:"id"`
	ServerID  string         `json:"server_id"`
	Name      string         `json:"name"`
	Action    ScheduleAction `json:"action"`
	Cron      string         `json:"cron"`              // 5-field cron expression
	Command   string         `json:"command,omitempty"` // for action=command
	Enabled   bool           `json:"enabled"`
	LastRunAt *time.Time     `json:"last_run_at,omitempty"`
	NextRunAt *time.Time     `json:"next_run_at,omitempty"`
	LastError string         `json:"last_error,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// AuditEntry records a single security-relevant action (who did what, to what,
// and the result). Entries are append-only and surfaced in the admin Audit log.
type AuditEntry struct {
	ID         string    `json:"id"`
	Time       time.Time `json:"time"`
	ActorID    string    `json:"actor_id,omitempty"`
	Actor      string    `json:"actor"` // username, or "anonymous" for pre-auth events
	Action     string    `json:"action"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	TargetType string    `json:"target_type,omitempty"`
	TargetID   string    `json:"target_id,omitempty"`
	Status     int       `json:"status"`
	IP         string    `json:"ip,omitempty"`
}

// Session is an authenticated session mapping an opaque token to a user.
type Session struct {
	Token     string
	UserID    string
	ExpiresAt time.Time
}

// Expired reports whether the session is past its expiry at time now.
func (s *Session) Expired(now time.Time) bool { return now.After(s.ExpiresAt) }

// UserStore persists users.
type UserStore interface {
	CreateUser(ctx context.Context, u *User) error
	GetUser(ctx context.Context, id string) (*User, error)
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	ListUsers(ctx context.Context) ([]*User, error)
	UpdateUser(ctx context.Context, u *User) error
	DeleteUser(ctx context.Context, id string) error
	CountUsers(ctx context.Context) (int, error)
}

// RoleStore persists roles. Built-in roles are seeded at startup.
type RoleStore interface {
	GetRole(ctx context.Context, id string) (*rbac.Role, error)
	ListRoles(ctx context.Context) ([]*rbac.Role, error)
	UpsertRole(ctx context.Context, r *rbac.Role) error
}

// SpecStore persists the game spec catalog.
type SpecStore interface {
	CreateSpec(ctx context.Context, s *spec.Spec) error
	GetSpec(ctx context.Context, id string) (*spec.Spec, error)
	GetSpecBySlug(ctx context.Context, slug string) (*spec.Spec, error)
	ListSpecs(ctx context.Context) ([]*spec.Spec, error)
	UpdateSpec(ctx context.Context, s *spec.Spec) error
	DeleteSpec(ctx context.Context, id string) error
}

// NodeStore persists the registry of node Agents.
type NodeStore interface {
	CreateNode(ctx context.Context, n *cluster.Node) error
	GetNode(ctx context.Context, id string) (*cluster.Node, error)
	ListNodes(ctx context.Context) ([]*cluster.Node, error)
	UpdateNode(ctx context.Context, n *cluster.Node) error
	DeleteNode(ctx context.Context, id string) error
}

// ServerStore persists provisioned servers.
type ServerStore interface {
	CreateServer(ctx context.Context, s *Server) error
	GetServer(ctx context.Context, id string) (*Server, error)
	ListServers(ctx context.Context) ([]*Server, error)
	UpdateServer(ctx context.Context, s *Server) error
	DeleteServer(ctx context.Context, id string) error
}

// ScheduleStore persists cron-scheduled tasks.
type ScheduleStore interface {
	CreateSchedule(ctx context.Context, t *ScheduledTask) error
	GetSchedule(ctx context.Context, id string) (*ScheduledTask, error)
	ListSchedules(ctx context.Context) ([]*ScheduledTask, error)
	ListSchedulesByServer(ctx context.Context, serverID string) ([]*ScheduledTask, error)
	UpdateSchedule(ctx context.Context, t *ScheduledTask) error
	DeleteSchedule(ctx context.Context, id string) error
}

// AuditStore persists the append-only audit log.
type AuditStore interface {
	AppendAudit(ctx context.Context, e *AuditEntry) error
	ListAudit(ctx context.Context, limit int) ([]*AuditEntry, error)
}

// SessionStore persists authentication sessions.
type SessionStore interface {
	CreateSession(ctx context.Context, s *Session) error
	GetSession(ctx context.Context, token string) (*Session, error)
	DeleteSession(ctx context.Context, token string) error
}

// CAStore persists the Panel's self-generated Agent-enrollment CA (a single
// keypair). Returns ErrNotFound when no CA has been generated yet.
type CAStore interface {
	GetCA(ctx context.Context) (cert, key []byte, err error)
	SaveCA(ctx context.Context, cert, key []byte) error
}

// SettingsStore persists Panel-global settings (a single row). GetSettings
// returns ErrNotFound when nothing has been saved yet.
type SettingsStore interface {
	GetSettings(ctx context.Context) (*Settings, error)
	SaveSettings(ctx context.Context, s *Settings) error
}

// NodeSettingsStore persists per-node configuration (one JSONB row per node).
// GetNodeConfig returns ErrNotFound when a node has no saved config.
type NodeSettingsStore interface {
	GetNodeConfig(ctx context.Context, nodeID string) (*NodeConfig, error)
	SaveNodeConfig(ctx context.Context, nodeID string, c *NodeConfig) error
	DeleteNodeConfig(ctx context.Context, nodeID string) error
}

// Store aggregates every repository the Panel needs.
type Store interface {
	UserStore
	RoleStore
	SpecStore
	NodeStore
	ServerStore
	ScheduleStore
	AuditStore
	SessionStore
	CAStore
	SettingsStore
	NodeSettingsStore
}
