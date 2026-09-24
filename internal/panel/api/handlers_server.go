package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
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
	"github.com/briggleman/kraken/internal/shared/powerbudget"
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

	go s.provision(server, sp, chosen, req.SteamGuardCode, "")

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

// installPassError is an install failure the Agent reported. treeUntouched is
// the Agent saying the pass ended before anything could write to the install
// tree — its pre-install guard refused it because a container still has the
// data dir (#351). A pass like that says nothing about the tree, so callers
// that know the server's previous state put it back there, the way #328 does
// for a pre-update stop that failed, instead of install_failed.
type installPassError struct {
	msg           string
	treeUntouched bool
}

func (e *installPassError) Error() string { return e.msg }

// treeUntouched reports whether err is an Agent-reported failure of a pass that
// never touched the install tree.
func treeUntouched(err error) bool {
	var e *installPassError
	return errors.As(err, &e) && e.treeUntouched
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
			return &installPassError{
				msg:           "install failed: " + e.Failed,
				treeUntouched: ev.GetTreeUntouched(),
			}
		}
	}
}

// provision runs the install phase on the Agent and flips the server's state.
// Runs in its own goroutine with a background context so it survives the request.
// steamGuardCode is the optional one-time 2FA code for authenticated installs.
//
// prev is the state a reinstall started from, or "" for a fresh create. A
// reinstall the Agent refused before touching the tree goes back to prev; a
// fresh create has nothing to go back to and lands install_failed.
func (s *Server) provision(server *store.Server, sp *spec.Spec, node *cluster.Node, steamGuardCode string, prev store.ServerState) {
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
		if prev != "" && treeUntouched(err) {
			s.abortUpdate(server, prev, err.Error())
			return
		}
		s.failServer(server, err.Error())
		return
	}

	s.installs.Append(server.ID, "[panel] install complete — "+server.Name+" is ready to start")
	s.markProvisioned(server.ID, server.Vars, time.Now().UTC())
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
	//
	// The provisioned_at stamp goes with it: it says "this tree was just
	// installed", and a failed pass has left it suspect. install_failed already
	// blocks a start until a reinstall succeeds (which stamps afresh), so this
	// is insurance against any later path out of install_failed inheriting a
	// stamp from the install before the one that failed.
	sv, err := s.store.GetServer(context.Background(), server.ID)
	if err != nil {
		s.logger.Error("could not load server for state update", "id", server.ID, "err", err)
	} else {
		sv.State = store.StateInstallFailed
		sv.LastError = reason
		sv.ProvisionedAt = nil
		if err := s.store.UpdateServer(context.Background(), sv); err != nil {
			s.logger.Error("could not update server state", "id", server.ID, "err", err)
		}
	}
	s.installs.Finish(server.ID)
}

// abortUpdate ends a pre-start update pass that failed before it could touch
// the install tree, putting the server back in the state it was in when the
// pass was asked for (#328).
//
// This is deliberately NOT failServer. install_failed means "the install tree
// is suspect", and it locks start/restart behind a reinstall — a fair price for
// an installer that half-wrote a game, and a lie about a pass that never got
// that far. The failures that land here (no route to the Agent, a stop that the
// Agent never answered) say nothing about the tree: the container is exactly as
// it was, which for the case that produced this is still running the game. So
// the state goes back, the reason lands in last_error where the operator reads
// it, and the retry is the button they already pressed.
func (s *Server) abortUpdate(sv *store.Server, prev store.ServerState, reason string) {
	s.logger.Error("install pass aborted before it touched the tree", "id", sv.ID, "reason", reason, "state", prev)
	s.installs.AppendError(sv.ID, "[panel] "+reason)
	// State before Finish, for the reason failServer gives: a subscriber
	// released by Finish reconnects immediately and should find the settled
	// state rather than a still-installing one.
	s.setServerState(sv.ID, prev, reason)
	s.installs.Finish(sv.ID)
}

