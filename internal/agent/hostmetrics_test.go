package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A rate needs two samples. The first one must report cpu/net unknown rather
// than differencing against a zero baseline, which would read as 100% busy and
// a throughput spike the size of the counter.
func TestTelemetryFirstSampleHasNoRates(t *testing.T) {
	cur := hostSnapshot{
		at:       time.Now(),
		cpuBusy:  500,
		cpuTotal: 1000,
		cpuOK:    true,

		netRxBytes: 1 << 30,
		netTxBytes: 1 << 30,
		netOK:      true,

		memTotalMB: 16000, memUsedMB: 8000, memOK: true,
	}
	tel := telemetryFrom(hostSnapshot{}, cur)

	if tel.CpuKnown {
		t.Errorf("cpu must be unknown on the first sample, got %.1f%%", tel.CpuPercent)
	}
	if tel.NetKnown {
		t.Errorf("net must be unknown on the first sample, got rx=%.0f tx=%.0f", tel.NetRxBps, tel.NetTxBps)
	}
	// Instantaneous metrics need no history and must be available immediately.
	if !tel.MemKnown || tel.MemUsedMb != 8000 {
		t.Errorf("memory should be known on the first sample, got known=%v used=%d", tel.MemKnown, tel.MemUsedMb)
	}
}

func TestTelemetryRatesAcrossInterval(t *testing.T) {
	t0 := time.Now()
	prev := hostSnapshot{
		at: t0, cpuBusy: 1000, cpuTotal: 4000, cpuOK: true,
		netRxBytes: 1000, netTxBytes: 500, netOK: true,
	}
	// Over 2s: 300 of 1000 new CPU ticks were busy; 2000 bytes in, 1000 out.
	cur := hostSnapshot{
		at: t0.Add(2 * time.Second), cpuBusy: 1300, cpuTotal: 5000, cpuOK: true,
		netRxBytes: 3000, netTxBytes: 1500, netOK: true,
	}
	tel := telemetryFrom(prev, cur)

	if !tel.CpuKnown {
		t.Fatal("cpu should be known with two samples")
	}
	if got := tel.CpuPercent; got < 29.9 || got > 30.1 {
		t.Errorf("cpu percent = %.2f, want 30", got)
	}
	if !tel.NetKnown {
		t.Fatal("net should be known with two samples")
	}
	if got := tel.NetRxBps; got != 1000 {
		t.Errorf("rx bps = %.0f, want 1000", got)
	}
	if got := tel.NetTxBps; got != 500 {
		t.Errorf("tx bps = %.0f, want 500", got)
	}
}

// A reboot resets the kernel's counters. Differencing across that produces a
// negative delta, which must read as unknown — not as a wrapped huge number.
func TestTelemetryCounterResetReportsUnknown(t *testing.T) {
	t0 := time.Now()
	prev := hostSnapshot{
		at: t0, cpuBusy: 9000, cpuTotal: 10000, cpuOK: true,
		netRxBytes: 1 << 40, netTxBytes: 1 << 40, netOK: true,
	}
	cur := hostSnapshot{
		at: t0.Add(2 * time.Second), cpuBusy: 10, cpuTotal: 100, cpuOK: true,
		netRxBytes: 20, netTxBytes: 10, netOK: true,
	}
	tel := telemetryFrom(prev, cur)

	if tel.CpuKnown {
		t.Errorf("cpu must be unknown across a counter reset, got %.1f%%", tel.CpuPercent)
	}
	if tel.NetKnown {
		t.Errorf("net must be unknown across a counter reset, got rx=%.0f", tel.NetRxBps)
	}
}

// A metric group whose source failed stays unknown; the groups that did read
// are unaffected. This is what keeps a Windows node (no temperature sensor)
// from reporting 0°C alongside its perfectly good CPU number.
func TestTelemetryUnknownGroupsAreIndependent(t *testing.T) {
	t0 := time.Now()
	prev := hostSnapshot{at: t0, cpuBusy: 100, cpuTotal: 1000, cpuOK: true}
	cur := hostSnapshot{
		at: t0.Add(time.Second), cpuBusy: 600, cpuTotal: 2000, cpuOK: true,
		memOK:  false, // /proc/meminfo unreadable
		diskOK: false, // statfs failed
		netOK:  false, // no interfaces
		tempOK: false, // no sensor
	}
	tel := telemetryFrom(prev, cur)

	if !tel.CpuKnown {
		t.Error("cpu should still be known when other sources fail")
	}
	for name, known := range map[string]bool{
		"mem": tel.MemKnown, "disk": tel.DiskKnown, "net": tel.NetKnown, "temp": tel.TempKnown,
	} {
		if known {
			t.Errorf("%s should be unknown", name)
		}
	}
	if tel.TempCelsius != 0 || tel.MemTotalMb != 0 {
		t.Error("unknown groups must not carry values")
	}
}

