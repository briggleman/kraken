package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
)

// Restore jobs (#361): the Panel-side half of a backup restore, tracked in
// memory the way agent-update pushes are (agentupdatejobs.go), and for the same
// reason — a restore is inseparable from one Panel process's gRPC stream. The
// durable half is the server row: it sits in `restoring` with a `restore`
// record (backup, the state to return to, when it began) while a job exists,
// and is written back to that prior state with a `restore_result` before the
// job is cleared, so the row is never left claiming a restore nobody is
// running. If the Panel dies mid-restore the row is the only thing left, and
// the reconciler settles a `restoring` row that has no job here
// (reconcile.go) — to the same prior state, because the row recorded it.
//
// There is at most one job per server. A finished job is dropped at once: its
// outcome belongs on the row, and a second place to read it from would only be
// a second place for the two to disagree.

// restoreDeadline bounds a whole restore. The job is asynchronous, so this is
// not a request timeout: it is "long enough for a big world over a slow
// mirror", matching the scale of the backup path rather than the 10 minutes the
// synchronous handler used to allow.
const restoreDeadline = 2 * time.Hour

// restorePhaseUnary is what the job reports while an Agent too old for the
// stream restores through the unary call: the Panel knows a restore is
// running and nothing else, so bytes_total stays 0 and the meter stays
// indeterminate.
const restorePhaseUnary = "restoring"

type restoreJob struct {
	ServerID   string
	BackupID   string
	Phase      string
	BytesDone  int64
	BytesTotal int64
	StartedAt  time.Time
}

// overlay is the row's durable restore record with this job's live reading
// laid over it — a fresh value, never the stored one mutated.
func (job restoreJob) overlay(r *store.ServerRestore) *store.ServerRestore {
	out := store.ServerRestore{BackupID: job.BackupID, StartedAt: job.StartedAt}
	if r != nil {
		out = *r
	}
	out.Phase, out.BytesDone, out.BytesTotal = job.Phase, job.BytesDone, job.BytesTotal
	return &out
}

// restoreJobs is also the per-server operation lock between a restore and
// anything that boots or reinstalls the server. The row's state cannot be that
// lock: a start writes its state only after its Power call returns — up to a
// minute later — so a restore that re-read the row in between would see
// `offline` and begin while the game boots. Instead every start, restart
// (manual, scheduled or node-scoped) and reinstall holds a claim in `starts`
// for the length of its Agent call, a restore holds its job in `byServer` for
// its whole run, and each refuses while the other is held. Both live under one
// mutex, so the check and the claim are one step.
//
// A retire or a revive (#360) holds the server in `ops` for as long as it
// changes where the server lives — a retire for its whole run (stop, final
// backup, removal), a revive until its row is placed and `installing` — and
// excludes everything else: starts, restores, and a second retire or revive.
//
// This is in-process only. A second Panel against the same database would not
// see it; the Agent's own refusal to restore over a running container
// (DockerRuntime.refuseRestoreOverRunningContainer) is the layer below.
type restoreJobs struct {
	mu       sync.Mutex
	byServer map[string]*restoreJob
	starts   map[string]int    // serverID -> starts/reinstalls currently holding a claim
	ops      map[string]string // serverID -> the retire or revive holding it (opRetire, opRevive)
}

func newRestoreJobs() *restoreJobs {
	return &restoreJobs{byServer: map[string]*restoreJob{}, starts: map[string]int{}, ops: map[string]string{}}
}

// The operations that hold a server in restoreJobs.ops. Each is named so
// that name+"d" reads as a sentence ("this server is being retired").
const (
	opRetire = "retire"
	opRevive = "revive"
	opDelete = "delete"
)

// The machine-readable codes a restore's refusals answer with, in the same
// {"error","code"} envelope as the Agent-call failures (agenterror.go).
const (
	codeServerRestoring   = "server_restoring"    // 409: a restore holds the server's tree
	codeRestoreInProgress = "restore_in_progress" // 409: a second restore was asked for
	codeServerBusy        = "server_busy"         // 409: a start, restart or reinstall holds the server
)

// Why a restore could not be registered.
const (
	restoreRefusedInProgress = codeRestoreInProgress // another restore holds the server
	restoreRefusedBusy       = codeServerBusy        // a start or reinstall holds it
	restoreRefusedOp         = "op"                  // a retire or revive holds it
)

// start registers a job for serverID, or says why it cannot: a restore is
// already running (two restores racing over one data dir would each swap the
// other's staged copies out from under it), or a start holds the server.
func (j *restoreJobs) start(serverID, backupID string) (restoreJob, string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if cur := j.byServer[serverID]; cur != nil {
		return *cur, restoreRefusedInProgress
	}
	if j.starts[serverID] > 0 {
		return restoreJob{}, restoreRefusedBusy
	}
	if j.ops[serverID] != "" {
		return restoreJob{}, restoreRefusedOp
	}
	job := &restoreJob{ServerID: serverID, BackupID: backupID, Phase: "opening", StartedAt: time.Now().UTC()}
	j.byServer[serverID] = job
	return *job, ""
}

// claimStart takes the start side of the lock, or reports what holds the
// server instead: codeServerRestoring for a restore, or the retire or revive
// (opRetire, opRevive). Several starts may hold it at once — they do not
// exclude each other here. The returned release is idempotent.
func (j *restoreJobs) claimStart(serverID string) (func(), string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.byServer[serverID] != nil {
		return nil, codeServerRestoring
	}
	if op := j.ops[serverID]; op != "" {
		return nil, op
	}
	j.starts[serverID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			j.mu.Lock()
			defer j.mu.Unlock()
			if j.starts[serverID]--; j.starts[serverID] <= 0 {
				delete(j.starts, serverID)
			}
		})
	}, ""
}

