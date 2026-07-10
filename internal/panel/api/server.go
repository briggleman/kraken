// Package api implements the Panel's HTTP API: routing, middleware, and handlers
// for auth, the game spec catalog, and user administration. It is transport over
// the store + rbac packages and holds no business state of its own.
package api

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/nodeclient"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/webui"
	"github.com/briggleman/kraken/internal/shared/mtls"
)

// Server holds the API dependencies and exposes an http.Handler.
type Server struct {
	cfg    *config.Config
	store  store.Store
	nodes  *nodeclient.Pool
	logger *slog.Logger
	router chi.Router

	// CA signing material for Agent enrollment (nil when not configured).
	caCert    []byte
	caKey     []byte
	bootstrap *bootstrapRegistry

	// In-memory Panel client TLS bundle from WithClientTLSBytes. When set,
	// the Agent pool uses these bytes rather than reading cfg.TLS{Cert,Key,CA}
	// off disk — sidesteps volume-permission gymnastics for the auto-issued
	// cert (see cmd/panel/main.go and buildNodePool below).
	clientTLSCert []byte
	clientTLSKey  []byte
	clientTLSCA   []byte

	// Per-node throttle for agent cert rotation attempts (see rotate.go).
	rotateMu   sync.Mutex
	lastRotate map[string]time.Time

	// setupNets is the parsed internal-network allowlist guarding /setup/*
	// (see internal_gate.go).
	setupNets []*net.IPNet

	// onRestart, when set, asks the host process to exit cleanly so a supervisor
	// restarts it (used after a UI-driven datastore change). Nil = no-op.
	onRestart func()
}

// WithRestart wires a callback the API can use to request a process restart.
func WithRestart(fn func()) Option {
	return func(s *Server) { s.onRestart = fn }
}

// Option customizes a Server at construction time.
type Option func(*Server)

// WithCA pre-loads the Agent-enrollment CA signing material (e.g. a self-generated
// CA resolved at startup). When set, it takes precedence over file-based loading.
func WithCA(cert, key []byte) Option {
	return func(s *Server) {
		if cert != nil && key != nil {
			s.caCert, s.caKey = cert, key
		}
	}
}

// WithClientTLSBytes provides Panel client TLS material as raw PEM bytes,
// used when the Panel auto-issues its own client cert at startup against
// the Agent-enrollment CA (see cmd/panel/main.go). Takes precedence over
// the KRAKEN_TLS_CERT/KEY/CA file-based path.
func WithClientTLSBytes(cert, key, ca []byte) Option {
	return func(s *Server) {
		if len(cert) > 0 && len(key) > 0 && len(ca) > 0 {
			s.clientTLSCert, s.clientTLSKey, s.clientTLSCA = cert, key, ca
		}
	}
}

// New constructs a Server and wires its routes. The Agent connection pool is
// secure by default: when TLS material is configured (either via
// KRAKEN_TLS_* files or via WithClientTLSBytes) it dials Agents over mutual
// TLS; only an explicitly cert-less (dev) config falls back to insecure,
// with a warning.
func New(cfg *config.Config, st store.Store, logger *slog.Logger, opts ...Option) *Server {
	s := &Server{cfg: cfg, store: st, logger: logger, bootstrap: newBootstrapRegistry(), lastRotate: map[string]time.Time{}}
	for _, o := range opts {
		o(s)
	}
	// Load CA signing material from files for Agent enrollment, if configured and
	// not already supplied via WithCA (e.g. a self-generated CA from the store).
	if s.caCert == nil && cfg.CASigning() {
		cert, cerr := os.ReadFile(cfg.CACert)
		key, kerr := os.ReadFile(cfg.CAKey)
		if cerr != nil || kerr != nil {
			logger.Error("CA signing configured but unreadable — Agent enrollment disabled", "cert_err", cerr, "key_err", kerr)
		} else {
			s.caCert, s.caKey = cert, key
			logger.Info("Agent enrollment enabled (Panel will sign Agent certs)")
		}
	}
	// Internal-network allowlist for the /setup/* surface. An unset list gets
	// the private-network defaults (config.Load does this too; this covers
	// direct constructions) so the gate never silently fails open.
	cidrs := cfg.SetupAllowedCIDRs
	if len(cidrs) == 0 {
		cidrs = config.DefaultSetupAllowedCIDRs()
	}
	s.setupNets = s.parseSetupCIDRs(cidrs)
	// Build the Agent pool now that all options have been applied. In-memory
	// bytes (from Panel auto-issue) beat file paths (operator override); no
	// TLS at all → plaintext with a warning.
	s.nodes = s.buildNodePool()
	s.router = s.routes()
	return s
}