// markProvisioned records a successful create or reinstall: the server is
// offline and ready to start, with no error, and its tree was installed at `at`
// — which is what lets a start inside freshInstallWindow skip a redundant update
// pass. installedVars is the variable snapshot the install script was rendered
// from.
//
// The stamp vouches for a tree installed with those values, so it is withheld
// when the row's variables no longer match them: an operator who edited one
// while the install was running cleared the stamp (see the settings handler),
// and stamping now would undo that and let the next start skip the pass the
// edit needs.
func (s *Server) markProvisioned(id string, installedVars map[string]string, at time.Time) {
	sv, err := s.store.GetServer(context.Background(), id)
	if err != nil {
		s.logger.Error("could not load server to mark it provisioned", "id", id, "err", err)
		return
	}
	sv.State = store.StateOffline
	sv.LastError = ""
	if maps.Equal(sv.Vars, installedVars) {
		sv.ProvisionedAt = &at
	} else {
		s.logger.Info("not stamping provisioned_at: variables were edited during the install, so the next start re-runs the pass",
			"id", id)
		sv.ProvisionedAt = nil
	}
	if err := s.store.UpdateServer(context.Background(), sv); err != nil {
		s.logger.Error("could not mark server provisioned", "id", id, "err", err)
	}
}

// requiredSettingsMessage is the sentence an operator reads when a start is
// refused for empty required settings. It names the fields by their labels —
// the words on the Settings tab — and says where to fix them.
func requiredSettingsMessage(missing []spec.SettingField) string {
	names := make([]string, 0, len(missing))
	for _, f := range missing {
		name := f.Label
		if name == "" {
			name = f.Key
		}
		names = append(names, name)
	}
	var list, verb, pronoun string
	switch len(names) {
	case 1:
		list, verb, pronoun = names[0], "is", "it"
	case 2:
		list, verb, pronoun = names[0]+" and "+names[1], "are", "them"
	default:
		list = strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
		verb, pronoun = "are", "them"
	}
	return list + " " + verb + " required before this server can start — set " + pronoun + " on the Settings tab"
}

// startRefusal is a start or restart the Panel will not send to the Agent: the
// status and body a power endpoint answers with, and — as an error — the
// sentence a scheduled restart records in its last_error.
type startRefusal struct {
	status  int
	message string
	code    string   // machine-readable reason; empty for a plain error body
	missing []string // the empty required settings' keys, for required_settings_missing
}

func (e *startRefusal) Error() string { return e.message }

// write answers a power request with the refusal.
func (e *startRefusal) write(w http.ResponseWriter) {
	if e.code == "" {
		writeError(w, e.status, e.message)
		return
	}
	writeJSON(w, e.status, map[string]any{
		"error":            e.message,
		"code":             e.code,
		"missing_settings": e.missing,
	})
}

