package api

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store/migrate"
)

type dbConfigRequest struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	DBName   string `json:"dbname"`
	SSLMode  string `json:"sslmode"`
}

// dsn builds a Postgres DSN from the request, applying defaults and URL-encoding
// the password so the client never assembles the secret string itself.
func (r dbConfigRequest) dsn() string {
	port := r.Port
	if port == 0 {
		port = 5432
	}
	dbname := strings.TrimSpace(r.DBName)
	if dbname == "" {
		dbname = "kraken"
	}
	ssl := strings.TrimSpace(r.SSLMode)
	if ssl == "" {
		ssl = "disable"
	}
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(r.User, r.Password),
		Host:   net.JoinHostPort(strings.TrimSpace(r.Host), strconv.Itoa(port)),
		Path:   "/" + dbname,
	}
	q := url.Values{}
	q.Set("sslmode", ssl)
	u.RawQuery = q.Encode()
	return u.String()
}

type dbView struct {
	UsingMemory bool   `json:"using_memory"`
	EnvLocked   bool   `json:"env_locked"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	User        string `json:"user,omitempty"`
	DBName      string `json:"dbname,omitempty"`
	SSLMode     string `json:"sslmode,omitempty"`
}

// handleGetDatabase reports the current datastore target (never the password).
func (s *Server) handleGetDatabase(w http.ResponseWriter, _ *http.Request) {
	v := dbView{UsingMemory: s.cfg.UsesMemoryStore(), EnvLocked: s.cfg.DatabaseURLFromEnv}
	if s.cfg.DatabaseURL != "" {
		if u, err := url.Parse(s.cfg.DatabaseURL); err == nil {
			v.Host = u.Hostname()
			if p, _ := strconv.Atoi(u.Port()); p != 0 {
				v.Port = p
			}
			v.User = u.User.Username()
			v.DBName = strings.TrimPrefix(u.Path, "/")
			v.SSLMode = u.Query().Get("sslmode")
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// handleTestDatabase preflights a connection: reports whether the target DB
// exists and whether the role can create it.
func (s *Server) handleTestDatabase(w http.ResponseWriter, r *http.Request) {
	var req dbConfigRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(req.Host) == "" || strings.TrimSpace(req.User) == "" {
		writeError(w, http.StatusBadRequest, "host and user are required")
		return
	}
	canCreate, exists, err := migrate.Probe(req.dsn())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "db_exists": exists, "can_create_db": canCreate})
}

// handleConnectDatabase creates the database if needed, runs migrations, persists
// the DSN to the config file, and requests a restart so the Panel comes back on
// Postgres. No-op (409) when the DSN is env-managed.
func (s *Server) handleConnectDatabase(w http.ResponseWriter, r *http.Request) {
	if s.cfg.DatabaseURLFromEnv {
		writeError(w, http.StatusConflict, "the database is managed by KRAKEN_DATABASE_URL and can't be changed from the UI")
		return
	}
	var req dbConfigRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(req.Host) == "" || strings.TrimSpace(req.User) == "" {
		writeError(w, http.StatusBadRequest, "host and user are required")
		return
	}
	dsn := req.dsn()
	if err := migrate.EnsureDatabase(dsn); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := migrate.Up(dsn); err != nil {
		writeError(w, http.StatusBadGateway, "migrations failed: "+err.Error())
		return
	}
	if err := config.SaveDatabaseURL(s.cfg.ConfigFile, dsn); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save config: "+err.Error())
		return
	}
	s.logger.Info("database configured via UI; restarting onto Postgres")
	s.recordAudit(r, http.StatusOK, "database-connect")
	writeJSON(w, http.StatusOK, map[string]any{"restarting": true})
	// Let the response flush, then ask the host process to restart onto Postgres.
	if s.onRestart != nil {
		go func() {
			time.Sleep(400 * time.Millisecond)
			s.onRestart()
		}()
	}
}
