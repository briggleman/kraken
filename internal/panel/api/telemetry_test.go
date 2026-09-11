package api

import (
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

func TestTelemetryCacheServesFreshReadings(t *testing.T) {
	c := newTelemetryCache()
	now := time.Now()
	c.put("n1", &agentpb.NodeTelemetry{CpuPercent: 42, CpuKnown: true}, 3.5, now)

	got, rtt := c.fresh("n1", now)
	if got == nil {
		t.Fatal("expected the reading just written to be fresh")
	}
	if got.GetCpuPercent() != 42 {
		t.Errorf("cpu = %.0f, want 42", got.GetCpuPercent())
	}
	if rtt != 3.5 {
		t.Errorf("rtt = %.1f, want the 3.5ms stored with the reading", rtt)
	}
	if tel, _ := c.fresh("unknown-node", now); tel != nil {
		t.Error("a node with no reading must return nothing")
	}
}

// A node whose Agent has died must not keep showing its last numbers. A frozen
// but plausible CPU figure reads as a working node; an empty instrument reads
// as what it is.
func TestTelemetryCacheExpiresStaleReadings(t *testing.T) {
	c := newTelemetryCache()
	now := time.Now()
	c.put("n1", &agentpb.NodeTelemetry{CpuPercent: 42, CpuKnown: true}, 1.2, now)

	justInside := now.Add(nodeTelemetryPollInterval*nodeTelemetryStaleFactor - time.Second)
	if tel, _ := c.fresh("n1", justInside); tel == nil {
		t.Error("a reading inside the stale window should still be served")
	}
	justOutside := now.Add(nodeTelemetryPollInterval*nodeTelemetryStaleFactor + time.Second)
	if tel, _ := c.fresh("n1", justOutside); tel != nil {
		t.Error("a reading past the stale window must not be served")
	}
}

func TestTelemetryCacheDropAndForget(t *testing.T) {
	c := newTelemetryCache()
	now := time.Now()
	c.put("n1", &agentpb.NodeTelemetry{CpuKnown: true}, 0.8, now)

	c.drop("n1")
	if tel, _ := c.fresh("n1", now); tel != nil {
		t.Error("a dropped reading must not be served")
	}
	// drop keeps the entry so per-node flags survive; forget removes it.
	c.mu.RLock()
	_, present := c.byID["n1"]
	c.mu.RUnlock()
	if !present {
		t.Error("drop should keep the node's entry, only clear its reading")
	}

	c.forget("n1")
	c.mu.RLock()
	_, present = c.byID["n1"]
	c.mu.RUnlock()
	if present {
		t.Error("forget should remove the node entirely")
	}
}

// The poller logs the "agent too old" notice once, not on every sweep.
func TestTelemetryCacheUnsupportedLogsOnce(t *testing.T) {
	c := newTelemetryCache()
	if first := c.markUnsupported("n1"); !first {
		t.Error("the first mark should report itself as first")
	}
	if first := c.markUnsupported("n1"); first {
		t.Error("a repeat mark must not report itself as first")
	}
}

// An agent that gains the RPC (via an update) must start being served again
// without the Panel restarting.
func TestTelemetryCacheRecoversAfterUnsupported(t *testing.T) {
	c := newTelemetryCache()
	now := time.Now()
	c.markUnsupported("n1")
	if tel, _ := c.fresh("n1", now); tel != nil {
		t.Fatal("an unsupported node should have no reading")
	}

	c.put("n1", &agentpb.NodeTelemetry{CpuPercent: 7, CpuKnown: true}, 2.1, now)
	got, _ := c.fresh("n1", now)
	if got == nil || got.GetCpuPercent() != 7 {
		t.Error("a node that starts answering must be served again")
	}
}

// Unknown groups must survive the hop to JSON as unknown — the whole point of
// the flags is that the browser can tell "no sensor" from a real zero. The link
// RTT rides alongside: it is the Panel's own measurement, not the Agent's, so
// it has no flag and simply carries the value stored with the reading.
func TestTelemetryBodyPreservesUnknownGroups(t *testing.T) {
	body := telemetryBody(&agentpb.NodeTelemetry{
		CpuPercent: 30, CpuKnown: true,
		MemTotalMb: 16000, MemUsedMb: 4000, MemKnown: true,
		DiskKnown: false,
	}, 4.2)
	if !body.CPUKnown || body.CPUPercent != 30 {
		t.Errorf("cpu should carry through: %+v", body)
	}
	if body.DiskKnown {
		t.Error("disk must stay unknown")
	}
	if body.LinkRttMs != 4.2 {
		t.Errorf("link rtt = %.1f, want the 4.2ms measured by the poll", body.LinkRttMs)
	}
}