// checkStartable reports why sv must not be started or restarted, or nil when
// it may be. It is asked before the node is contacted and before any update
// pass, so a refusal changes nothing — no install container, no state change.
// Every path that boots a server asks it: both power endpoints and scheduled
// restarts, so none of them starts a server another would refuse.
//
//   - A server that never completed its install would boot against an empty
//     /data and crash-loop, with misleading "exe not found" errors.
//   - A server whose spec cannot be loaded cannot be checked, so it is not
//     started on the strength of a check that never ran: a spec that no longer
//     exists is a 409 spec_missing, any other store error a 500.
//   - A server with an empty required setting would boot into a crash its own
//     spec predicts. Judged on the EFFECTIVE settings, so a field the spec
//     added later counts its default.
func (s *Server) checkStartable(ctx context.Context, sv *store.Server, action agentpb.PowerAction) *startRefusal {
	verb := "started"
	if action == agentpb.PowerAction_POWER_ACTION_RESTART {
		verb = "restarted"
	}
	switch sv.State {
	case store.StateInstalling:
		return &startRefusal{status: http.StatusConflict,
			message: "server is still installing; wait for the install to finish before starting"}
	case store.StateInstallFailed:
		return &startRefusal{status: http.StatusConflict,
			message: "server install failed; POST /api/v1/servers/{id}/reinstall to retry"}
	}
	// A restore is swapping the save files the game would open (#361). The job
	// as well as the state, so the gate holds even against a row write that
	// raced the restore's own.
	if s.restoreInProgress(sv) {
		return &startRefusal{status: http.StatusConflict, code: "server_restoring",
			message: "a backup restore is in progress; the server can start once it finishes"}
	}
	sp, err := s.store.GetSpec(ctx, sv.SpecID)
	if errors.Is(err, store.ErrNotFound) {
		// Permanent: a spec's id is a UUID, so re-adding the game makes a new
		// spec rather than bringing this one back.
		return &startRefusal{status: http.StatusConflict, code: "spec_missing",
			message: "the game spec this server was built from no longer exists, so it was not " + verb}
	}
	if err != nil {
		s.logger.Error("start refused: could not load the server's spec", "server", sv.ID, "spec", sv.SpecID, "err", err)
		return &startRefusal{status: http.StatusInternalServerError,
			message: "could not load this server's game spec, so it was not " + verb}
	}
	if missing := sp.MissingRequiredSettings(sp.ResolveSettings(sv.Settings)); len(missing) > 0 {
		keys := make([]string, 0, len(missing))
		for _, f := range missing {
			keys = append(keys, f.Key)
		}
		return &startRefusal{status: http.StatusConflict, message: requiredSettingsMessage(missing),
			code: "required_settings_missing", missing: keys}
	}
	return nil
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
			visible = append(visible, s.serverResponse(sv))
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
	writeJSON(w, http.StatusOK, s.serverResponse(sv))
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

// serverResponse is a server as the list and get endpoints answer it: the
// stripped record with a running restore's live reading laid over the row's
// `restore` block (#361). The row holds what the job knew when it began; the
// meter needs the bytes read since, which only the job has.
func (s *Server) serverResponse(sv *store.Server) *store.Server {
	out := serverView(sv)
	if job, ok := s.restores.active(sv.ID); ok {
		out.Restore = job.overlay(sv.Restore)
	}
	return out
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
	// Start and restart are refused on a server that has not finished its
	// install, whose spec cannot be loaded, or whose required settings are
	// empty (see checkStartable). Stop
	// and kill are never refused: they're no-ops on a non-running container, let
	// the operator clean up any lingering runtime state, and refusing to stop a
	// server would be the opposite of safe.
	if action == agentpb.PowerAction_POWER_ACTION_START || action == agentpb.PowerAction_POWER_ACTION_RESTART {
		if refusal := s.checkStartable(ctx, sv, action); refusal != nil {
			refusal.write(w)
			return
		}
	}
	node, err := s.store.GetNode(ctx, sv.NodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load hosting node")
		return
	}
	// Refuse the whole action when the Panel cannot reach the node (#328).
	//
	// Without this, an update-on-start against an unreachable node answered 202
	// and then flipped the server's state on the strength of a failure that had
	// happened entirely on the Panel side — while the container on the node was
	// still running the game. Nothing here can succeed without the Agent, so the
	// operator is told now, and the stored state is left exactly as it is.
	if lerr := s.ensureNodeLive(ctx, node); lerr != nil {
		writeCoded(w, http.StatusServiceUnavailable, codeNodeUnreachable,
			"node "+nodeLabel(node)+" is offline — the panel has no live connection to its agent ("+lerr.Error()+"); nothing was changed")
		return
	}
	client, err := s.nodes.Client(node.DialTarget())
	if err != nil {
		writeAgentError(w, err)
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
				// Otherwise `installing` would be written over a restore that
				// began since the gate above, and SteamCMD run over the swap.
				if s.restoreBegan(ctx, sv.ID) {
					restoreRefusal().write(w)
					return
				}
				// The state to fall back to if the pass aborts before it has
				// touched the install tree (see updateThenStart): captured here,
				// because the next line overwrites it.
				prev := sv.State
				sv.State = store.StateInstalling
				sv.LastError = ""
				sv.LastExitCode, sv.LastExitCodeKnown = 0, false
				if err := s.store.UpdateServer(ctx, sv); err != nil {
					writeError(w, http.StatusInternalServerError, "could not update server state")
					return
				}
				s.logger.Info("server update-on-start requested", "id", sv.ID, "name", sv.Name, "action", req.Action)
				go s.updateThenStart(sv, sp, node, prev)
				writeJSON(w, http.StatusAccepted, map[string]any{"state": sv.State, "updating": true})
				return
			}
			s.rePushServerSpec(ctx, client, sv, sp)
			if _, aerr := s.applyConfig(ctx, sv, sp); aerr != nil {
				s.logger.Warn("config apply before start failed", "server", sv.ID, "err", aerr)
			}
		}
	}

	// The spec re-push and config apply above are two Agent round trips after
	// the start gate; a restore registered in that gap must still win.
	if (action == agentpb.PowerAction_POWER_ACTION_START || action == agentpb.PowerAction_POWER_ACTION_RESTART) &&
		s.restoreBegan(ctx, sv.ID) {
		restoreRefusal().write(w)
		return
	}
	pctx, cancel := context.WithTimeout(ctx, powerTimeout(action))
	defer cancel()
	resp, err := client.PowerAction(pctx, &agentpb.PowerActionRequest{ServerId: sv.ID, Action: action})
	if err != nil {
		writeAgentError(w, err)
		return
	}
	// Stop and kill still reach the Agent while a restore runs — they are how
	// an operator clears a container that should not be there — but the row
	// stays `restoring`: writing the Agent's `offline` over it would lift the
	// start gate with the swap still underway. The job settles the row.
	if _, restoring := s.restores.active(sv.ID); restoring {
		writeJSON(w, http.StatusOK, map[string]any{"state": store.StateRestoring})
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

// powerTimeout is the Panel's deadline for one power RPC of the given action:
// the Agent's worst case on a Windows node — the slow one, where the daemon
// waits up to 75s for a killed container — plus a margin (see powerbudget).
// Every Panel call site that sends a PowerAction uses it, or one of the named
// deadlines below that are built from it, and a test holds each to the
// Agent's own budget: a deadline shorter than that cancels an action the Agent
// would have finished.
func powerTimeout(action agentpb.PowerAction) time.Duration {
	return powerbudget.Deadline(action)
}

// The power deadlines outside the power handler, named so the budget test can
// check them rather than re-test powerTimeout.
var (
	// scheduledRestartTimeout bounds a scheduled task's RESTART (schedule.go).
	scheduledRestartTimeout = powerTimeout(agentpb.PowerAction_POWER_ACTION_RESTART)
	// preUpdateStopTimeout bounds the STOP before an update pass (updateThenStart).
	preUpdateStopTimeout = powerTimeout(agentpb.PowerAction_POWER_ACTION_STOP)
	// postUpdateStartTimeout bounds the START after an update pass.
	postUpdateStartTimeout = powerTimeout(agentpb.PowerAction_POWER_ACTION_START)
)

// freshInstallWindow is how long after a create or reinstall a start skips the
// update-on-start pass. It covers the deploy form's "start once the install
// finishes", which fires the moment the install lands, and an operator who stops
// to fill in settings before starting. It is deliberately short: a server created
// and then left for days must still update on its first start, which is why
// this is a window and not a "never started" flag.
const freshInstallWindow = 30 * time.Minute

// freshlyProvisioned reports whether sv's install pass completed within
// freshInstallWindow of now — in which case the tree is already current and an
// update pass would only repeat it. A stamp in the future (a clock that stepped
// back, a hand-edited row) is not fresh: it says nothing about when the tree
// was installed, and the pass is the safe answer to not knowing.
func freshlyProvisioned(sv *store.Server, now time.Time) bool {
	if sv.ProvisionedAt == nil {
		return false
	}
	d := now.Sub(*sv.ProvisionedAt)
	return d >= 0 && d < freshInstallWindow
}

// updatesOnStart reports whether an operator-initiated start/restart of sv
// should re-run the install pass first (#307). It is on by default — a server
// that never re-runs its installer stays on its creation-day build forever, and
// every bundled install script is an idempotent `app_update … validate`.
//
// It is off when the spec opts out (per-spec or per-platform
// skip_update_on_start), when the operator pinned this server's build, when the
// server was created or reinstalled within freshInstallWindow (the pass just
// ran), or when the pass could not succeed unattended:
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
	switch s.updateSkipFor(ctx, sv, sp, node.ID) {
	case updateSkipNone:
		return true
	case updateSkipFreshInstall:
		s.logger.Info("skipping update-on-start: the install pass just ran",
			"server", sv.ID, "provisioned_at", sv.ProvisionedAt)
	case updateSkipSteamLogin:
		s.logger.Warn("skipping update-on-start: spec needs a Steam login and the node has no stored credentials",
			"server", sv.ID, "node", node.ID)
	}
	return false
}

