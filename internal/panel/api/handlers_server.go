package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/scheduler"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/spec"
)

type createServerRequest struct {
	SpecID    string            `json:"spec_id"`
	Name      string            `json:"name"`
	Variables map[string]string `json:"variables"`
	MemoryMB  int               `json:"memory_mb"`
	// SteamGuardCode is an optional one-time 2FA code for specs whose install
	// requires an authenticated Steam login. It is used only for this install and
	// never persisted.
	SteamGuardCode string `json:"steam_guard_code,omitempty"`
	// InstallBepInEx opts this server into BepInEx mod support (only honored when
	// the spec is bepinex_compatible). Persisted on the server so every
	// install/start uses the modded install append + loader command.
	InstallBepInEx bool `json:"install_bepinex,omitempty"`
}

// handleCreateServer schedules a server onto a node, persists it, and kicks off
// the install on the hosting Agent asynchronously. It returns 201 immediately
// with the server in the "installing" state; clients poll GET for progress.
func (s *Server) handleCreateServer(w http.ResponseWriter, r *http.Request) {
	var req createServerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.SpecID == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "spec_id and name are required")
		return
	}
	ctx := r.Context()

	sp, err := s.store.GetSpec(ctx, req.SpecID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "spec not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load spec")
		return
	}

	// Reject variable overrides containing shell metacharacters before they can
	// be substituted into the server's install/startup command (CWE-78).
	if err := sp.ValidateVarOverrides(req.Variables); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list nodes")
		return
	}

	// Scheduler reserves memory + ports on the chosen node (in the loaded copy).
	placement, err := scheduler.Place(sp, nodes)
	if err != nil {
		writeError(w, http.StatusConflict, "no node can host this spec: "+err.Error())
		return
	}
	chosen := findNode(nodes, placement.NodeID)
	if chosen == nil { // unreachable, but be defensive
		writeError(w, http.StatusInternalServerError, "scheduler returned unknown node")
		return
	}
	// Persist the node's updated allocation.
	if err := s.store.UpdateNode(ctx, chosen); err != nil {
		writeError(w, http.StatusInternalServerError, "could not persist node allocation")
		return
	}

	ownerID := ""
	if u := userFrom(ctx); u != nil {
		ownerID = u.ID
	}
	vars := buildVars(sp, placement, req.Variables)
	server := &store.Server{
		ID:       uuid.NewString(),
		Name:     req.Name,
		OwnerID:  ownerID,
		SpecID:   sp.ID,
		NodeID:   chosen.ID,
		Kind:     placement.Kind,
		State:    store.StateInstalling,
		Vars:     vars,
		Settings: sp.ResolveSettings(nil),
		Ports:    placement.Ports,
		MemoryMB: placement.MemoryMB,
		// Only honor the BepInEx opt-in when the spec actually supports it.
		BepInEx:   req.InstallBepInEx && sp.Install.BepInExCompatible,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.CreateServer(ctx, server); err != nil {
		writeError(w, http.StatusInternalServerError, "could not persist server")
		return
	}

	go s.provision(server, sp, chosen, req.SteamGuardCode)

	s.logger.Info("server scheduled", "id", server.ID, "name", server.Name, "node", chosen.Name, "kind", placement.Kind)
	writeJSON(w, http.StatusCreated, server)
}

// provision runs the install phase on the Agent and flips the server's state.
// Runs in its own goroutine with a background context so it survives the request.
// steamGuardCode is the optional one-time 2FA code for authenticated installs.
func (s *Server) provision(server *store.Server, sp *spec.Spec, node *cluster.Node, steamGuardCode string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	client, err := s.nodes.Client(node.Address)
	if err != nil {
		s.failServer(server, "connect agent: "+err.Error())
		return
	}
	agentSpec := toAgentSpec(server, sp)
	if _, err := client.CreateServer(ctx, &agentpb.CreateServerRequest{Spec: agentSpec}); err != nil {
		s.failServer(server, "agent create: "+err.Error())
		return
	}

	// Build the install env. For specs that need an authenticated Steam login,
	// inject the node's stored credentials (+ the one-time Steam Guard code) here
	// only — never into server.Vars, which is persisted and shell-validated.
	installEnv := server.Vars
	if sp.Install.RequiresSteamLogin {
		env, cerr := s.steamInstallEnv(ctx, node, server.Vars, steamGuardCode)
		if cerr != nil {
			s.failServer(server, cerr.Error())
			return
		}
		installEnv = env
	}

	// When BepInEx is enabled, run the vanilla install first, then append the
	// spec's BepInEx install (download + unpack the loader into the data dir). The
	// separator is OS-aware: cmd chains with " & ", POSIX shells with a newline.
	installScript := sp.Install.Script
	if server.BepInEx && sp.Install.BepInExScript != "" {
		sep := "\n"
		if server.Kind == spec.WindowsNative {
			sep = " & "
		}
		installScript = installScript + sep + sp.Install.BepInExScript
	}
	stream, err := client.InstallServer(ctx, &agentpb.InstallServerRequest{
		ServerId:      server.ID,
		Image:         agentSpec.Image,
		InstallScript: spec.Render(installScript, server.Vars),
		Env:           installEnv,
		MemoryLimitMb: int64(server.MemoryMB),
	})
	if err != nil {
		s.failServer(server, "agent install: "+err.Error())
		return
	}
	for {
		ev, err := stream.Recv()
		if err != nil {
			break // EOF or error ends the install stream
		}
		if f, ok := ev.Event.(*agentpb.InstallEvent_Failed); ok {
			s.failServer(server, "install failed: "+f.Failed)
			return
		}
	}

	s.setServerState(server.ID, store.StateOffline)
	s.logger.Info("server installed", "id", server.ID)
}