// Busy time can momentarily exceed the interval on a host with many cores and a
// coarse clock. Clamping keeps the UI's percentage bars from overflowing.
func TestTelemetryClampsCPUPercent(t *testing.T) {
	t0 := time.Now()
	prev := hostSnapshot{at: t0, cpuBusy: 0, cpuTotal: 0, cpuOK: true}
	cur := hostSnapshot{at: t0.Add(time.Second), cpuBusy: 2000, cpuTotal: 1000, cpuOK: true}
	if got := telemetryFrom(prev, cur).CpuPercent; got != 100 {
		t.Errorf("cpu percent = %.1f, want clamped to 100", got)
	}
}

func TestHostSamplerPublishesAfterStart(t *testing.T) {
	base := time.Now()
	var n int
	h := &HostSampler{
		interval: time.Hour, // never ticks; we drive sample() by hand
		read: func() hostSnapshot {
			n++
			return hostSnapshot{
				at:       base.Add(time.Duration(n) * time.Second),
				cpuBusy:  uint64(n * 500),
				cpuTotal: uint64(n * 1000),
				cpuOK:    true,
			}
		},
	}

	if h.Telemetry() != nil {
		t.Fatal("no telemetry should exist before the first sample")
	}
	h.sample()
	first := h.Telemetry()
	if first == nil {
		t.Fatal("telemetry should exist after one sample")
	}
	if first.CpuKnown {
		t.Error("one sample cannot produce a cpu rate")
	}
	h.sample()
	second := h.Telemetry()
	if !second.CpuKnown {
		t.Error("two samples should produce a cpu rate")
	}
	if got := second.CpuPercent; got < 49.9 || got > 50.1 {
		t.Errorf("cpu percent = %.2f, want 50", got)
	}
	// The clone must not alias the sampler's live message.
	second.CpuPercent = 999
	if h.Telemetry().CpuPercent == 999 {
		t.Error("Telemetry must return a copy, not the sampler's own message")
	}
}

// The data dir is created lazily per server, so on a node that has never had one
// deployed it does not exist yet. Statfs on a missing path fails, which would
// leave the disk instrument blank on exactly the fresh nodes an operator is most
// likely to be watching — so the reading falls back to the nearest existing
// ancestor, which is on the same filesystem anyway.
func TestResolveDiskPathWalksUpToAnExistingDir(t *testing.T) {
	root := t.TempDir()
	if got := resolveDiskPath(root); got != root {
		t.Errorf("an existing dir should resolve to itself: got %q", got)
	}

	missing := filepath.Join(root, "server-data")
	if got := resolveDiskPath(missing); got != root {
		t.Errorf("a missing leaf should resolve to its parent: got %q, want %q", got, root)
	}

	deep := filepath.Join(root, "a", "b", "c")
	if got := resolveDiskPath(deep); got != root {
		t.Errorf("a missing subtree should resolve to the nearest existing ancestor: got %q, want %q", got, root)
	}

	if got := resolveDiskPath(""); got != "" {
		t.Errorf("an empty path resolves to nothing: got %q", got)
	}
}

// A file where a directory was expected is not a filesystem to measure; keep
// walking rather than handing statfs something it will reject.
func TestResolveDiskPathSkipsNonDirectories(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := resolveDiskPath(file); got != root {
		t.Errorf("a file should resolve to its parent dir: got %q, want %q", got, root)
	}
}

// A fresh node reports disk even before its data dir has been created.
func TestReadDiskOnUncreatedDataDir(t *testing.T) {
	var s hostSnapshot
	readDisk(filepath.Join(t.TempDir(), "server-data"), &s)
	if !s.diskOK {
		t.Fatal("disk should still be readable when the data dir does not exist yet")
	}
	if s.diskTotalMB <= 0 {
		t.Errorf("total = %dMB, want positive", s.diskTotalMB)
	}
	if s.diskPath == "" {
		t.Error("the measured path should be reported")
	}
}
