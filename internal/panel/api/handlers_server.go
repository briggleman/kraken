package api

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	// MemoryMB sizes the server explicitly. Zero (or absent) takes the spec's
	// own figure — its recommended memory, falling back to the minimum. An
	// explicit value must still clear the spec's minimum: below that floor the
	// game does not boot, and provisioning a server that cannot start is not a
	// choice worth honoring.
	MemoryMB int `json:"memory_mb,omitempty"`
	// NodeID pins placement to one node. The scheduler still runs — it checks
	// eligibility (OS/kind, memory, ports) and reserves resources — but only
	// this node is a candidate, and an ineligible pin is a 409 naming the
	// reason rather than a silent placement elsewhere.
	NodeID string `json:"node_id,omitempty"`
	// SteamGuardCode is an optional one-time 2FA code for specs whose install
	// requires an authenticated Steam login. It is used only for this install and
	// never persisted.
	SteamGuardCode string `json:"steam_guard_code,omitempty"`
	// InstallBepInEx opts this server into BepInEx mod support (only honored when
	// the spec is bepinex_compatible). Persisted on the server so every
	// install/start uses the modded install append + loader command.
	InstallBepInEx bool `json:"install_bepinex,omitempty"`
	// PinBuild pins the server to the build its install pulls now: no update
	// pass runs before later starts (see store.Server.PinBuild). Default false.
	PinBuild bool `json:"pin_build,omitempty"`
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

	// A pinned placement narrows the candidate list to that one node — the
	// scheduler still owns eligibility and reservation, so a pin that can't
	// host the spec is refused with the scheduler's own reason instead of
	// quietly landing somewhere else (the pre-pin wizard behavior).
	pinnedName := ""
	if req.NodeID != "" {
		pinned := findNode(nodes, req.NodeID)
		if pinned == nil {
			writeError(w, http.StatusNotFound, "pinned node not found")
			return
		}
		pinnedName = pinned.Name
		nodes = []*cluster.Node{pinned}
	}

	// Memory: the spec's own figure unless the caller sized it explicitly.
	memReq := sp.Resources.AllocMemoryMB()
	if req.MemoryMB != 0 {
		if req.MemoryMB < sp.Resources.MinMemoryMB {
			writeError(w, http.StatusBadRequest, fmt.Sprintf(
				"memory_mb must be at least the spec's minimum of %dMB", sp.Resources.MinMemoryMB))
			return
		}
		memReq = req.MemoryMB
	}

	// Scheduler reserves memory + ports on the chosen node (in the loaded copy).
	placement, err := scheduler.PlaceWithMemory(sp, nodes, memReq)
	if err != nil {
		if pinnedName != "" {
			writeError(w, http.StatusConflict, "node "+pinnedName+" can't host this spec: "+err.Error())
			return
		}
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
		PinBuild:  req.PinBuild,
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

// installScriptFor renders the install script a pass should run: the spec's
// per-platform script (falling back to the spec-level one) with the server's
// CURRENT variables substituted, plus — only when withBepInEx — the spec's
// BepInEx overlay appended after it. The separator is OS-aware: cmd chains with
// " & ", POSIX shells (incl. the wine image, which is a Linux container) with a
// newline.
//
// withBepInEx is false for the pre-start update pass even on a modded server:
// the overlay scripts copy files over the tree rather than update them (they
// would clobber BepInEx/config on every restart) and pull unpinned "latest"
// builds, while a SteamCMD `validate` leaves the loader files alone. It is
// re-run only by create and by an explicit reinstall.
func installScriptFor(sv *store.Server, sp *spec.Spec, withBepInEx bool) string {
	script := sp.InstallScriptFor(sv.Kind)
	if withBepInEx && sp.Install.BepInExScript != "" {
		sep := "\n"
		if sv.Kind == spec.WindowsNative {
			sep = " & "
		}
		script = script + sep + sp.Install.BepInExScript
	}
	return spec.Render(script, sv.Vars)
}

// runInstallPass runs one install phase on the Agent, streaming the installer's
// output into the server's install buffer, and returns the failure reason (nil
// on success). It does NOT touch the server's state or close the buffer — the
// caller owns both, because the three callers differ: create/reinstall end at
// offline, the pre-start update pass continues into a start.
//
// The caller must have opened the buffer (installs.Start) first, so even a
// connect-time failure leaves the operator something to read.
func (s *Server) runInstallPass(ctx context.Context, server *store.Server, sp *spec.Spec, node *cluster.Node, steamGuardCode string, withBepInEx bool) error {
	client, err := s.nodes.Client(node.DialTarget())
	if err != nil {
		return fmt.Errorf("connect agent: %w", err)
	}
	agentSpec := toAgentSpec(server, sp)
	if _, err := client.CreateServer(ctx, &agentpb.CreateServerRequest{Spec: agentSpec}); err != nil {
		return fmt.Errorf("agent create: %w", err)
	}

	// Build the install env. For specs that need an authenticated Steam login,
	// inject the node's stored credentials (+ the one-time Steam Guard code) here
	// only — never into server.Vars, which is persisted and shell-validated.
	installEnv := server.Vars
	if sp.Install.RequiresSteamLogin {
		env, cerr := s.steamInstallEnv(ctx, node, server.Vars, steamGuardCode)
		if cerr != nil {
			return cerr
		}
		installEnv = env
	}

	stream, err := client.InstallServer(ctx, &agentpb.InstallServerRequest{
		ServerId:      server.ID,
		Image:         agentSpec.Image,
		InstallScript: installScriptFor(server, sp, withBepInEx),
		Env:           installEnv,
		MemoryLimitMb: int64(server.MemoryMB),
	})
	if err != nil {
		return fmt.Errorf("agent install: %w", err)
	}
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil // clean end of the install stream = success
		}
		if err != nil {
			// The stream died mid-install (agent restart, tunnel drop, network).
			// The outcome is unknown — which must read as failed, never as
			// installed; a reinstall re-runs the (idempotent) install script.
			return fmt.Errorf("install stream interrupted: %w", err)
		}
		switch e := ev.Event.(type) {
		case *agentpb.InstallEvent_LogLine:
			// The installer's own output — SteamCMD's download progress, unpack
			// errors, a game's first-run complaints. This is the only place it
			// exists: the install container is removed when the phase ends.
			s.installs.Append(server.ID, e.LogLine)
		case *agentpb.InstallEvent_Failed:
			return fmt.Errorf("install failed: %s", e.Failed)
		}
	}
}