func (s *Server) failServer(server *store.Server, reason string) {
	s.logger.Error("server provisioning failed", "id", server.ID, "reason", reason)
	s.setServerState(server.ID, store.StateCrashed)
}

// setServerState reloads the server and updates only its state, so a concurrent
// settings edit during async install isn't clobbered by a stale write.
func (s *Server) setServerState(id string, st store.ServerState) {
	sv, err := s.store.GetServer(context.Background(), id)
	if err != nil {
		s.logger.Error("could not load server for state update", "id", id, "err", err)
		return
	}
	sv.State = st
	if err := s.store.UpdateServer(context.Background(), sv); err != nil {
		s.logger.Error("could not update server state", "id", id, "err", err)
	}
}

func (s *Server) handleListServers(w http.ResponseWriter, r *http.Request) {
	servers, err := s.store.ListServers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list servers")
		return
	}
	// Scope the list to servers the caller may access (owner, or PermServerAny),
	// and strip SFTP credential material from the response.
	visible := make([]*store.Server, 0, len(servers))
	for _, sv := range servers {
		if s.mayAccessServer(r.Context(), sv) {
			visible = append(visible, serverView(sv))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": visible})
}

func (s *Server) handleGetServer(w http.ResponseWriter, r *http.Request) {
	sv, err := s.store.GetServer(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get server")
		return
	}
	if !s.authorizeServer(w, r.Context(), sv) {
		return
	}
	writeJSON(w, http.StatusOK, serverView(sv))
}

// serverView returns a shallow copy of sv with SFTP credential material removed,
// so the general server API never leaks the (bcrypt) password hash or keys — those
// are surfaced only via the dedicated SFTP endpoint. Maps are shared read-only.
func serverView(sv *store.Server) *store.Server {
	cp := *sv
	cp.SFTP = nil
	return &cp
}

// handleServerLifecyclePower forwards a power action to the Agent hosting the
// server (looked up by the server's node) and records the resulting state.
// rePushServerSpec re-delivers a server's spec to its Agent so the Agent can
// (re)create the container — from the current image — even after an Agent
// restart cleared its in-memory spec map. CreateServer is idempotent on the
// Agent (it only ensures the data dir and records the spec; no data is touched),
// so this is safe to call before every start/restart. Best-effort: a failure is
// logged, not fatal, since the subsequent power action surfaces real problems.
func (s *Server) rePushServerSpec(ctx context.Context, client agentpb.NodeServiceClient, sv *store.Server, sp *spec.Spec) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := client.CreateServer(cctx, &agentpb.CreateServerRequest{Spec: toAgentSpec(sv, sp)}); err != nil {
		s.logger.Warn("spec re-push before start failed", "server", sv.ID, "err", err)
	}
}

