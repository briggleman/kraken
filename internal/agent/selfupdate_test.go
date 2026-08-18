package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

func testUpdater(t *testing.T, version string) (*SelfUpdater, string) {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "agent.exe")
	if err := os.WriteFile(exe, []byte("OLD-BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	u := newSelfUpdaterAt(version, filepath.Join(dir, "state"), exe, func(string) {}, logger)
	return u, exe
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// stageBinary runs the Begin → write → close half of an update, returning the
// temp path and the received-bytes hash, ready for Commit.
func stageBinary(t *testing.T, u *SelfUpdater, version string, content []byte) (tmp, gotSHA string, size int64) {
	t.Helper()
	tmp, err := u.Begin(version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	w, err := newBinaryWriter(tmp)
	if err != nil {
		t.Fatalf("newBinaryWriter: %v", err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return tmp, w.SumHex(), w.size
}

// TestCommitSwapsAndMilestoneClears — the happy path: verified binary swapped
// in, previous kept as .old, sentinel present until MarkHealthy clears it.
func TestCommitSwapsAndMilestoneClears(t *testing.T) {
	u, exe := testUpdater(t, "v1")
	newBin := []byte("NEW-BINARY")
	tmp, gotSHA, size := stageBinary(t, u, "v2", newBin)

	from, err := u.Commit(tmp, "v2", shaHex(newBin), gotSHA, size)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if from != "v1" {
		t.Errorf("from = %q, want v1", from)
	}
	if b, _ := os.ReadFile(exe); string(b) != "NEW-BINARY" {
		t.Errorf("exe content = %q, want the new binary", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "OLD-BINARY" {
		t.Errorf("exe.old content = %q, want the previous binary", b)
	}
	if _, err := os.Stat(u.sentinelPath()); err != nil {
		t.Fatalf("sentinel missing after commit: %v", err)
	}

	// MarkHealthy on the OLD version must not clear someone else's sentinel...
	u.MarkHealthy()
	if _, err := os.Stat(u.sentinelPath()); err != nil {
		t.Fatal("sentinel cleared by the pre-update binary version")
	}
	// ...but the updated binary (same paths, new version) clears it and the
	// rollback binary.
	u2 := newSelfUpdaterAt("v2", u.stateDir, exe, func(string) {}, u.logger)
	u2.MarkHealthy()
	if _, err := os.Stat(u.sentinelPath()); err == nil {
		t.Error("sentinel survived MarkHealthy on the updated binary")
	}
	if _, err := os.Stat(exe + ".old"); err == nil {
		t.Error(".old survived MarkHealthy")
	}
}

// TestCommitChecksumMismatch — nothing on disk may change when the received
// bytes don't hash to what the Panel declared.
func TestCommitChecksumMismatch(t *testing.T) {
	u, exe := testUpdater(t, "v1")
	tmp, gotSHA, size := stageBinary(t, u, "v2", []byte("NEW-BINARY"))

	if _, err := u.Commit(tmp, "v2", shaHex([]byte("something else")), gotSHA, size); err == nil {
		t.Fatal("Commit accepted a checksum mismatch")
	}
	if b, _ := os.ReadFile(exe); string(b) != "OLD-BINARY" {
		t.Errorf("exe changed on failed commit: %q", b)
	}
	if _, err := os.Stat(tmp); err == nil {
		t.Error("partial temp binary not cleaned up")
	}
	if _, err := os.Stat(u.sentinelPath()); err == nil {
		t.Error("sentinel written despite failed commit")
	}
	// The update slot must be free again.
	if _, err := u.Begin("v2", runtime.GOOS, runtime.GOARCH); err != nil {
		t.Errorf("Begin after failed commit: %v", err)
	}
}

// TestCheckBootCountsAndRollsBack — the crash-loop budget: attempts advance
// per boot, and exhausting them reverts to .old and records the failure.
func TestCheckBootCountsAndRollsBack(t *testing.T) {
	u, exe := testUpdater(t, "v2") // we ARE the (bad) updated binary
	if err := os.WriteFile(exe+".old", []byte("GOOD-OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := u.writeSentinel(updateSentinel{From: "v1", To: "v2", Started: time.Now()}); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= maxUpdateBootAttempts; i++ {
		if u.CheckBoot() {
			t.Fatalf("boot %d requested a restart before the budget was spent", i)
		}
		st, err := u.readSentinel()
		if err != nil {
			t.Fatalf("boot %d: sentinel gone: %v", i, err)
		}
		if st.Attempts != i {
			t.Fatalf("boot %d: attempts = %d", i, st.Attempts)
		}
	}

	// Budget spent: this boot must revert.
	if !u.CheckBoot() {
		t.Fatal("expected rollback restart after the attempt budget")
	}
	if b, _ := os.ReadFile(exe); string(b) != "GOOD-OLD" {
		t.Errorf("exe = %q, want the reverted binary", b)
	}
	if _, err := os.Stat(u.sentinelPath()); err == nil {
		t.Error("sentinel survived rollback")
	}
	if msg := u.LastFailure(); !strings.Contains(msg, "v2") || !strings.Contains(msg, "reverted") {
		t.Errorf("LastFailure = %q, want target + reverted", msg)
	}
}

// TestBeginRejections — platform mismatch, same-version, and container mode
// must all refuse before any bytes are accepted.
func TestBeginRejections(t *testing.T) {
	u, _ := testUpdater(t, "v1")

	if _, err := u.Begin("v2", "plan9", runtime.GOARCH); err == nil {
		t.Error("accepted a wrong-OS binary")
	}
	if _, err := u.Begin("v1", runtime.GOOS, runtime.GOARCH); err == nil {
		t.Error("accepted an update to the running version")
	}
	if _, err := u.Begin("", runtime.GOOS, runtime.GOARCH); err == nil {
		t.Error("accepted an empty version")
	}

	t.Setenv("KRAKEN_IN_CONTAINER", "1")
	if _, err := u.Begin("v2", runtime.GOOS, runtime.GOARCH); err == nil || !strings.Contains(err.Error(), "container") {
		t.Errorf("container guard: err = %v", err)
	}
	t.Setenv("KRAKEN_IN_CONTAINER", "")
	_ = os.Unsetenv("KRAKEN_IN_CONTAINER")

	// Slot exclusivity: a second Begin while one is in flight fails.
	if _, err := u.Begin("v2", runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := u.Begin("v2", runtime.GOOS, runtime.GOARCH); err == nil {
		t.Error("accepted a concurrent update")
	}
}

// --- Service.UpdateAgent over a mock stream --------------------------------

type mockUpdateStream struct {
	grpc.ServerStream
	msgs []*agentpb.UpdateAgentChunk
	resp *agentpb.UpdateAgentResponse
}

func (m *mockUpdateStream) Recv() (*agentpb.UpdateAgentChunk, error) {
	if len(m.msgs) == 0 {
		return nil, io.EOF
	}
	msg := m.msgs[0]
	m.msgs = m.msgs[1:]
	return msg, nil
}

func (m *mockUpdateStream) SendAndClose(r *agentpb.UpdateAgentResponse) error {
	m.resp = r
	return nil
}

// TestServiceUpdateAgent — the full RPC path: meta + chunks in, verified swap,
// response out.
func TestServiceUpdateAgent(t *testing.T) {
	u, exe := testUpdater(t, "v1")
	svc := NewService(NewFakeRuntime("n1", "linux", false, "v1"), WithSelfUpdater(u))

	newBin := []byte("NEW-BINARY-VIA-RPC")
	stream := &mockUpdateStream{msgs: []*agentpb.UpdateAgentChunk{
		{Payload: &agentpb.UpdateAgentChunk_Meta_{Meta: &agentpb.UpdateAgentChunk_Meta{
			Version: "v2", Sha256: shaHex(newBin), Os: runtime.GOOS, Arch: runtime.GOARCH,
		}}},
		{Payload: &agentpb.UpdateAgentChunk_Data{Data: newBin[:5]}},
		{Payload: &agentpb.UpdateAgentChunk_Data{Data: newBin[5:]}},
	}}
	if err := svc.UpdateAgent(stream); err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}
	if stream.resp == nil || stream.resp.FromVersion != "v1" || stream.resp.ToVersion != "v2" {
		t.Errorf("response = %+v", stream.resp)
	}
	if b, _ := os.ReadFile(exe); string(b) != string(newBin) {
		t.Errorf("exe = %q, want the pushed binary", b)
	}
}

// TestServiceUpdateAgentBadChecksum — a corrupted stream must fail the RPC and
// leave the binary untouched.
func TestServiceUpdateAgentBadChecksum(t *testing.T) {
	u, exe := testUpdater(t, "v1")
	svc := NewService(NewFakeRuntime("n1", "linux", false, "v1"), WithSelfUpdater(u))

	stream := &mockUpdateStream{msgs: []*agentpb.UpdateAgentChunk{
		{Payload: &agentpb.UpdateAgentChunk_Meta_{Meta: &agentpb.UpdateAgentChunk_Meta{
			Version: "v2", Sha256: shaHex([]byte("what the panel promised")), Os: runtime.GOOS, Arch: runtime.GOARCH,
		}}},
		{Payload: &agentpb.UpdateAgentChunk_Data{Data: []byte("what actually arrived")}},
	}}
	if err := svc.UpdateAgent(stream); err == nil {
		t.Fatal("UpdateAgent accepted a checksum mismatch")
	}
	if b, _ := os.ReadFile(exe); string(b) != "OLD-BINARY" {
		t.Errorf("exe changed: %q", b)
	}
}

// TestSentinelRoundTrip — the on-disk formats survive a write/read cycle.
func TestSentinelRoundTrip(t *testing.T) {
	u, _ := testUpdater(t, "v1")
	want := updateSentinel{From: "v1", To: "v2", Attempts: 2, Started: time.Now().UTC().Truncate(time.Second)}
	if err := u.writeSentinel(want); err != nil {
		t.Fatal(err)
	}
	got, err := u.readSentinel()
	if err != nil {
		t.Fatal(err)
	}
	if got.From != want.From || got.To != want.To || got.Attempts != want.Attempts {
		t.Errorf("round trip: got %+v want %+v", got, want)
	}
	// Failure record renders through LastFailure.
	u.recordFailure("v9", "boom")
	if msg := u.LastFailure(); !strings.Contains(msg, "v9") || !strings.Contains(msg, "boom") {
		t.Errorf("LastFailure = %q", msg)
	}
	var f updateFailure
	b, _ := os.ReadFile(u.failurePath())
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("failure file not json: %v", err)
	}
}
