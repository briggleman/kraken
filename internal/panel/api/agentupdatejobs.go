package api

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// Agent-update jobs: the Panel-side half of a push, tracked in memory.
//
// Why not a table: a push is inseparable from one Panel process's gRPC stream.
// If the Panel restarts mid-push the stream dies and the Agent aborts its
// receive, so a durable "pushing" row would outlive the thing it describes and
// assert something false. What IS durable already exists a layer down — the
// Agent's sentinel/rollback state reaches us as NodeInfo.last_update_error, and
// the reconciler writes the observed version onto the node record. So this
// registry answers one narrow question ("what is this Panel doing about that
// node right now?") and the node record answers the lasting one.
//
// The contract that falls out: a status lookup with no job is a 404 that MEANS
// "this process knows nothing; read the node record", not an error.
const (
	agentUpdatePushing    = "pushing"
	agentUpdateRestarting = "restarting"
	agentUpdateFailed     = "failed"

	// How long a finished job stays queryable. Long enough that a UI poll and a
	// curious operator both still find it; short enough that a long-lived Panel
	// does not accumulate them.
	agentUpdateJobTTL = time.Hour
)

// agentUpdateJob is one push's story. There is deliberately no "done" phase:
// the job ends when the Panel's part ends. Whether the node came back on the
// new build is the node record's story, and claiming it here would be a guess.
type agentUpdateJob struct {
	ID          string
	NodeID      string
	NodeName    string
	FromVersion string
	ToVersion   string
	Phase       string
	Error       string
	BytesSent   int64
	BytesTotal  int64
	StartedAt   time.Time
	FinishedAt  time.Time
}

type agentUpdateJobs struct {
	mu     sync.Mutex
	byID   map[string]*agentUpdateJob
	byNode map[string]string // node id -> its most recent job id
}

func newAgentUpdateJobs() *agentUpdateJobs {
	return &agentUpdateJobs{byID: map[string]*agentUpdateJob{}, byNode: map[string]string{}}
}

// start registers a pushing job and returns a snapshot of it. Callers get
// copies, never the live struct: the pushing goroutine mutates BytesSent while
// a request goroutine may be marshalling, and handing out the pointer would
// make that a data race.
func (j *agentUpdateJobs) start(nodeID, nodeName, from, to string, total int64) agentUpdateJob {
	job := &agentUpdateJob{
		ID:          uuid.NewString(),
		NodeID:      nodeID,
		NodeName:    nodeName,
		FromVersion: from,
		ToVersion:   to,
		Phase:       agentUpdatePushing,
		BytesTotal:  total,
		StartedAt:   time.Now(),
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.sweepLocked()
	j.byID[job.ID] = job
	j.byNode[nodeID] = job.ID
	return *job
}

// running reports the node's in-flight job, if it has one. This is the
// single-flight gate: the Agent refuses a concurrent update anyway ("an update
// is already in progress"), but refusing here names the job the operator is
// already waiting on instead of surfacing the Agent's refusal as a fresh error.
func (j *agentUpdateJobs) running(nodeID string) (agentUpdateJob, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job := j.latestLocked(nodeID)
	if job == nil || job.Phase != agentUpdatePushing {
		return agentUpdateJob{}, false
	}
	return *job, true
}

// latest reports the node's most recent job in any phase.
func (j *agentUpdateJobs) latest(nodeID string) (agentUpdateJob, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job := j.latestLocked(nodeID)
	if job == nil {
		return agentUpdateJob{}, false
	}
	return *job, true
}

func (j *agentUpdateJobs) latestLocked(nodeID string) *agentUpdateJob {
	id, ok := j.byNode[nodeID]
	if !ok {
		return nil
	}
	return j.byID[id]
}

// progress records bytes handed to the stream. Absolute rather than additive so
// a retry or reorder cannot inflate it past the total.
func (j *agentUpdateJobs) progress(id string, sent int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if job := j.byID[id]; job != nil && job.Phase == agentUpdatePushing {
		job.BytesSent = sent
	}
}

// finish moves a job to a terminal phase. errMsg is the Agent's verbatim
// refusal when phase is failed — it is the operator's whole diagnosis (#160)
// and this is the only place the UI can read it from.
func (j *agentUpdateJobs) finish(id, phase, errMsg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job := j.byID[id]
	if job == nil {
		return
	}
	job.Phase = phase
	job.Error = errMsg
	job.FinishedAt = time.Now()
}

// sweepLocked drops finished jobs past the TTL. Opportunistic on insert, like
// bootstrapRegistry.issue — no timer to own, and the only moment the map grows
// is the only moment it needs pruning. A job still pushing is never swept
// however old: an unbounded push is exactly what the operator is watching.
func (j *agentUpdateJobs) sweepLocked() {
	for id, job := range j.byID {
		if job.Phase == agentUpdatePushing || time.Since(job.FinishedAt) <= agentUpdateJobTTL {
			continue
		}
		delete(j.byID, id)
		if j.byNode[job.NodeID] == id {
			delete(j.byNode, job.NodeID)
		}
	}
}

// body is the wire shape. bytes_total is 0 for a job that failed before the
// binary was sized, so the UI must treat progress as unknown rather than zero.
func (job agentUpdateJob) body() map[string]any {
	out := map[string]any{
		"job_id":       job.ID,
		"node_id":      job.NodeID,
		"from_version": job.FromVersion,
		"to_version":   job.ToVersion,
		"phase":        job.Phase,
		"bytes_sent":   job.BytesSent,
		"bytes_total":  job.BytesTotal,
		"started_at":   job.StartedAt,
	}
	if job.Error != "" {
		out["error"] = job.Error
	}
	if !job.FinishedAt.IsZero() {
		out["finished_at"] = job.FinishedAt
	}
	return out
}