func (s *Server) handleServerLifecyclePower(w http.ResponseWriter, r *http.Request) {
	var req serverPowerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	action, ok := powerActions[req.Action]
	if !ok {
		writeError(w, http.StatusBadRequest, "action must be one of start|stop|restart|kill")
		return
	}
	ctx := r.Context()
	sv, err := s.store.GetServer(ctx, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get server")
		return
	}
	if !s.authorizeServer(w, ctx, sv) {
		return
	}
	node, err := s.store.GetNode(ctx, sv.NodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load hosting node")
		return
	}
	client, err := s.nodes.Client(node.Address)
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not connect to agent: "+err.Error())
		return
	}

	// On start/restart, re-push the spec then render + push the latest game config
	// before launching, so the server boots with current settings — and so the
	// Agent can (re)create the container from the current image even if it lost
	// its in-memory spec (e.g. after an Agent restart).
	if action == agentpb.PowerAction_POWER_ACTION_START || action == agentpb.PowerAction_POWER_ACTION_RESTART {
		if sp, serr := s.store.GetSpec(ctx, sv.SpecID); serr == nil {
			s.rePushServerSpec(ctx, client, sv, sp)
			if _, aerr := s.applyConfig(ctx, sv, sp); aerr != nil {
				s.logger.Warn("config apply before start failed", "server", sv.ID, "err", aerr)
			}
		}
	}

	// Deadline by action. Start/kill return promptly (start is async — the Agent
	// reports STARTING and the watchdog flips to running). Stop/restart must clear
	// the Agent's graceful-stop grace (30s ContainerStop timeout) before it SIGKILLs,
	// plus the recreate+start on restart — otherwise a slow-saving game server (e.g.
	// Palworld) times out mid-stop and the restart never fires.
	powerTimeout := 15 * time.Second
	switch action {
	case agentpb.PowerAction_POWER_ACTION_STOP:
		powerTimeout = 45 * time.Second
	case agentpb.PowerAction_POWER_ACTION_RESTART:
		powerTimeout = 60 * time.Second
	}
	pctx, cancel := context.WithTimeout(ctx, powerTimeout)
	defer cancel()
	resp, err := client.PowerAction(pctx, &agentpb.PowerActionRequest{ServerId: sv.ID, Action: action})
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent error: "+err.Error())
		return
	}
	sv.State = storeStateFromAgent(resp.State)
	if err := s.store.UpdateServer(ctx, sv); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update server state")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": sv.State})
}