// buildNodePool returns an mTLS Agent pool built from whichever TLS source
// was configured — WithClientTLSBytes for auto-issued in-memory certs,
// KRAKEN_TLS_* paths for operator-provided files, or an insecure pool with a
// warning when nothing is set.
func (s *Server) buildNodePool() *nodeclient.Pool {
	if len(s.clientTLSCert) > 0 {
		tlsCfg, err := mtls.ClientTLSFromBytes(s.clientTLSCert, s.clientTLSKey, s.clientTLSCA, mtls.AgentServerName)
		if err != nil {
			s.logger.Error("in-memory mTLS config failed — falling back to insecure Agent pool", "err", err)
			return nodeclient.NewInsecurePool(nodeclient.WithLogger(s.logger))
		}
		s.logger.Info("Panel→Agent gRPC secured with mutual TLS (auto-issued client cert)",
			"client_cert", mtls.SummarizePEM(s.clientTLSCert),
			"trusted_ca", mtls.SummarizePEM(s.clientTLSCA),
			"pinned_server_name", mtls.AgentServerName)
		return nodeclient.NewTLSPool(tlsCfg, nodeclient.WithLogger(s.logger))
	}
	if !s.cfg.MutualTLS() {
		s.logger.Warn("Panel→Agent gRPC is INSECURE (no mTLS) — set KRAKEN_TLS_CERT/KEY/CA to enable")
		return nodeclient.NewInsecurePool(nodeclient.WithLogger(s.logger))
	}
	tlsCfg, err := mtls.ClientTLS(s.cfg.TLSCert, s.cfg.TLSKey, s.cfg.TLSCA, mtls.AgentServerName)
	if err != nil {
		s.logger.Error("mTLS config failed — falling back to insecure Agent pool", "err", err)
		return nodeclient.NewInsecurePool(nodeclient.WithLogger(s.logger))
	}
	s.logger.Info("Panel→Agent gRPC secured with mutual TLS (operator-provided files)",
		"client_cert", summarizeCertFile(s.cfg.TLSCert),
		"trusted_ca", summarizeCertFile(s.cfg.TLSCA),
		"pinned_server_name", mtls.AgentServerName)
	return nodeclient.NewTLSPool(tlsCfg, nodeclient.WithLogger(s.logger))
}

// summarizeCertFile is a best-effort SummarizePEM over a cert file path.
func summarizeCertFile(path string) string {
	pem, err := os.ReadFile(path)
	if err != nil {
		return "unreadable: " + err.Error()
	}
	return mtls.SummarizePEM(pem)
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.router }

// Close releases resources held by the server (Agent gRPC connections).
func (s *Server) Close() error { return s.nodes.Close() }

