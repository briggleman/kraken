package api

import (
	"errors"
	"io"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// fakeUpdateStream stands in for the UpdateAgent client stream. sendErrs is
// consumed one entry per Send (nil meaning "accepted"); once exhausted, Send
// keeps returning the last entry, which is how a server that has ended the RPC
// behaves — every subsequent Send reports io.EOF.
type fakeUpdateStream struct {
	sendErrs  []error
	sends     int
	bytesSeen int
	closeResp *agentpb.UpdateAgentResponse
	closeErr  error
}

func (f *fakeUpdateStream) Send(c *agentpb.UpdateAgentChunk) error {
	f.sends++
	f.bytesSeen += len(c.GetData())
	if len(f.sendErrs) == 0 {
		return nil
	}
	i := min(f.sends-1, len(f.sendErrs)-1)
	return f.sendErrs[i]
}

func (f *fakeUpdateStream) CloseAndRecv() (*agentpb.UpdateAgentResponse, error) {
	return f.closeResp, f.closeErr
}

func meta() *agentpb.UpdateAgentChunk_Meta {
	return &agentpb.UpdateAgentChunk_Meta{Version: "v9.9.9", Os: "linux", Arch: "amd64"}
}

// The regression this file exists for. An agent that cannot write its own
// install directory refuses right after the metadata, before reading a chunk —
// so the Panel's Send starts returning io.EOF while megabytes are still queued.
// Reporting that Send error yielded "stream binary: EOF" and discarded the only
// sentence that said what was wrong, which is what made an unwritable
// /usr/local/bin (ProtectSystem=strict, root-owned dir) undiagnosable from the
// Panel. CloseAndRecv's status must win.
func TestPushUpdateSurfacesAgentRefusalNotEOF(t *testing.T) {
	refusal := "open /usr/local/bin/kraken-agent.new: read-only file system"
	stream := &fakeUpdateStream{
		sendErrs: []error{nil, io.EOF}, // metadata lands, then the agent is gone
		closeErr: status.Error(codes.Internal, refusal),
	}

	_, err := pushUpdate(stream, meta(), make([]byte, updateChunkSize*3), nil)
	if err == nil {
		t.Fatal("pushUpdate succeeded; want the agent's refusal")
	}
	if !strings.Contains(err.Error(), refusal) {
		t.Errorf("error does not carry the agent's reason:\n got: %v\nwant it to contain: %s", err, refusal)
	}
	if strings.Contains(err.Error(), "stream binary") {
		t.Errorf("error reports the Send failure instead of the refusal: %v", err)
	}
}

// The metadata Send has the same trap: an agent built without self-update ends
// the RPC before the Panel's first message is even accepted.
func TestPushUpdateSurfacesRefusalWhenMetadataSendFails(t *testing.T) {
	refusal := "self-update is not available on this agent"
	stream := &fakeUpdateStream{
		sendErrs: []error{io.EOF},
		closeErr: status.Error(codes.FailedPrecondition, refusal),
	}

	_, err := pushUpdate(stream, meta(), make([]byte, updateChunkSize*2), nil)
	if err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("want the agent's refusal, got: %v", err)
	}
	// Nothing should follow a failed metadata send.
	if stream.sends != 1 {
		t.Errorf("sends = %d, want 1 — chunks must not be pushed after the metadata fails", stream.sends)
	}
}

func TestPushUpdateSendsEveryByteThenReturnsTheAck(t *testing.T) {
	want := &agentpb.UpdateAgentResponse{FromVersion: "v0.27.1", ToVersion: "v9.9.9"}
	stream := &fakeUpdateStream{closeResp: want}

	// A size that is deliberately not a multiple of the chunk size, so a
	// mis-computed final chunk shows up as a byte count mismatch.
	data := make([]byte, updateChunkSize*2+7)
	resp, err := pushUpdate(stream, meta(), data, nil)
	if err != nil {
		t.Fatalf("pushUpdate: %v", err)
	}
	if resp.GetToVersion() != want.ToVersion {
		t.Errorf("to_version = %q, want %q", resp.GetToVersion(), want.ToVersion)
	}
	if stream.bytesSeen != len(data) {
		t.Errorf("streamed %d bytes, want %d", stream.bytesSeen, len(data))
	}
	if stream.sends != 4 { // metadata + 3 chunks
		t.Errorf("sends = %d, want 4 (metadata + 3 chunks)", stream.sends)
	}
}

// A truncated push that the agent somehow acks must not be reported as success:
// the checksum should have caught it, and if it did not, a green result would
// bury a real problem.
func TestPushUpdateRefusesToReportSuccessOnATruncatedStream(t *testing.T) {
	sendErr := errors.New("connection reset by peer")
	stream := &fakeUpdateStream{
		sendErrs:  []error{nil, nil, sendErr},
		closeResp: &agentpb.UpdateAgentResponse{FromVersion: "v0.27.1", ToVersion: "v9.9.9"},
	}

	_, err := pushUpdate(stream, meta(), make([]byte, updateChunkSize*4), nil)
	if err == nil {
		t.Fatal("pushUpdate reported success for a stream that failed mid-push")
	}
	if !strings.Contains(err.Error(), sendErr.Error()) {
		t.Errorf("error should carry the transport failure, got: %v", err)
	}
}