// updateSkip names why a start of a server would not run the update pass; the
// empty value means it would. The Settings tab reports it as-is.
type updateSkip string

const (
	updateSkipNone         updateSkip = ""
	updateSkipSpec         updateSkip = "spec"          // the spec opted out
	updateSkipPinned       updateSkip = "pinned"        // the operator pinned the build
	updateSkipFreshInstall updateSkip = "fresh_install" // within freshInstallWindow of an install
	updateSkipSteamLogin   updateSkip = "steam_login"   // authenticated Steam, no stored credentials
)

// updateSkipFor is updatesOnStart's decision without its logging, so a read
// (the Settings tab) can ask what the next start would do without writing a
// "skipping" line for a start that never happened.
func (s *Server) updateSkipFor(ctx context.Context, sv *store.Server, sp *spec.Spec, nodeID string) updateSkip {
	switch {
	case sp == nil || sp.SkipUpdateOnStartFor(sv.Kind):
		return updateSkipSpec
	case sv.PinBuild:
		return updateSkipPinned
	case freshlyProvisioned(sv, time.Now()):
		return updateSkipFreshInstall
	}
	if sp.Install.RequiresSteamLogin {
		cfg, err := s.store.GetNodeConfig(ctx, nodeID)
		if err != nil || cfg == nil || cfg.SteamUsername == "" {
			return updateSkipSteamLogin
		}
	}
	return updateSkipNone
}

