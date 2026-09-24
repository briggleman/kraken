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
// This is in-process only. A second Panel against the same database would not
// see it; the Agent's own refusal to restore over a running container
// (DockerRuntime.refuseRestoreOverRunningContainer) is the layer below.
type restoreJobs struct {
	mu       sync.Mutex
	byServer map[string]*restoreJob
	starts   map[string]int // serverID -> starts/reinstalls currently holding a claim
}

func newRestoreJobs() *restoreJobs {
	return &restoreJobs{byServer: map[string]*restoreJob{}, starts: map[string]int{}}
}

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
	job := &restoreJob{ServerID: serverID, BackupID: backupID, Phase: "opening", StartedAt: time.Now().UTC()}
	j.byServer[serverID] = job
	return *job, ""
}

// claimStart takes the start side of the lock, or reports false when a restore
// holds the server. Several starts may hold it at once — they do not exclude
// each other here, only a restore. The returned release is idempotent.
func (j *restoreJobs) claimStart(serverID string) (func(), bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.byServer[serverID] != nil {
		return nil, false
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
	}, true
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

// refuseWhileRestoring is the gate every writer of a server's tree asks
// (#361): a restore swaps save files into place by rename, and anything that
// writes the tree mid-swap — a file edit, a config push, a backup and its
// retention pass, a reinstall, a delete — either lands in a directory about to
// be replaced or is replaced itself. It answers 409 server_restoring and
// reports true when the caller must stop. Reads and downloads never ask.
// Called after the handler has authorized the caller, so a refusal reveals
// nothing to someone who may not see the server.
func (s *Server) refuseWhileRestoring(w http.ResponseWriter, sv *store.Server) bool {
	if !s.restoreInProgress(sv) {
		return false
	}
	writeCoded(w, http.StatusConflict, codeServerRestoring,
		"a backup restore is in progress for this server; wait for the restore to finish")
	return true
}

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
	release, ok := s.restores.claimStart(serverID)
	if !ok {
		return nil, restoreRefusal()
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
