package api

import (
	"errors"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

func TestJobsSingleFlightOnlyWhilePushing(t *testing.T) {
	j := newAgentUpdateJobs()
	job := j.start("n1", "alpha", "0.1.0", "0.2.0", 100)

	if running, ok := j.running("n1"); !ok || running.ID != job.ID {
		t.Fatalf("a pushing job should be reported as running, got %+v ok=%v", running, ok)
	}
	if _, ok := j.running("other"); ok {
		t.Error("another node reported a running job")
	}

	// Terminal phases must release the gate, or a failed push would lock the
	// node out of ever being retried.
	j.finish(job.ID, agentUpdateFailed, "agent said no")
	if _, ok := j.running("n1"); ok {
		t.Error("a finished job still blocks the node")
	}
	latest, ok := j.latest("n1")
	if !ok || latest.Phase != agentUpdateFailed || latest.Error != "agent said no" {
		t.Errorf("latest should carry the terminal phase and reason, got %+v", latest)
	}
	if latest.FinishedAt.IsZero() {
		t.Error("a finished job has no FinishedAt")
	}
}

// Handing out the live pointer would let a request goroutine marshal a job while
// the pushing goroutine writes BytesSent. Snapshots are the defence, so prove
// they are snapshots.
func TestJobsHandOutCopiesNotTheLiveJob(t *testing.T) {
	j := newAgentUpdateJobs()
	job := j.start("n1", "alpha", "0.1.0", "0.2.0", 900)

	j.progress(job.ID, 450)

	if job.BytesSent != 0 {
		t.Error("the caller's snapshot mutated behind its back")
	}
	fresh, _ := j.latest("n1")
	if fresh.BytesSent != 450 {
		t.Errorf("BytesSent = %d, want 450", fresh.BytesSent)
	}
}

func TestJobsProgressIgnoredOnceTerminal(t *testing.T) {
	j := newAgentUpdateJobs()
	job := j.start("n1", "alpha", "0.1.0", "0.2.0", 900)
	j.finish(job.ID, agentUpdateRestarting, "")

	j.progress(job.ID, 900)

	fresh, _ := j.latest("n1")
	if fresh.BytesSent != 0 {
		t.Errorf("a terminal job accepted progress: BytesSent = %d", fresh.BytesSent)
	}
}

func TestJobsSweepKeepsPushingAndDropsStaleFinished(t *testing.T) {
	j := newAgentUpdateJobs()

	stale := j.start("n-stale", "stale", "0.1.0", "0.2.0", 1)
	j.finish(stale.ID, agentUpdateRestarting, "")
	// Backdate past the TTL.
	j.mu.Lock()
	j.byID[stale.ID].FinishedAt = time.Now().Add(-2 * agentUpdateJobTTL)
	j.mu.Unlock()

	// An old job still pushing must survive any sweep: an unbounded push is
	// exactly what the operator is watching.
	old := j.start("n-old", "old", "0.1.0", "0.2.0", 1)
	j.mu.Lock()
	j.byID[old.ID].StartedAt = time.Now().Add(-4 * agentUpdateJobTTL)
	j.mu.Unlock()

	j.start("n-new", "new", "0.1.0", "0.2.0", 1) // insert triggers the sweep

	if _, ok := j.latest("n-stale"); ok {
		t.Error("a finished job past the TTL survived the sweep")
	}
	if _, ok := j.running("n-old"); !ok {
		t.Error("an in-flight job was swept")
	}
}

// The progress callback is the only signal #159 can render a determinate fill
// from, so it must report absolute totals and must not fire for a chunk the
// stream refused.
func TestPushUpdateReportsAbsoluteProgressAndStopsOnRefusal(t *testing.T) {
	var seen []int64
	stream := &fakeUpdateStream{closeResp: &agentpb.UpdateAgentResponse{ToVersion: "v9"}}
	data := make([]byte, updateChunkSize*2+7)

	if _, err := pushUpdate(stream, meta(), data, func(sent int64) { seen = append(seen, sent) }); err != nil {
		t.Fatalf("pushUpdate: %v", err)
	}
	want := []int64{updateChunkSize, updateChunkSize * 2, int64(len(data))}
	if len(seen) != len(want) {
		t.Fatalf("progress calls = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("progress[%d] = %d, want %d", i, seen[i], want[i])
		}
	}

	// A refused chunk must not be counted as sent.
	seen = nil
	refusing := &fakeUpdateStream{
		sendErrs:  []error{nil, nil, errors.New("connection reset by peer")},
		closeResp: &agentpb.UpdateAgentResponse{ToVersion: "v9"},
	}
	_, _ = pushUpdate(refusing, meta(), make([]byte, updateChunkSize*4), func(sent int64) { seen = append(seen, sent) })
	if len(seen) != 1 || seen[0] != updateChunkSize {
		t.Errorf("progress after a refused chunk = %v, want just [%d]", seen, updateChunkSize)
	}
}