// updateThenStart runs the pre-start update pass and then starts the server.
// Runs in its own goroutine with a background context (the pass outlives the
// request that asked for it); the server is already in `installing`. prev is
// the state it was in before that, for the phases that leave it untouched.
//
// The three phases fail differently, and the difference is the whole point
// (#328):
//
//   - Before the install — connecting to the Agent, the pre-update stop —
//     nothing on the node has been touched, so the server goes back to prev
//     with the reason in last_error. See abortUpdate.
//   - The install itself — install_failed with last_error set, exactly like a
//     failed create: the power handler then refuses start until a reinstall,
//     which is the right outcome, since an update that half-wrote the install
//     tree must not be launched over.
//   - The start after a good install — offline. The tree is fine and a plain
//     start can be retried.
func (s *Server) updateThenStart(sv *store.Server, sp *spec.Spec, node *cluster.Node, prev store.ServerState) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	s.installs.Start(sv.ID)
	s.installs.Append(sv.ID, "[panel] updating "+sv.Name+" — re-running the install script before start")

	client, err := s.nodes.Client(node.DialTarget())
	if err != nil {
		s.abortUpdate(sv, prev, "connect agent: "+err.Error())
		return
	}
	// Stop first, always. The install container and the game container
	// bind-mount the same data dir, and SteamCMD writing under a running game
	// is how an update pass corrupts a live server. On a `start` the server is
	// already down and this is a no-op; on a `restart` it is the stop half.
	sctx, scancel := context.WithTimeout(ctx, preUpdateStopTimeout)
	_, perr := client.PowerAction(sctx, &agentpb.PowerActionRequest{
		ServerId: sv.ID, Action: agentpb.PowerAction_POWER_ACTION_STOP,
	})
	scancel()
	if perr != nil {
		s.abortUpdate(sv, prev, "stop before update: "+perr.Error())
		return
	}

	// Vanilla install script only — never the BepInEx overlay (see
	// installScriptFor).
	if err := s.runInstallPass(ctx, sv, sp, node, "", false); err != nil {
		// A pass the Agent refused before touching the tree (a container still
		// holds the data dir) says nothing about the tree, so it is not
		// install_failed. It is not prev either: the stop above has already
		// run and been confirmed, so the server is stopped — offline, with
		// the refusal as the reason. Anything else may have half-written it.
		if treeUntouched(err) {
			s.abortUpdate(sv, store.StateOffline, err.Error())
			return
		}
		s.failServer(sv, err.Error())
		return
	}
	s.installs.Append(sv.ID, "[panel] update complete — starting "+sv.Name)

	// Config after the update, not before: the pass can restore a file the
	// depot owns, so the server's settings are re-rendered over the fresh tree.
	if _, aerr := s.applyConfig(ctx, sv, sp); aerr != nil {
		s.logger.Warn("config apply after update failed", "server", sv.ID, "err", aerr)
	}
	pctx, pcancel := context.WithTimeout(ctx, postUpdateStartTimeout)
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
	if s.refuseWhileRestoring(w, sv) {
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

	if s.restoreBegan(ctx, sv.ID) {
		restoreRefusal().write(w)
		return
	}
	prev := sv.State // where a refused pass puts it back (see provision)
	sv.State = store.StateInstalling
	sv.LastError = "" // a fresh attempt starts with a clean slate
	if err := s.store.UpdateServer(ctx, sv); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update server state")
		return
	}
	s.logger.Info("server reinstall requested", "id", sv.ID, "name", sv.Name)
	go s.provision(sv, sp, node, req.SteamGuardCode, prev)
	writeJSON(w, http.StatusAccepted, map[string]any{"state": sv.State})
}

