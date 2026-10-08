package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/updatecheck"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// updateCheckFirstRun is how long after startup the first fleet pass runs. Not
// at boot: boot is busy, and a node that has not reconnected yet would read
// as unknown for a whole interval.
const updateCheckFirstRun = 5 * time.Minute

// serverUpdateCheckTimeout caps an on-demand check of one server. The answer
// is a SteamCMD session on the node — about twenty seconds when the image is
// already there — and an operator is waiting on it.
const serverUpdateCheckTimeout = 2 * time.Minute

// fleetUpdateCheckTimeout bounds a fleet pass started from the API, which
// outlives the request that asked for it.
const fleetUpdateCheckTimeout = 30 * time.Minute

// StartUpdateChecker launches the daily build check (#392): every server's
// installed Steam build against the build on its branch, a first pass five
// minutes after boot and one every KRAKEN_UPDATE_CHECK_INTERVAL after that.
// It returns immediately, and is a no-op when the interval is 0.
func (s *Server) StartUpdateChecker(ctx context.Context) {
	every := s.cfg.UpdateCheckInterval
	if every <= 0 {
		s.logger.Info("build check: the daily pass is off (KRAKEN_UPDATE_CHECK_INTERVAL=0); checks run on demand and after installs")
		return
	}
	s.logger.Info("build check", "every", every.String(), "first_in", updateCheckFirstRun.String())
	go s.updates.Run(ctx, updateCheckFirstRun, every)
}

// serverJSON is a server as the API answers it: the stored record, plus what
// is worked out at read time.
type serverJSON struct {
	*store.Server
	// Update is the Steam build check (#392): the installed and available
	// builds the last check found and the status they add up to under the
	// spec as it is now.
	Update updatecheck.Result `json:"update"`
}

// handleServerUpdateCheck checks one server's build now and answers with the
// result, which is also recorded on the row.
//
// Refused while the server is installing — its manifest may be mid-write, and
// the install records the build itself when it lands — and while a restore,
// retire or revive holds it, or it is retired. A node that cannot be reached
// is not a refusal: the check runs, reports unknown and says why, which is
// what the operator asked to find out.
func (s *Server) handleServerUpdateCheck(w http.ResponseWriter, r *http.Request) {
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
	if s.refuseWhileHeld(w, sv) {
		return
	}
	if sv.State == store.StateInstalling {
		writeCoded(w, http.StatusConflict, codeServerBusy,
			"this server is installing; the install records its build when it finishes")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), serverUpdateCheckTimeout)
	defer cancel()
	res, err := s.updates.CheckServer(ctx, sv.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "server not found")
		return
	case errors.Is(err, updatecheck.ErrNotPlaced):
		writeCoded(w, http.StatusConflict, codeServerRetired, retiredRefusal)
		return
	case err != nil:
		s.logger.Error("build check failed", "server", sv.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "could not check this server's build: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleFleetUpdateCheck starts a build check of every server and answers 202
// at once: the pass is a SteamCMD session per game image, and its results land
// on the rows, where GET /servers reads them. A pass already running — the
// daily one, or one an operator asked for a moment ago — is joined rather than
// doubled, and the answer says so.
func (s *Server) handleFleetUpdateCheck(w http.ResponseWriter, _ *http.Request) {
	joined := s.updates.Running()
	if !joined {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), fleetUpdateCheckTimeout)
			defer cancel()
			if err := s.updates.CheckAll(ctx); err != nil {
				s.logger.Warn("build check: fleet pass failed", "err", err)
			}
		}()
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"running": true, "joined": joined})
}

// recordInstalledBuild records the build a successful install pass left on
// disk as both the installed and the available build (#392): the pass ran
// app_update, so it is the branch's current build, and the check comes free.
// Best-effort — a manifest that cannot be read leaves the row as it was and is
// logged, never a failed install — and a spec with no build to check is
// skipped. The console gets a system line when it worked.
func (s *Server) recordInstalledBuild(ctx context.Context, sv *store.Server, sp *spec.Spec, node *cluster.Node) {
	if !sp.UpdateCheckFor(sv.Kind).Steam() {
		return
	}
	client, err := s.nodes.Client(node.DialTarget())
	if err != nil {
		s.logger.Warn("could not record the installed build", "server", sv.ID, "err", err)
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	build, err := s.updates.RecordInstalled(rctx, sv, sp, client)
	if err != nil {
		s.logger.Warn("could not record the installed build", "server", sv.ID, "err", err)
		return
	}
	s.installs.AppendSystem(sv.ID, "[panel] installed build "+build)
}
