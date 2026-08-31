package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// The HTTP-level test can only exercise the push when the Panel was built with
// embedded agent binaries, which a CI checkout is not (dist/ holds a .gitkeep).
// These drive runAgentUpdate directly so the phase machine — the part that would
// silently rot — is covered everywhere.

// stubStream satisfies grpc.ClientStreamingClient for UpdateAgent.
type stubStream struct {
	grpc.ClientStream
	sends    int
	sendErr  error // returned once sends exceed sendOK
	sendOK   int
	closeErr error
}

func (s *stubStream) Send(*agentpb.UpdateAgentChunk) error {
	s.sends++
	if s.sendErr != nil && s.sends > s.sendOK {
		return s.sendErr
	}
	return nil
}

func (s *stubStream) CloseAndRecv() (*agentpb.UpdateAgentResponse, error) {
	if s.closeErr != nil {
		return nil, s.closeErr
	}
	return &agentpb.UpdateAgentResponse{FromVersion: "0.1.0", ToVersion: "0.2.0"}, nil
}

// stubClient implements only UpdateAgent; the embedded interface supplies the
// rest of NodeService as nil methods this path never calls.
type stubClient struct {
	agentpb.NodeServiceClient
	stream  *stubStream
	openErr error
}

func (c *stubClient) UpdateAgent(context.Context, ...grpc.CallOption) (grpc.ClientStreamingClient[agentpb.UpdateAgentChunk, agentpb.UpdateAgentResponse], error) {
	if c.openErr != nil {
		return nil, c.openErr
	}
	return c.stream, nil
}

func testServerForJobs() *Server {
	return &Server{
		logger:    slog.New(slog.DiscardHandler),
		agentJobs: newAgentUpdateJobs(),
	}
}

func runPlan(t *testing.T, client agentpb.NodeServiceClient, size int) agentUpdateJob {
	t.Helper()
	s := testServerForJobs()
	plan := &agentUpdatePlan{
		client: client, data: make([]byte, size), sha: "abc",
		os: "linux", arch: "amd64", fromVersion: "0.1.0",
	}
	job := s.agentJobs.start("n1", "alpha", plan.fromVersion, "0.2.0", int64(size))
	s.runAgentUpdate(job.ID, "n1", "alpha", plan)
	out, ok := s.agentJobs.latest("n1")
	if !ok {
		t.Fatal("job vanished")
	}
	return out
}

// A pushed stream that the agent acks ends at `restarting` — never `done`. The
// agent is mid-swap and mid-reboot; whether it comes back on the new build is
// the node record's story, and asserting it here would be a guess.
func TestRunAgentUpdateEndsAtRestartingOnSuccess(t *testing.T) {
	size := updateChunkSize * 2
	job := runPlan(t, &stubClient{stream: &stubStream{}}, size)

	if job.Phase != agentUpdateRestarting {
		t.Errorf("phase = %q, want %q", job.Phase, agentUpdateRestarting)
	}
	if job.Error != "" {
		t.Errorf("a successful push recorded an error: %q", job.Error)
	}
	if job.BytesSent != int64(size) {
		t.Errorf("BytesSent = %d, want %d — progress must reach the total", job.BytesSent, size)
	}
	if job.FinishedAt.IsZero() {
		t.Error("terminal job has no FinishedAt")
	}
}

// The agent's refusal is the operator's whole diagnosis (#160) and the job's
// error field is the only place the UI can read it.
func TestRunAgentUpdateCarriesTheAgentsRefusal(t *testing.T) {
	refusal := "agent runs in a container; its binary is immutable — pull the new image instead"
	job := runPlan(t, &stubClient{stream: &stubStream{
		sendErr:  io.EOF, // the agent ended the RPC; Send reports EOF, not the reason
		closeErr: status.Error(codes.FailedPrecondition, refusal),
	}}, updateChunkSize*3)

	if job.Phase != agentUpdateFailed {
		t.Fatalf("phase = %q, want %q", job.Phase, agentUpdateFailed)
	}
	if !strings.Contains(job.Error, refusal) {
		t.Errorf("job error lost the agent's reason: %q", job.Error)
	}
	if strings.Contains(job.Error, "stream binary") {
		t.Errorf("job error reports the Send failure instead of the refusal: %q", job.Error)
	}
}

// Opening the stream fails on its own, before any chunk — the job must still
// reach a terminal phase rather than sitting on `pushing` forever.
func TestRunAgentUpdateFailsWhenTheStreamWontOpen(t *testing.T) {
	job := runPlan(t, &stubClient{openErr: errors.New("tunnel closed")}, updateChunkSize)

	if job.Phase != agentUpdateFailed {
		t.Fatalf("phase = %q, want %q", job.Phase, agentUpdateFailed)
	}
	if !strings.Contains(job.Error, "open update stream") || !strings.Contains(job.Error, "tunnel closed") {
		t.Errorf("error should name the stage and the cause, got %q", job.Error)
	}
}
