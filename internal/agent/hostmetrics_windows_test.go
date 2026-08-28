//go:build windows

package agent

import (
	"testing"
	"time"
)

// The Windows collector is syscalls all the way down, so there is no fixture
// seam to test the parsing against — the only meaningful check is that the real
// host answers with numbers in a sane range. This is what catches a wrong
// struct layout, a bad FILETIME conversion, or a renamed proc.
func TestWindowsHostReaderAgainstRealHost(t *testing.T) {
	r := newHostReader(t.TempDir())
	first := r.read()

	if !first.memOK {
		t.Fatal("GlobalMemoryStatusEx reported nothing")
	}
	if first.memTotalMB < 512 || first.memTotalMB > 8<<20 {
		t.Errorf("total memory = %dMB, outside a plausible range", first.memTotalMB)
	}
	if first.memUsedMB <= 0 || first.memUsedMB > first.memTotalMB {
		t.Errorf("used memory = %dMB, want within 0..%d", first.memUsedMB, first.memTotalMB)
	}

	if !first.diskOK {
		t.Fatal("GetDiskFreeSpaceEx reported nothing for the temp dir")
	}
	if first.diskUsedMB > first.diskTotalMB {
		t.Errorf("disk used %dMB exceeds total %dMB", first.diskUsedMB, first.diskTotalMB)
	}

	if !first.cpuOK {
		t.Fatal("GetSystemTimes reported nothing")
	}
	if first.cpuCores <= 0 {
		t.Errorf("cores = %d, want positive", first.cpuCores)
	}
	if first.uptime <= 0 {
		t.Errorf("uptime = %v, want positive", first.uptime)
	}

	// Temperature is expected to be unavailable on Windows; assert the contract
	// rather than the value, so a future WMI source has to update this.
	if first.tempOK {
		t.Errorf("temperature unexpectedly reported (%.1f°C) — update the docs if a source was added", first.tempC)
	}

	// A second reading must advance the cumulative counters so rates work.
	time.Sleep(50 * time.Millisecond)
	second := r.read()
	if second.cpuTotal <= first.cpuTotal {
		t.Errorf("cpu total did not advance: %d then %d", first.cpuTotal, second.cpuTotal)
	}

	tel := telemetryFrom(first, second)
	if !tel.CpuKnown {
		t.Fatal("two real samples should yield a cpu rate")
	}
	if tel.CpuPercent < 0 || tel.CpuPercent > 100 {
		t.Errorf("cpu percent = %.2f, outside 0..100", tel.CpuPercent)
	}
	t.Logf("host: cpu %.1f%% over %d cores, mem %d/%dMB, disk %d/%dMB, net known=%v rx=%.0f B/s, up %v",
		tel.CpuPercent, tel.CpuCores, tel.MemUsedMb, tel.MemTotalMb,
		tel.DiskUsedMb, tel.DiskTotalMb, tel.NetKnown, tel.NetRxBps, second.uptime.Truncate(time.Second))
}