// handleDeleteServer removes the server's container on the Agent, releases its
// node allocation, and deletes the record.
func (s *Server) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sv, err := s.store.GetServer(ctx, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get server")
		return
	}
	if !s.authorizeServer(w, ctx, sv) {
		return
	}
	if node, err := s.store.GetNode(ctx, sv.NodeID); err == nil {
		if client, cerr := s.nodes.Client(node.Address); cerr == nil {
			dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, _ = client.RemoveServer(dctx, &agentpb.RemoveServerRequest{ServerId: sv.ID, DeleteData: true})
			cancel()
		}
		// Release the node's reserved memory + ports.
		ports := make([]int, 0, len(sv.Ports))
		for _, p := range sv.Ports {
			ports = append(ports, p)
		}
		node.Release(sv.MemoryMB, ports)
		_ = s.store.UpdateNode(ctx, node)
	}
	// Best-effort cleanup of external resources this server published (Cloudflare
	// DNS records + UniFi port-forwards).
	s.cleanupServerExternal(ctx, sv)
	if err := s.store.DeleteServer(ctx, sv.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete server")
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// ---- helpers ----

func findNode(nodes []*cluster.Node, id string) *cluster.Node {
	for _, n := range nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// steamInstallEnv builds the install env for an authenticated Steam install: a
// copy of baseVars plus the node's stored Steam credentials and the one-time
// Steam Guard code. The credentials exist only in this transient env (passed to
// the install container), never in the persisted server record.
func (s *Server) steamInstallEnv(ctx context.Context, node *cluster.Node, baseVars map[string]string, guardCode string) (map[string]string, error) {
	cfg, err := s.store.GetNodeConfig(ctx, node.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("load node Steam credentials: %w", err)
	}
	if cfg == nil || cfg.SteamUsername == "" {
		return nil, fmt.Errorf("this game requires a Steam login, but node %q has no Steam credentials set (configure them in the node's settings)", node.Name)
	}
	env := make(map[string]string, len(baseVars)+3)
	for k, v := range baseVars {
		env[k] = v
	}
	env["STEAM_USER"] = cfg.SteamUsername
	env["STEAM_PASS"] = cfg.SteamPassword
	env["STEAM_GUARD"] = guardCode
	return env, nil
}

// buildVars merges spec defaults + user overrides, then injects APP_ID (by the
// platform's OS family) and PORT_<NAME> for each allocated host port.
func buildVars(sp *spec.Spec, placement *scheduler.Placement, overrides map[string]string) map[string]string {
	vars := sp.ResolveVars(overrides)
	if appID := sp.AppIDFor(osFamilyForKind(placement.Kind)); appID != "" {
		vars["APP_ID"] = appID
	}
	for name, hostPort := range placement.Ports {
		vars["PORT_"+strings.ToUpper(name)] = strconv.Itoa(hostPort)
	}
	return vars
}

func osFamilyForKind(k spec.PlatformKind) string {
	if k == spec.LinuxNative {
		return "linux"
	}
	return "windows"
}

// toAgentSpec translates a scheduled server + its spec into the Agent's runtime
// ServerSpec, rendering the startup command and mapping allocated ports.
func toAgentSpec(server *store.Server, sp *spec.Spec) *agentpb.ServerSpec {
	image, _ := sp.ImageFor(server.Kind)

	// Protocol comes from the spec; the allocated port is used 1:1 — the server
	// binds it (specs pass {{PORT_<NAME>}} as the bind port) and Docker publishes
	// it on the same host port. A NAT remap (host≠container) would silently break
	// game servers, which bind and advertise a single port end-to-end; the spec's
	// `default` is only the allocator's starting hint, not a fixed container port.
	specByName := make(map[string]spec.Port, len(sp.Ports))
	for _, p := range sp.Ports {
		specByName[p.Name] = p
	}
	ports := make([]*agentpb.PortMapping, 0, len(server.Ports))
	for name, hostPort := range server.Ports {
		sp := specByName[name]
		ports = append(ports, &agentpb.PortMapping{
			Name:          name,
			Protocol:      string(sp.Protocol),
			ContainerPort: int32(hostPort),
			HostPort:      int32(hostPort),
		})
	}

	// A BepInEx server launches through the loader command (./run_bepinex.sh …)
	// so plugins load; fall back to the vanilla command when not modded or unset.
	startupCmd := sp.Startup.Command
	if server.BepInEx && sp.Startup.BepInExCommand != "" {
		startupCmd = sp.Startup.BepInExCommand
	}
	agentSpec := &agentpb.ServerSpec{
		ServerId:       server.ID,
		Image:          image,
		StartupCommand: spec.Render(startupCmd, server.Vars),
		Env:            server.Vars,
		MemoryLimitMb:  int64(server.MemoryMB),
		Ports:          ports,
		// DataPath left empty: the Agent picks the OS-appropriate mount path
		// (/data on Linux, C:\data on Windows).
		ReadyRegex:     sp.Startup.ReadyRegex,
		RestartOnCrash: sp.Startup.Restart.OnCrash,
		MaxRestarts:    int32(sp.Startup.Restart.MaxRetries),
	}
	switch sp.Startup.Stop.Type {
	case spec.StopSignal:
		agentSpec.StopSignal = sp.Startup.Stop.Value
	case spec.StopCommand:
		agentSpec.StopCommand = sp.Startup.Stop.Value
	}
	// Player-count query. Port resolution differs by method:
	//   a2s          → q.Port names a spec PORT → the allocated host port (queried
	//                  on loopback, since ports are published 1:1).
	//   palworld-rest→ q.Port names a SETTING key holding the container-internal
	//                  REST port; q.Password names the admin-password setting. The
	//                  Agent curls it inside the container, so nothing is published.
	if q := sp.Query; q != nil && q.Method != "" {
		switch q.Method {
		case "a2s":
			if hostPort, ok := server.Ports[q.Port]; ok {
				agentSpec.PlayerQuery = &agentpb.PlayerQuery{Method: q.Method, Port: int32(hostPort)}
			}
		case "palworld-rest":
			if pv, err := strconv.Atoi(server.Settings[q.Port]); err == nil && pv > 0 && pv <= 65535 {
				agentSpec.PlayerQuery = &agentpb.PlayerQuery{Method: q.Method, Port: int32(pv), Password: server.Settings[q.Password]}
			}
		}
	}
	// Per-server SFTP credentials (username = server id); pushed so the Agent's
	// SFTP server can authenticate + jail a login to this server's data dir.
	if sf := server.SFTP; sf != nil && sf.Enabled {
		agentSpec.Sftp = &agentpb.SftpAccess{
			Enabled:        true,
			Username:       server.ID,
			PasswordHash:   sf.PasswordHash,
			AuthorizedKeys: sf.Keys,
		}
	}
	return agentSpec
}

func storeStateFromAgent(st agentpb.ServerState) store.ServerState {
	switch st {
	case agentpb.ServerState_SERVER_STATE_RUNNING:
		return store.StateRunning
	case agentpb.ServerState_SERVER_STATE_STARTING:
		return store.StateStarting
	case agentpb.ServerState_SERVER_STATE_STOPPING:
		return store.StateStopping
	case agentpb.ServerState_SERVER_STATE_INSTALLING:
		return store.StateInstalling
	case agentpb.ServerState_SERVER_STATE_CRASHED:
		return store.StateCrashed
	default:
		return store.StateOffline
	}
}
