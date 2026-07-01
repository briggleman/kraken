package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/unifi"
)

// unifiClient builds a UniFi client from the stored settings, or nil when the
// UniFi integration isn't configured.
func (s *Server) unifiClient(ctx context.Context) *unifi.Client {
	st := s.panelSettings(ctx)
	if st.UnifiURL == "" || st.UnifiAPIKey == "" {
		return nil
	}
	return unifi.New(st.UnifiURL, st.UnifiAPIKey, st.UnifiSite)
}

// unifiWANIP returns the gateway's WAN IP when UniFi is configured, else "".
// Best-effort: errors are swallowed (the agent's egress echo remains the fallback).
func (s *Server) unifiWANIP(ctx context.Context) string {
	uc := s.unifiClient(ctx)
	if uc == nil {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ip, err := uc.GatewayWANIP(cctx)
	if err != nil {
		return ""
	}
	return ip
}

// handleTestUnifi verifies the stored UniFi credentials by listing port forwards
// and reading the gateway WAN IP.
func (s *Server) handleTestUnifi(w http.ResponseWriter, r *http.Request) {
	uc := s.unifiClient(r.Context())
	if uc == nil {
		writeError(w, http.StatusBadRequest, "UniFi is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	forwards, err := uc.ListPortForwards(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	wan, _ := uc.GatewayWANIP(ctx)
	writeJSON(w, http.StatusOK, map[string]any{"forward_count": len(forwards), "wan_ip": wan})
}

type setForwardRequest struct {
	Open bool `json:"open"`
}

// handleSetServerForward opens (creates/enables) or closes (disables) the UniFi
// port-forward for a named server port. Forwards WAN:port → node-LAN-IP:port.
func (s *Server) handleSetServerForward(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uc := s.unifiClient(ctx)
	if uc == nil {
		writeError(w, http.StatusServiceUnavailable, "UniFi is not configured — set a controller URL + API key in Settings first")
		return
	}
	var req setForwardRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	portName := chi.URLParam(r, "portName")
	sv, sp, ok := s.loadServerAndSpec(ctx, w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	port := sv.Ports[portName]
	if port == 0 {
		writeError(w, http.StatusBadRequest, "no allocated port named "+portName)
		return
	}
	node, _ := s.store.GetNode(ctx, sv.NodeID)
	lanIP := nodeHost(node)
	if lanIP == "" {
		writeError(w, http.StatusBadRequest, "the server's node has no LAN address yet")
		return
	}

	existing := sv.Forwards[portName]
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	id, err := uc.EnsureForward(cctx, unifi.Forward{
		ID:      existing.RuleID,
		Name:    fmt.Sprintf("kraken-%s-%s", sv.Name, portName),
		Port:    port,
		LANIP:   lanIP,
		Proto:   protoForPort(sp, portName),
		Enabled: req.Open,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not update port forward: "+err.Error())
		return
	}
	if sv.Forwards == nil {
		sv.Forwards = map[string]store.PortForward{}
	}
	sv.Forwards[portName] = store.PortForward{RuleID: id, Enabled: req.Open}
	if err := s.store.UpdateServer(ctx, sv); err != nil {
		writeError(w, http.StatusInternalServerError, "forward updated but could not be saved")
		return
	}
	action := "forward-close"
	if req.Open {
		action = "forward-open"
	}
	s.recordAudit(r, http.StatusOK, action+":"+sv.Name+"/"+portName)
	writeJSON(w, http.StatusOK, map[string]any{"forwards": sv.Forwards})
}