// provision runs the install phase on the Agent and flips the server's state.
// Runs in its own goroutine with a background context so it survives the request.
// steamGuardCode is the optional one-time 2FA code for authenticated installs.
func (s *Server) provision(server *store.Server, sp *spec.Spec, node *cluster.Node, steamGuardCode string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// Open a fresh install buffer before anything can fail, so even a
	// connect-time failure leaves the operator something to read. failServer
	// closes it out on every failure path; the success path drops it.
	s.installs.Start(server.ID)
	nodeName := node.Name
	if nodeName == "" {
		nodeName = node.ID
	}
	s.installs.Append(server.ID, "[panel] provisioning "+server.Name+" on "+nodeName)

	if err := s.runInstallPass(ctx, server, sp, node, steamGuardCode, server.BepInEx); err != nil {
		s.failServer(server, err.Error())
		return
	}

	s.installs.Append(server.ID, "[panel] install complete — "+server.Name+" is ready to start")
	s.setServerState(server.ID, store.StateOffline, "")
	// Close the buffer but KEEP it. A successful install is not proof of a
	// working one: an installer can exit 0 having written half a game (#278),
	// and once the state leaves `installing` the console has no container to
	// tail, so dropping the lines here left the operator with a wipe-and-watch
	// reinstall as the only way to see what SteamCMD had actually done (#280).
	// It is freed on delete, and replaced the moment a reinstall starts.
	s.installs.Finish(server.ID)
	s.logger.Info("server installed", "id", server.ID)
}

