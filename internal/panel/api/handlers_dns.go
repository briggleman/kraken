package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/briggleman/kraken/internal/panel/cloudflare"
	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// hostnameRe is a permissive FQDN check (labels of letters/digits/hyphens, ≥2 labels).
var hostnameRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)

// panelSettings returns the stored settings, or an empty (unconfigured) value.
func (s *Server) panelSettings(ctx context.Context) *store.Settings {
	st, err := s.store.GetSettings(ctx)
	if err != nil || st == nil {
		return &store.Settings{}
	}
	return st
}

// cloudflareToken returns the stored Cloudflare API token, or "" if unconfigured.
func (s *Server) cloudflareToken(ctx context.Context) string {
	return s.panelSettings(ctx).CloudflareAPIToken
}

type settingsView struct {
	CloudflareConfigured bool   `json:"cloudflare_configured"`
	UnifiConfigured      bool   `json:"unifi_configured"`
	UnifiURL             string `json:"unifi_url,omitempty"`
	UnifiSite            string `json:"unifi_site,omitempty"`
	UnifiVerifyTLS       bool   `json:"unifi_verify_tls"`

	// Global runtime settings. Each *_locked flag is true when the equivalent env
	// var is set, in which case the effective value comes from env (not the store)
	// and the UI shows the field as read-only/ENV-MANAGED.
	SessionTTLSeconds    int      `json:"session_ttl_seconds"`
	SessionTTLLocked     bool     `json:"session_ttl_locked"`
	AllowedOrigins       []string `json:"allowed_origins"`
	AllowedOriginsLocked bool     `json:"allowed_origins_locked"`
	BootstrapDisabled    bool     `json:"bootstrap_disabled"`
	BootstrapUser        string   `json:"bootstrap_user"`
	BootstrapLocked      bool     `json:"bootstrap_locked"`
}

func (s *Server) toSettingsView(ctx context.Context, st *store.Settings) settingsView {
	return settingsView{
		CloudflareConfigured: st.CloudflareAPIToken != "",
		UnifiConfigured:      st.UnifiURL != "" && st.UnifiAPIKey != "",
		UnifiURL:             st.UnifiURL,
		UnifiSite:            st.UnifiSite,
		UnifiVerifyTLS:       st.UnifiVerifyTLS,

		SessionTTLSeconds:    int(s.sessionTTL(ctx) / time.Second),
		SessionTTLLocked:     s.cfg.SessionTTLFromEnv,
		AllowedOrigins:       s.allowedOrigins(ctx),
		AllowedOriginsLocked: s.cfg.AllowedOriginsFromEnv,
		BootstrapDisabled:    st.BootstrapDisabled,
		BootstrapUser:        s.cfg.BootstrapAdminUser,
		BootstrapLocked:      s.cfg.BootstrapFromEnv,
	}
}

// sessionTTL resolves the session lifetime: KRAKEN_SESSION_TTL (env) wins, then
// the stored setting, then the config default.
func (s *Server) sessionTTL(ctx context.Context) time.Duration {
	if s.cfg.SessionTTLFromEnv {
		return s.cfg.SessionTTL
	}
	if secs := s.panelSettings(ctx).SessionTTLSeconds; secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return s.cfg.SessionTTL
}

// allowedOrigins resolves the WS Origin allowlist: KRAKEN_ALLOWED_ORIGINS (env)
// wins, then the stored setting; empty means the caller applies dev defaults.
func (s *Server) allowedOrigins(ctx context.Context) []string {
	if s.cfg.AllowedOriginsFromEnv {
		return s.cfg.AllowedOrigins
	}
	if o := s.panelSettings(ctx).AllowedOrigins; len(o) > 0 {
		return o
	}
	return s.cfg.AllowedOrigins
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.toSettingsView(r.Context(), s.panelSettings(r.Context())))
}

