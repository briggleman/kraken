package api

import (
	"sync"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
)

// Restore jobs (#361): the Panel-side half of a backup restore, tracked in
// memory the way agent-update pushes are (agentupdatejobs.go), and for the same
// reason — a restore is inseparable from one Panel process's gRPC stream. The
// durable half is the server row: it sits in `restoring` while a job exists and
// is written back to offline (or install_failed) with last_error before the job
// is cleared, so the row is never left claiming a restore nobody is running.
// If the Panel dies mid-restore the row is the only thing left, and the
// reconciler settles a `restoring` row that has no job here (reconcile.go).
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

// restoreView is the `restore` field of a server view while a job is active.
type restoreView struct {
	BackupID   string    `json:"backup_id"`
	Phase      string    `json:"phase"`
	BytesDone  int64     `json:"bytes_done"`
	BytesTotal int64     `json:"bytes_total"`
	StartedAt  time.Time `json:"started_at"`
}

func (job restoreJob) view() *restoreView {
	return &restoreView{
		BackupID: job.BackupID, Phase: job.Phase,
		BytesDone: job.BytesDone, BytesTotal: job.BytesTotal, StartedAt: job.StartedAt,
	}
}

type restoreJobs struct {
	mu       sync.Mutex
	byServer map[string]*restoreJob
}

func newRestoreJobs() *restoreJobs {
	return &restoreJobs{byServer: map[string]*restoreJob{}}
}

// start registers a job for serverID, or reports the one already running. It
// is the single-flight gate: two restores racing over the same data dir would
// each swap the other's staged copies out from under it.
func (j *restoreJobs) start(serverID, backupID string) (restoreJob, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if cur := j.byServer[serverID]; cur != nil {
		return *cur, false
	}
	job := &restoreJob{ServerID: serverID, BackupID: backupID, Phase: "opening", StartedAt: time.Now().UTC()}
	j.byServer[serverID] = job
	return *job, true
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

// finish drops the job. The caller has already written the outcome to the row.
func (j *restoreJobs) finish(serverID string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.byServer, serverID)
}