// failServer marks the server as install_failed — distinct from runtime
// crashed so the power handler can reject start/restart until a reinstall
// clears the failure. Runtime crashes (watchdog surface) still land in
// StateCrashed and remain retriable via the normal power flow.
//
// The reason is persisted on the record (Server.LastError), not just logged:
// an operator whose install failed must be able to read why from the UI/API
// without shell access to the Panel host.
func (s *Server) failServer(server *store.Server, reason string) {
	s.logger.Error("server provisioning failed", "id", server.ID, "reason", reason)
	// The reason lands in the install buffer too, not just on the record: the
	// failures that abort before the Agent stream opens (no route to the node, a
	// rejected create, missing Steam credentials) have no other line to read,
	// and a mid-install failure belongs at the end of the output it interrupted.
	s.installs.AppendError(server.ID, "[panel] "+reason)
	// State first, then close the buffer: a subscriber released by Finish
	// reconnects immediately, and it should find the failed state (and so be
	// handed the log it just lost) rather than a still-installing one.
	s.setServerState(server.ID, store.StateInstallFailed, reason)
	s.installs.Finish(server.ID)
}

// setServerState reloads the server and updates only its state (plus the
// provisioning error that travels with it), so a concurrent settings edit
// during async install isn't clobbered by a stale write. lastError replaces
// the stored value: pass "" to clear it (any non-failed state), the failure
// reason otherwise.
func (s *Server) setServerState(id string, st store.ServerState, lastError string) {
	sv, err := s.store.GetServer(context.Background(), id)
	if err != nil {
		s.logger.Error("could not load server for state update", "id", id, "err", err)
		return
	}
	sv.State = st
	sv.LastError = lastError
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

// installLogResponse is the retained install output for one server.
//
// `retained` is the honest part: the buffer lives in this Panel process's
// memory, so a restart between the install and the read leaves nothing, and the
// caller must be able to tell that from an install that printed nothing at all.
type installLogResponse struct {
	ServerID   string        `json:"server_id"`
	Lines      []installLine `json:"lines"`
	Done       bool          `json:"done"`
	Retained   bool          `json:"retained"`
	StartedMs  int64         `json:"started_ms,omitempty"`
	FinishedMs int64         `json:"finished_ms,omitempty"`
}

// handleServerInstallLog serves the buffered output of a server's most recent
// install, at any state — including long after it succeeded.
//
// The console WebSocket only routes to the install buffer while the server is
// installing or install_failed; afterwards it tails the container, which for a
// server that never started is nothing at all. This endpoint is the way back to
// the one record of what the installer did (#280). It reads the same buffer, so
// it needs no agent and works for a server on a node the Panel cannot reach.
func (s *Server) handleServerInstallLog(w http.ResponseWriter, r *http.Request) {
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
	snap := s.installs.Snapshot(sv.ID)
	resp := installLogResponse{
		ServerID: sv.ID,
		Lines:    snap.Lines,
		Done:     snap.Done,
		Retained: snap.Retained,
	}
	if resp.Lines == nil {
		resp.Lines = []installLine{} // a JSON array, never null
	}
	if !snap.StartedAt.IsZero() {
		resp.StartedMs = snap.StartedAt.UnixMilli()
	}
	if !snap.FinishedAt.IsZero() {
		resp.FinishedMs = snap.FinishedAt.UnixMilli()
	}
	writeJSON(w, http.StatusOK, resp)
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
	// Reject start/restart on a server that never completed its install phase.
	// The runtime container would boot against empty /data and crash-loop
	// immediately, producing misleading "exe not found" errors and locking the
	// server. Stop/kill are allowed through — they're no-ops on a non-running
	// container and let the operator clean up any lingering runtime state.
	if action == agentpb.PowerAction_POWER_ACTION_START || action == agentpb.PowerAction_POWER_ACTION_RESTART {
		switch sv.State {
		case store.StateInstalling:
			writeError(w, http.StatusConflict, "server is still installing; wait for the install to finish before starting")
			return
		case store.StateInstallFailed:
			writeError(w, http.StatusConflict, "server install failed; POST /api/v1/servers/{id}/reinstall to retry")
			return
		}
	}
	node, err := s.store.GetNode(ctx, sv.NodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load hosting node")
		return
	}
	client, err := s.nodes.Client(node.DialTarget())
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
			// Update-on-start (#307): unless the spec opts out or this server
			// pins its build, re-run the install pass before launching so the
			// game picks up depot updates instead of staying forever on the
			// build it was created with. The pass can take many minutes, so it
			// runs in its own goroutine and the request returns 202 with the
			// server already in `installing` — which gates a racing start and
			// routes the console to the live install log for free.
			if s.updatesOnStart(ctx, sv, sp, node) {
				sv.State = store.StateInstalling
				sv.LastError = ""
				sv.LastExitCode, sv.LastExitCodeKnown = 0, false
				if err := s.store.UpdateServer(ctx, sv); err != nil {
					writeError(w, http.StatusInternalServerError, "could not update server state")
					return
				}
				s.logger.Info("server update-on-start requested", "id", sv.ID, "name", sv.Name, "action", req.Action)
				go s.updateThenStart(sv, sp, node)
				writeJSON(w, http.StatusAccepted, map[string]any{"state": sv.State, "updating": true})
				return
			}
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
	// The crash exit code describes the run that ended; a power action begins a
	// new one, so it must not survive into it and explain a server that is now
	// plainly fine. (The reconciler re-attaches it if this server crashes again.)
	if sv.State != store.StateCrashed {
		sv.LastExitCode, sv.LastExitCodeKnown = 0, false
	}
	if err := s.store.UpdateServer(ctx, sv); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update server state")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": sv.State})
}