type updatePanelSettingsRequest struct {
	// Pointers so an omitted field leaves the value unchanged; "" clears it.
	CloudflareAPIToken *string   `json:"cloudflare_api_token"`
	UnifiURL           *string   `json:"unifi_url"`
	UnifiAPIKey        *string   `json:"unifi_api_key"`
	UnifiSite          *string   `json:"unifi_site"`
	UnifiVerifyTLS     *bool     `json:"unifi_verify_tls"`
	SessionTTLSeconds  *int      `json:"session_ttl_seconds"`
	AllowedOrigins     *[]string `json:"allowed_origins"`
	BootstrapDisabled  *bool     `json:"bootstrap_disabled"`
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req updatePanelSettingsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx := r.Context()
	st, err := s.store.GetSettings(ctx)
	if errors.Is(err, store.ErrNotFound) || st == nil {
		st = &store.Settings{}
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load settings")
		return
	}
	if req.CloudflareAPIToken != nil {
		st.CloudflareAPIToken = strings.TrimSpace(*req.CloudflareAPIToken)
	}
	if req.UnifiURL != nil {
		st.UnifiURL = strings.TrimRight(strings.TrimSpace(*req.UnifiURL), "/")
	}
	if req.UnifiAPIKey != nil {
		st.UnifiAPIKey = strings.TrimSpace(*req.UnifiAPIKey)
	}
	if req.UnifiSite != nil {
		st.UnifiSite = strings.TrimSpace(*req.UnifiSite)
	}
	if req.UnifiVerifyTLS != nil {
		st.UnifiVerifyTLS = *req.UnifiVerifyTLS
	}
	// Global runtime settings — ignored when the equivalent env var locks them.
	if req.SessionTTLSeconds != nil && !s.cfg.SessionTTLFromEnv {
		secs := *req.SessionTTLSeconds
		if secs < 0 {
			secs = 0
		}
		st.SessionTTLSeconds = secs
	}
	if req.AllowedOrigins != nil && !s.cfg.AllowedOriginsFromEnv {
		var origins []string
		for _, o := range *req.AllowedOrigins {
			if t := strings.TrimSpace(o); t != "" {
				origins = append(origins, t)
			}
		}
		st.AllowedOrigins = origins
	}
	if req.BootstrapDisabled != nil && !s.cfg.BootstrapFromEnv {
		st.BootstrapDisabled = *req.BootstrapDisabled
	}
	if err := s.store.SaveSettings(ctx, st); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save settings")
		return
	}
	s.recordAudit(r, http.StatusOK, "settings")
	writeJSON(w, http.StatusOK, s.toSettingsView(ctx, st))
}