// handleDeleteServer removes the server's container and data on the Agent,
// releases its node allocation, and deletes the record and its schedules.
//
// A removal that does not land — the node is down, or the Agent reports a
// failure — does not stop the delete: it is recorded on the node as a pending
// removal and finished by the node reconciler once the node answers (#354).
// Backups are not removed; they stay on the node.
func (s *Server) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	// Detached from the request: once the removal has been attempted, the
	// record of how it went must be written even if the client has gone.
	ctx := context.WithoutCancel(r.Context())
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
	// Before anything is recorded: a delete refused for a running restore
	// must leave no pending removal behind for the reconciler to replay.
	if s.refuseWhileRestoring(w, sv) {
		return
	}
	// A node that no longer exists has nothing to be told and nothing to hold
	// the allocation; any other failure to read it means the removal could be
	// neither delivered nor remembered, and a delete the Panel cannot remember
	// does not happen.
	node, err := s.store.GetNode(ctx, sv.NodeID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		node = nil
	case err != nil:
		s.logger.Error("server delete refused: could not load its node", "server", sv.ID, "node", sv.NodeID, "err", err)
		writeError(w, http.StatusInternalServerError, "could not load the server's node; nothing was deleted")
		return
	}
	if node != nil {
		removeErr := s.removeOnNode(ctx, node, sv.ID, true)
		if err := s.settleNodeAfterDelete(ctx, sv, node.ID, removeErr); err != nil {
			s.logger.Error("server delete refused: could not record its removal on the node",
				"server", sv.ID, "node", node.ID, "removal_err", removeErr, "err", err)
			msg := "could not record the removal on the server's node; the server was not deleted"
			if removeErr == nil {
				// The node did its part: the containers and the data are gone.
				// Only the Panel's books are behind, and a retry settles them.
				msg = "the server's data was removed on the node but the delete could not be recorded; retry the delete"
			}
			writeError(w, http.StatusInternalServerError, msg)
			return
		}
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