// updatesOnStart reports whether an operator-initiated start/restart of sv
// should re-run the install pass first (#307). It is on by default — a server
// that never re-runs its installer stays on its creation-day build forever, and
// every bundled install script is an idempotent `app_update … validate`.
//
// It is off when the spec opts out (per-spec or per-platform
// skip_update_on_start), when the operator pinned this server's build, or when
// the pass could not succeed unattended:
//
//   - An authenticated-Steam install can be asked for a Steam Guard code, and a
//     start request carries none. Without stored node credentials the pass could
//     only fail and strand a working server in install_failed, so it is skipped;
//     POST /reinstall remains the way to update one of these, and it takes a code.
//
// Note the Agent's crash watchdog is deliberately NOT a caller: auto-restart
// after a crash happens entirely inside the Agent (monitor.go → ensureAndStart)
// and never reaches the Panel, so a crash loop can never become a re-download
// loop. Scheduled restarts (schedule.go) likewise drive the Agent directly — a
// nightly restart is not an invitation to validate a 30GB tree nightly.
func (s *Server) updatesOnStart(ctx context.Context, sv *store.Server, sp *spec.Spec, node *cluster.Node) bool {
	if sp == nil || sv.PinBuild || sp.SkipUpdateOnStartFor(sv.Kind) {
		return false
	}
	if sp.Install.RequiresSteamLogin {
		cfg, err := s.store.GetNodeConfig(ctx, node.ID)
		if err != nil || cfg == nil || cfg.SteamUsername == "" {
			s.logger.Warn("skipping update-on-start: spec needs a Steam login and the node has no stored credentials",
				"server", sv.ID, "node", node.ID)
			return false
		}
	}
	return true
}

// updateThenStart runs the pre-start update pass and then starts the server.
// Runs in its own goroutine with a background context (the pass outlives the
// request that asked for it); the server is already in `installing`.
//
// A failed pass lands in install_failed with last_error set, exactly like a
// failed create: the power handler then refuses start until a reinstall, which
// is the right outcome — an update that half-wrote the install tree must not be
// launched over.
func (s *Server) updateThenStart(sv *store.Server, sp *spec.Spec, node *cluster.Node) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	s.installs.Start(sv.ID)
	s.installs.Append(sv.ID, "[panel] updating "+sv.Name+" — re-running the install script before start")

	client, err := s.nodes.Client(node.DialTarget())
	if err != nil {
		s.failServer(sv, "connect agent: "+err.Error())
		return
	}
	// Stop first, always. The install container and the game container
	// bind-mount the same data dir, and SteamCMD writing under a running game
	// is how an update pass corrupts a live server. On a `start` the server is
	// already down and this is a no-op; on a `restart` it is the stop half.
	sctx, scancel := context.WithTimeout(ctx, 60*time.Second)
	_, perr := client.PowerAction(sctx, &agentpb.PowerActionRequest{
		ServerId: sv.ID, Action: agentpb.PowerAction_POWER_ACTION_STOP,
	})
	scancel()
	if perr != nil {
		s.failServer(sv, "stop before update: "+perr.Error())
		return
	}

	// Vanilla install script only — never the BepInEx overlay (see
	// installScriptFor).
	if err := s.runInstallPass(ctx, sv, sp, node, "", false); err != nil {
		s.failServer(sv, err.Error())
		return
	}
	s.installs.Append(sv.ID, "[panel] update complete — starting "+sv.Name)

	// Config after the update, not before: the pass can restore a file the
	// depot owns, so the server's settings are re-rendered over the fresh tree.
	if _, aerr := s.applyConfig(ctx, sv, sp); aerr != nil {
		s.logger.Warn("config apply after update failed", "server", sv.ID, "err", aerr)
	}
	pctx, pcancel := context.WithTimeout(ctx, 30*time.Second)
	resp, err := client.PowerAction(pctx, &agentpb.PowerActionRequest{
		ServerId: sv.ID, Action: agentpb.PowerAction_POWER_ACTION_START,
	})
	pcancel()
	if err != nil {
		// The update itself succeeded, so this is NOT install_failed: the tree
		// is good and a plain start can be retried. Land offline with the
		// reason in the log the operator is already reading.
		s.installs.AppendError(sv.ID, "[panel] start after update failed: "+err.Error())
		s.installs.Finish(sv.ID)
		s.setServerState(sv.ID, store.StateOffline, "")
		s.logger.Error("start after update failed", "server", sv.ID, "err", err)
		return
	}
	s.installs.Finish(sv.ID)
	s.setServerState(sv.ID, storeStateFromAgent(resp.State), "")
	s.logger.Info("server updated and started", "id", sv.ID, "state", resp.State)
}

