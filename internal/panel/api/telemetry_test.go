package api

import (
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

func TestTelemetryCacheServesFreshReadings(t *testing.T) {
	c := newTelemetryCache()
	now := time.Now()
	c.put("n1", &agentpb.NodeTelemetry{CpuPercent: 42, CpuKnown: true}, now)

	got := c.fresh("n1", now)
	if got == nil {
		t.Fatal("expected the reading just written to be fresh")
	}
	if got.GetCpuPercent() != 42 {
		t.Errorf("cpu = %.0f, want 42", got.GetCpuPercent())
	}
	if c.fresh("unknown-node", now) != nil {
		t.Error("a node with no reading must return nothing")
	}
}

// A node whose Agent has died must not keep showing its last numbers. A frozen
// but plausible CPU figure reads as a working node; an empty instrument reads
// as what it is.
func TestTelemetryCacheExpiresStaleReadings(t *testing.T) {
	c := newTelemetryCache()
	now := time.Now()
	c.put("n1", &agentpb.NodeTelemetry{CpuPercent: 42, CpuKnown: true}, now)

	justInside := now.Add(nodeTelemetryPollInterval*nodeTelemetryStaleFactor - time.Second)
	if c.fresh("n1", justInside) == nil {
		t.Error("a reading inside the stale window should still be served")
	}
	justOutside := now.Add(nodeTelemetryPollInterval*nodeTelemetryStaleFactor + time.Second)
	if c.fresh("n1", justOutside) != nil {
		t.Error("a reading past the stale window must not be served")
	}
}

func TestTelemetryCacheDropAndForget(t *testing.T) {
	c := newTelemetryCache()
	now := time.Now()
	c.put("n1", &agentpb.NodeTelemetry{CpuKnown: true}, now)

	c.drop("n1")
	if c.fresh("n1", now) != nil {
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
	if c.fresh("n1", now) != nil {
		t.Fatal("an unsupported node should have no reading")
	}

	c.put("n1", &agentpb.NodeTelemetry{CpuPercent: 7, CpuKnown: true}, now)
	got := c.fresh("n1", now)
	if got == nil || got.GetCpuPercent() != 7 {
		t.Error("a node that starts answering must be served again")
	}
}

// Unknown groups must survive the hop to JSON as unknown — the whole point of
// the flags is that the browser can tell "no sensor" from "0 degrees".
func TestTelemetryBodyPreservesUnknownGroups(t *testing.T) {
	body := telemetryBody(&agentpb.NodeTelemetry{
		CpuPercent: 30, CpuKnown: true,
		MemTotalMb: 16000, MemUsedMb: 4000, MemKnown: true,
		TempKnown: false, // a Windows node with no sensor
		DiskKnown: false,
	})
	if !body.CPUKnown || body.CPUPercent != 30 {
		t.Errorf("cpu should carry through: %+v", body)
	}
	if body.TempKnown || body.TempCelsius != 0 {
		t.Errorf("temp must stay unknown, got known=%v c=%.1f", body.TempKnown, body.TempCelsius)
	}
	if body.DiskKnown {
		t.Error("disk must stay unknown")
	}
}