// handleTestCloudflare verifies the stored token by listing the zones it can reach.
func (s *Server) handleTestCloudflare(w http.ResponseWriter, r *http.Request) {
	token := s.cloudflareToken(r.Context())
	if token == "" {
		writeError(w, http.StatusBadRequest, "Cloudflare is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	zones, err := cloudflare.New(token).ListZones(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	names := make([]string, len(zones))
	for i, z := range zones {
		names[i] = z.Name
	}
	writeJSON(w, http.StatusOK, map[string]any{"zones": names})
}

// nodeHost returns the player-facing host for a node (its public host, else the
// host part of its control address), or "" if unknown.
func nodeHost(n *cluster.Node) string {
	if n == nil {
		return ""
	}
	if n.PublicHost != "" {
		return n.PublicHost
	}
	if host, _, err := net.SplitHostPort(n.Address); err == nil {
		return host
	}
	return ""
}

// serverExternalHost returns the player-facing host used for DNS records and the
// connect address: the node's detected external/WAN IP, else its operator-set
// public host, else its LAN host. (UniFi-gateway override is layered in later.)
func serverExternalHost(n *cluster.Node) string {
	if n == nil {
		return ""
	}
	if n.ExternalIP != "" {
		return n.ExternalIP
	}
	return nodeHost(n)
}

// protoForPort returns the protocol ("tcp"/"udp") of the named spec port, defaulting to tcp.
func protoForPort(sp *spec.Spec, portName string) string {
	for _, p := range sp.Ports {
		if p.Name == portName && p.Protocol != "" {
			return string(p.Protocol)
		}
	}
	return "tcp"
}

// primaryPortName returns the spec's first declared port name (the default the SRV
// record advertises when the caller doesn't choose one).
func primaryPortName(sp *spec.Spec) string {
	if len(sp.Ports) > 0 {
		return sp.Ports[0].Name
	}
	return ""
}

// cleanupServerExternal best-effort removes external resources a server published
// — Cloudflare DNS records and UniFi port-forward rules. Safe with nothing set or
// integrations unconfigured. Called on server deletion so nothing dangles.
func (s *Server) cleanupServerExternal(ctx context.Context, sv *store.Server) {
	if sv == nil {
		return
	}
	if sv.DNS != nil {
		if token := s.cloudflareToken(ctx); token != "" {
			cf := cloudflare.New(token)
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			for _, id := range sv.DNS.RecordIDs {
				_ = cf.DeleteRecord(cctx, sv.DNS.ZoneID, id)
			}
			cancel()
		}
	}
	if len(sv.Forwards) > 0 {
		if uc := s.unifiClient(ctx); uc != nil {
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			for _, f := range sv.Forwards {
				_ = uc.DeleteForward(cctx, f.RuleID)
			}
			cancel()
		}
	}
}

func (s *Server) handleGetServerDNS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sv, err := s.store.GetServer(ctx, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load server")
		return
	}
	if !s.authorizeServer(w, ctx, sv) {
		return
	}
	node, _ := s.store.GetNode(ctx, sv.NodeID)
	writeJSON(w, http.StatusOK, map[string]any{
		"cloudflare_configured": s.cloudflareToken(ctx) != "",
		"unifi_configured":      s.unifiClient(ctx) != nil,
		"target_host":           serverExternalHost(node),
		"lan_host":              nodeHost(node),
		"ports":                 sv.Ports,
		"dns":                   sv.DNS,
		"forwards":              sv.Forwards,
	})
}

type setDNSRequest struct {
	Name     string `json:"name"`
	Service  string `json:"service"`
	PortName string `json:"port_name"`
}

func (s *Server) handleSetServerDNS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := s.cloudflareToken(ctx)
	if token == "" {
		writeError(w, http.StatusServiceUnavailable, "Cloudflare is not configured — set an API token in Settings first")
		return
	}
	var req setDNSRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(req.Name, ".")))
	if !hostnameRe.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, "name must be a valid fully-qualified hostname (e.g. play.example.com)")
		return
	}
	req.Service = strings.TrimSpace(req.Service)

	sv, sp, ok := s.loadServerAndSpec(ctx, w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	node, _ := s.store.GetNode(ctx, sv.NodeID)
	host := serverExternalHost(node)
	if host == "" {
		writeError(w, http.StatusBadRequest, "the server's node has no public host yet — bring the node online first")
		return
	}
	portName := req.PortName
	if portName == "" {
		portName = primaryPortName(sp)
	}
	port := sv.Ports[portName]
	if req.Service != "" && port == 0 {
		writeError(w, http.StatusBadRequest, "no allocated port named "+portName)
		return
	}

	cf := cloudflare.New(token)
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	zone, err := cf.ZoneFor(cctx, req.Name)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// Replace any records we created previously (idempotent re-assign).
	if sv.DNS != nil {
		for _, id := range sv.DNS.RecordIDs {
			_ = cf.DeleteRecord(cctx, sv.DNS.ZoneID, id)
		}
	}
	ids := make([]string, 0, 2)
	hostID, err := cf.CreateHostRecord(cctx, zone.ID, req.Name, host)
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not create host record: "+err.Error())
		return
	}
	ids = append(ids, hostID)
	if req.Service != "" {
		srvID, serr := cf.CreateSRVRecord(cctx, zone.ID, req.Service, protoForPort(sp, portName), req.Name, port)
		if serr != nil {
			writeError(w, http.StatusBadGateway, "could not create SRV record: "+serr.Error())
			return
		}
		ids = append(ids, srvID)
	}
	sv.DNS = &store.ServerDNS{Name: req.Name, ZoneID: zone.ID, Service: req.Service, PortName: portName, RecordIDs: ids}
	if err := s.store.UpdateServer(ctx, sv); err != nil {
		writeError(w, http.StatusInternalServerError, "records created but could not be saved")
		return
	}
	s.logger.Info("server DNS set", "server", sv.ID, "name", req.Name, "records", len(ids))
	s.recordAudit(r, http.StatusOK, "dns:"+req.Name)
	writeJSON(w, http.StatusOK, map[string]any{"dns": sv.DNS})
}

func (s *Server) handleDeleteServerDNS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sv, err := s.store.GetServer(ctx, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load server")
		return
	}
	if !s.authorizeServer(w, ctx, sv) {
		return
	}
	if sv.DNS == nil {
		writeJSON(w, http.StatusOK, map[string]any{"dns": nil})
		return
	}
	if token := s.cloudflareToken(ctx); token != "" {
		cf := cloudflare.New(token)
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		for _, id := range sv.DNS.RecordIDs {
			_ = cf.DeleteRecord(cctx, sv.DNS.ZoneID, id)
		}
	}
	name := sv.DNS.Name
	sv.DNS = nil
	if err := s.store.UpdateServer(ctx, sv); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update server")
		return
	}
	s.recordAudit(r, http.StatusOK, "dns-remove:"+name)
	writeJSON(w, http.StatusOK, map[string]any{"dns": nil})
}