func (s *Server) routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// Note: middleware.RealIP is intentionally omitted — it trusts client-supplied
	// X-Forwarded-For/X-Real-IP, which are spoofable without a trusted proxy and
	// would corrupt audit-log source IPs. clientIP() uses the real TCP peer.
	r.Use(secureHeaders)
	r.Use(middleware.Recoverer)
	r.Use(s.metricsMiddleware)
	// Coarse safety net only — it must sit ABOVE the longest legitimate handler
	// deadline (backup restore sets 10m; power stop/restart up to 60s), because
	// context.WithTimeout(r.Context(), …) is capped by this parent. A tighter value
	// here silently clamps those per-operation deadlines (a 30s cap previously broke
	// graceful restart and large restores). Long-lived WS streams hijack the
	// connection and are unaffected.
	r.Use(middleware.Timeout(15 * time.Minute))

	// Liveness/readiness + Prometheus metrics — unauthenticated.
	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleHealth)
	r.Get("/metrics", s.handleMetrics)

	r.Route("/api/v1", func(r chi.Router) {
		// Auth: login is public; logout/me require a valid session.
		r.Post("/auth/login", s.handleLogin)

		// Agent enrollment: authenticated by a one-time bootstrap token (the Agent
		// has no session/cert yet), so this is intentionally outside requireAuth.
		r.Post("/agents/enroll", s.handleEnroll)

		// Local single-host enrollment: issues a bootstrap token for the co-located
		// Agent. No session (the Agent has none); gated on a loopback source IP
		// plus the internal-network allowlist like the rest of /setup/*.
		r.With(s.requireInternal).Post("/setup/local-enroll", s.handleLocalEnroll)

		// Live console + stats WebSocket. Authenticates from the ?token= query
		// param (the browser can't set Authorization on a WS handshake).
		r.Get("/servers/{id}/stream/ws", s.handleServerStream)
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Use(s.auditMiddleware)
			// First-run gate: a user who must rotate their password can only reach
			// the allow-listed recovery endpoints until they do.
			r.Use(s.requirePasswordCurrent)
			r.Post("/auth/logout", s.handleLogout)
			r.Get("/auth/me", s.handleMe)
			r.Post("/auth/change-password", s.handleChangePassword)

			// First-run onboarding. The whole /setup/* surface is additionally
			// gated on an internal-network source IP (requireInternal): setup can
			// reconfigure the datastore and mint enrollment material, so it must
			// never be drivable from the public internet even with a valid session.
			r.Group(func(r chi.Router) {
				r.Use(s.requireInternal)
				r.Get("/setup/status", s.handleSetupStatus)
				r.Post("/setup/dismiss", s.handleDismissSetup)
				// Datastore configuration (bring-your-own Postgres).
				r.With(s.requirePermission(rbac.PermSettingsView)).Get("/setup/database", s.handleGetDatabase)
				r.With(s.requirePermission(rbac.PermSettingsManage)).Post("/setup/database/test", s.handleTestDatabase)
				r.With(s.requirePermission(rbac.PermSettingsManage)).Post("/setup/database", s.handleConnectDatabase)
			})

			// OpenAPI document — any authenticated user; rendered by the in-app
			// API reference. Not public.
			r.Get("/openapi.yaml", s.handleOpenAPISpec)

			// Game spec catalog.
			r.With(s.requirePermission(rbac.PermSpecView)).Get("/specs", s.handleListSpecs)
			r.With(s.requirePermission(rbac.PermSpecView)).Get("/specs/{id}", s.handleGetSpec)
			r.With(s.requirePermission(rbac.PermSpecManage)).Post("/specs", s.handleCreateSpec)
			r.With(s.requirePermission(rbac.PermSpecManage)).Put("/specs/{id}", s.handleUpdateSpec)
			r.With(s.requirePermission(rbac.PermSpecManage)).Delete("/specs/{id}", s.handleDeleteSpec)

			// Built-in starter game catalog (one-click import during onboarding).
			r.With(s.requirePermission(rbac.PermSpecView)).Get("/catalog", s.handleListCatalog)
			r.With(s.requirePermission(rbac.PermSpecManage)).Post("/catalog/{id}/import", s.handleImportCatalogSpec)

			// Node registry + live Agent control (Panel → Agent over gRPC).
			r.With(s.requirePermission(rbac.PermNodeView)).Get("/nodes", s.handleListNodes)
			r.With(s.requirePermission(rbac.PermNodeManage)).Post("/nodes", s.handleRegisterNode)
			r.With(s.requirePermission(rbac.PermNodeView)).Get("/nodes/{id}", s.handleGetNode)
			r.With(s.requirePermission(rbac.PermNodeView)).Get("/nodes/{id}/info", s.handleNodeInfo)
			r.With(s.requirePermission(rbac.PermNodeManage)).Patch("/nodes/{id}", s.handleUpdateNode)
			r.With(s.requirePermission(rbac.PermNodeManage)).Delete("/nodes/{id}", s.handleDeleteNode)
			// Per-node System settings (backup target + replication). Uses the
			// Panel-global settings perms since it manages node-wide credentials.
			r.With(s.requirePermission(rbac.PermSettingsView)).Get("/nodes/{id}/config", s.handleGetNodeConfig)
			r.With(s.requirePermission(rbac.PermSettingsManage)).Put("/nodes/{id}/config", s.handleUpdateNodeConfig)
			r.With(s.requirePermission(rbac.PermServerPower)).
				Post("/nodes/{id}/servers/{serverID}/power", s.handleServerPower)

			// Server lifecycle (schedule → install → run via the hosting Agent).
			r.With(s.requirePermission(rbac.PermServerView)).Get("/servers", s.handleListServers)
			r.With(s.requirePermission(rbac.PermServerView)).Get("/servers/{id}", s.handleGetServer)
			r.With(s.requirePermission(rbac.PermServerCreate)).Post("/servers", s.handleCreateServer)
			r.With(s.requirePermission(rbac.PermServerPower)).Post("/servers/{id}/power", s.handleServerLifecyclePower)
			r.With(s.requirePermission(rbac.PermServerPower)).Post("/servers/{id}/reinstall", s.handleReinstallServer)
			r.With(s.requirePermission(rbac.PermServerDelete)).Delete("/servers/{id}", s.handleDeleteServer)
			r.With(s.requirePermission(rbac.PermServerView)).Get("/servers/{id}/settings", s.handleGetServerSettings)
			r.With(s.requirePermission(rbac.PermServerConfig)).Put("/servers/{id}/settings", s.handleUpdateServerSettings)
			r.With(s.requirePermission(rbac.PermServerFilesRead)).Get("/servers/{id}/files", s.handleListFiles)
			r.With(s.requirePermission(rbac.PermServerFilesRead)).Get("/servers/{id}/files/content", s.handleReadFile)
			r.With(s.requirePermission(rbac.PermServerFilesRead)).Get("/servers/{id}/files/raw", s.handleDownloadFile)
			r.With(s.requirePermission(rbac.PermServerFilesRead)).Post("/servers/{id}/files/download", s.handleDownloadFiles)
			r.With(s.requirePermission(rbac.PermServerFilesWrite)).Post("/servers/{id}/files/mkdir", s.handleMakeDir)
			r.With(s.requirePermission(rbac.PermServerFilesWrite)).Post("/servers/{id}/files/move", s.handleMovePath)
			r.With(s.requirePermission(rbac.PermServerFilesWrite)).Post("/servers/{id}/files/copy", s.handleCopyPath)
			r.With(s.requirePermission(rbac.PermServerFilesWrite)).Post("/servers/{id}/files/write", s.handleWriteFile)
			r.With(s.requirePermission(rbac.PermServerFilesWrite)).Post("/servers/{id}/files/upload", s.handleUploadFiles)
			r.With(s.requirePermission(rbac.PermServerFilesWrite)).Post("/servers/{id}/files/delete", s.handleDeleteFiles)
			r.With(s.requirePermission(rbac.PermBackupManage)).Get("/servers/{id}/backups", s.handleListBackups)
			r.With(s.requirePermission(rbac.PermBackupManage)).Post("/servers/{id}/backups", s.handleCreateBackup)
			r.With(s.requirePermission(rbac.PermBackupManage)).Post("/servers/{id}/backups/{backupId}/restore", s.handleRestoreBackup)
			r.With(s.requirePermission(rbac.PermBackupManage)).Delete("/servers/{id}/backups/{backupId}", s.handleDeleteBackup)

			// SFTP access (power-user file access). View needs files-read; changing
			// credentials needs config. All are additionally owner-scoped.
			r.With(s.requirePermission(rbac.PermServerFilesRead)).Get("/servers/{id}/sftp", s.handleGetServerSFTP)
			r.With(s.requirePermission(rbac.PermServerConfig)).Post("/servers/{id}/sftp/password", s.handleResetServerSFTPPassword)
			r.With(s.requirePermission(rbac.PermServerConfig)).Put("/servers/{id}/sftp/keys", s.handleSetServerSFTPKeys)
			r.With(s.requirePermission(rbac.PermServerConfig)).Post("/servers/{id}/sftp/disable", s.handleDisableServerSFTP)

			// Cron-scheduled tasks (restart / backup / command).
			r.With(s.requirePermission(rbac.PermServerView)).Get("/servers/{id}/schedules", s.handleListSchedules)
			r.With(s.requirePermission(rbac.PermServerPower)).Post("/servers/{id}/schedules", s.handleCreateSchedule)
			r.With(s.requirePermission(rbac.PermServerPower)).Put("/servers/{id}/schedules/{scheduleId}", s.handleUpdateSchedule)
			r.With(s.requirePermission(rbac.PermServerPower)).Delete("/servers/{id}/schedules/{scheduleId}", s.handleDeleteSchedule)

			// User administration.
			r.With(s.requirePermission(rbac.PermUserManage)).Get("/users", s.handleListUsers)
			r.With(s.requirePermission(rbac.PermUserManage)).Post("/users", s.handleCreateUser)
			r.With(s.requirePermission(rbac.PermUserManage)).Put("/users/{id}", s.handleUpdateUser)
			r.With(s.requirePermission(rbac.PermUserManage)).Post("/users/{id}/password", s.handleResetPassword)
			r.With(s.requirePermission(rbac.PermUserManage)).Delete("/users/{id}", s.handleDeleteUser)
			r.With(s.requirePermission(rbac.PermUserManage)).Get("/roles", s.handleListRoles)
			r.With(s.requirePermission(rbac.PermUserManage)).Get("/permissions", s.handleListPermissions)

			// Panel-global settings (Cloudflare DNS integration).
			r.With(s.requirePermission(rbac.PermSettingsView)).Get("/settings", s.handleGetSettings)
			r.With(s.requirePermission(rbac.PermSettingsManage)).Put("/settings", s.handleUpdateSettings)
			r.With(s.requirePermission(rbac.PermSettingsManage)).Post("/settings/cloudflare/test", s.handleTestCloudflare)
			r.With(s.requirePermission(rbac.PermSettingsManage)).Post("/settings/unifi/test", s.handleTestUnifi)

			// Per-server networking: Cloudflare DNS + UniFi port forwards.
			r.With(s.requirePermission(rbac.PermServerView)).Get("/servers/{id}/dns", s.handleGetServerDNS)
			r.With(s.requirePermission(rbac.PermServerConfig)).Put("/servers/{id}/dns", s.handleSetServerDNS)
			r.With(s.requirePermission(rbac.PermServerConfig)).Delete("/servers/{id}/dns", s.handleDeleteServerDNS)
			r.With(s.requirePermission(rbac.PermServerConfig)).Post("/servers/{id}/forwards/{portName}", s.handleSetServerForward)

			// Audit log (admin).
			r.With(s.requirePermission(rbac.PermAuditView)).Get("/audit", s.handleListAudit)

			// Agent bootstrap token issuance (admin) for mTLS enrollment, plus
			// the token-lifecycle poll the setup wizard uses to show progress.
			r.With(s.requirePermission(rbac.PermNodeManage)).Post("/agents/bootstrap-tokens", s.handleCreateBootstrapToken)
			r.With(s.requirePermission(rbac.PermNodeManage)).Get("/agents/enroll-status", s.handleEnrollStatus)
		})
	})

	// Embedded UI: catch-all handler under NotFound so it never shadows
	// /api/v1/*, /healthz, /readyz, or /metrics (chi tries those first).
	// The handler serves the built React bundle with SPA fallback so any
	// client-router path (/servers/foo, /nodes, …) renders index.html.
	ui := webui.Handler()
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		// Anything under /api that reaches this fallback is a real 404 —
		// hand it back as JSON rather than the SPA shell.
		if strings.HasPrefix(req.URL.Path, "/api/") {
			http.NotFound(w, req)
			return
		}
		ui.ServeHTTP(w, req)
	})

	return r
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// secureHeaders sets conservative response security headers. The Panel serves
// JSON/YAML and a metrics endpoint (no HTML), so nosniff is the key one — it
// stops browsers MIME-sniffing a response into something executable.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