// handleReinstallServer re-runs the install phase without deleting the server
// (so the allocated ports, name, and server ID all survive). It is both the
// retry for a failed install and the explicit "update now" for a server whose
// start path does not update it — one that pins its build, or whose spec opted
// out of update-on-start — which is why it accepts offline and crashed too.
// A server mid-flight (installing/starting/running/stopping) is refused: a
// second install container against a live data dir is the one thing this must
// never do.
func (s *Server) handleReinstallServer(w http.ResponseWriter, r *http.Request) {
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
	switch sv.State {
	case store.StateInstallFailed, store.StateOffline, store.StateCrashed:
		// Stopped states: nothing holds the data dir, so the install container
		// can have it.
	default:
		writeError(w, http.StatusConflict,
			"reinstall needs a stopped server (install_failed, offline or crashed); current state: "+string(sv.State))
		return
	}
	sp, err := s.store.GetSpec(ctx, sv.SpecID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load server spec")
		return
	}
	node, err := s.store.GetNode(ctx, sv.NodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load hosting node")
		return
	}
	// Optional Steam Guard code for authenticated installs — same shape as
	// the initial create-server request.
	var req struct {
		SteamGuardCode string `json:"steam_guard_code,omitempty"`
	}
	_ = decodeJSON(r, &req) // empty body is fine

	sv.State = store.StateInstalling
	sv.LastError = "" // a fresh attempt starts with a clean slate
	if err := s.store.UpdateServer(ctx, sv); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update server state")
		return
	}
	s.logger.Info("server reinstall requested", "id", sv.ID, "name", sv.Name)
	go s.provision(sv, sp, node, req.SteamGuardCode)
	writeJSON(w, http.StatusAccepted, map[string]any{"state": sv.State})
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
		if client, cerr := s.nodes.Client(node.DialTarget()); cerr == nil {
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
	s.installs.Drop(sv.ID)
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

	// Startup command precedence: BepInEx loader (when the server is modded and
	// the spec provides one) > per-platform override > spec-level command.
	// Note BepInEx-under-wine is unvalidated; modded deploys are expected on
	// native platforms.
	startupCmd := sp.StartupCommandFor(server.Kind)
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
		case "log":
			// The Agent follows the container's own console and keeps the
			// roster from the join/leave lines. A log never states the cap, so
			// it is the server's own setting where the operator picks it
			// (Enshrouded's slotCount), else the spec's constant.
			cap := q.MaxPlayers
			if q.MaxPlayersSetting != "" {
				if v, err := strconv.Atoi(strings.TrimSpace(server.Settings[q.MaxPlayersSetting])); err == nil && v > 0 {
					cap = v
				}
			}
			agentSpec.PlayerQuery = &agentpb.PlayerQuery{
				Method: q.Method, JoinRegex: q.JoinRegex, LeaveRegex: q.LeaveRegex, MaxPlayers: int32(cap),
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