// holdOp takes the server for a retire or revive, or reports what already
// holds it: codeServerRestoring, codeServerBusy (a start, restart or
// reinstall), or the other operation's name.
func (j *restoreJobs) holdOp(serverID, op string) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	switch {
	case j.byServer[serverID] != nil:
		return codeServerRestoring
	case j.starts[serverID] > 0:
		return codeServerBusy
	case j.ops[serverID] != "":
		return j.ops[serverID]
	}
	j.ops[serverID] = op
	return ""
}

// releaseOp drops a retire's or revive's hold. The caller has written the row.
func (j *restoreJobs) releaseOp(serverID string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.ops, serverID)
}

// opHolding reports the retire or revive holding serverID, or "".
func (j *restoreJobs) opHolding(serverID string) string {
	if j == nil {
		return ""
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.ops[serverID]
}

// active reports the server's running job. Callers get a copy, never the live
// struct: the job goroutine writes progress while a request marshals it.
func (j *restoreJobs) active(serverID string) (restoreJob, bool) {
	if j == nil {
		return restoreJob{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	job := j.byServer[serverID]
	if job == nil {
		return restoreJob{}, false
	}
	return *job, true
}

// progress records one event from the Agent. bytes_done is absolute, and is
// never allowed to move backwards within a job: the meter is a reading the
// operator watches, and a bar that jumps back reads as a restart.
func (j *restoreJobs) progress(serverID, phase string, done, total int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job := j.byServer[serverID]
	if job == nil {
		return
	}
	if phase != "" {
		job.Phase = phase
	}
	if total > 0 {
		job.BytesTotal = total
	}
	if done > job.BytesDone {
		job.BytesDone = done
	}
}

// restoreInProgress is the one question every gate asks: is a restore running
// for sv, by this process's registry or by the row. Both, because the row can
// be read before a handler's write of `restoring` lands, and the registry is
// empty after a Panel restart while the row still says it.
func (s *Server) restoreInProgress(sv *store.Server) bool {
	if _, ok := s.restores.active(sv.ID); ok {
		return true
	}
	return sv.State == store.StateRestoring
}

// retiring reports whether a retire is running for sv, by this process's
// registry or by the row (whose retire block outlives a Panel restart until
// the reconciler settles it).
func (s *Server) retiring(sv *store.Server) bool {
	return s.restores.opHolding(sv.ID) == opRetire || sv.Retire != nil || sv.State == store.StateRetiring
}

// refuseWhileHeld is the gate every writer of a server's tree or config asks.
// It answers 409 and reports true when the caller must stop:
//
//   - server_busy while a retire or revive holds the server (#360): a retire
//     is stopping it, archiving it and removing it, and a write in between is
//     either archived half-done or deleted with the rest;
//   - server_restoring while a restore runs (#361): a restore swaps save files
//     into place by rename, and anything that writes the tree mid-swap — a
//     file edit, a config push, a backup and its retention pass, a reinstall —
//     either lands in a directory about to be replaced or is replaced itself;
//   - server_retired on a retired server, which has no tree and no node to
//     write to: revive it first.
//
// Reads never ask, except where a retired server has nothing to read (the
// file handlers refuse it in agentForServer). Called after the handler has
// authorized the caller, so a refusal reveals nothing to someone who may not
// see the server.
func (s *Server) refuseWhileHeld(w http.ResponseWriter, sv *store.Server) bool {
	switch {
	case s.retiring(sv):
		writeCoded(w, http.StatusConflict, codeServerBusy,
			"this server is being retired; nothing can change it until the retire finishes")
	case s.restores.opHolding(sv.ID) != "":
		writeCoded(w, http.StatusConflict, codeServerBusy,
			"this server is being "+s.restores.opHolding(sv.ID)+"d; wait for that to finish")
	case s.restoreInProgress(sv):
		writeCoded(w, http.StatusConflict, codeServerRestoring,
			"a backup restore is in progress for this server; wait for the restore to finish")
	case sv.State == store.StateRetired:
		writeCoded(w, http.StatusConflict, codeServerRetired, retiredRefusal)
	default:
		return false
	}
	return true
}

// retiredRefusal is what a retired server answers any change with.
const retiredRefusal = "this server is retired — it is on no node and has no files; revive it first"

// restoreClaimHook, when set, runs just after a start claims the server (see
// claimStart) — inside the window a restore used to slip into. Tests use it to
// attempt a restore exactly there; it is nil in the Panel.
var restoreClaimHook func(serverID string)

// claimStart is what every start, restart and reinstall takes before its
// first Agent call and holds until the row is written (or the Agent call has
// returned, on paths that never write the row): the start half of the
// operation lock described on restoreJobs. It refuses with server_restoring
// when a restore holds the server. The caller must call release.
func (s *Server) claimStart(serverID string) (release func(), refusal *startRefusal) {
	release, held := s.restores.claimStart(serverID)
	switch held {
	case "":
	case codeServerRestoring:
		return nil, restoreRefusal()
	default:
		return nil, &startRefusal{status: http.StatusConflict, code: codeServerBusy,
			message: "this server is being " + held + "d; it can start once that finishes"}
	}
	if restoreClaimHook != nil {
		restoreClaimHook(serverID)
	}
	return release, nil
}

// restoreRefusal is the start/restart/reinstall refusal while a restore runs.
func restoreRefusal() *startRefusal {
	return &startRefusal{status: http.StatusConflict, code: codeServerRestoring,
		message: "a backup restore is in progress; wait for the restore to finish"}
}

// finish drops the job. The caller has already written the outcome to the row.
func (j *restoreJobs) finish(serverID string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.byServer, serverID)
}
