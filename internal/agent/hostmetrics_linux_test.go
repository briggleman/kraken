//go:build linux

package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sampleProcStat = `cpu  100 20 50 800 30 0 0 0 0 0
cpu0 50 10 25 400 15 0 0 0 0 0
cpu1 50 10 25 400 15 0 0 0 0 0
intr 12345 0 0
ctxt 987654
btime 1700000000
`

func TestParseProcStat(t *testing.T) {
	busy, total, cores, ok := parseProcStat(sampleProcStat)
	if !ok {
		t.Fatal("parse failed on a well-formed /proc/stat")
	}
	// user+nice+system+idle+iowait = 100+20+50+800+30 = 1000; idle+iowait = 830.
	if total != 1000 {
		t.Errorf("total = %d, want 1000", total)
	}
	if busy != 170 {
		t.Errorf("busy = %d, want 170 (total minus idle and iowait)", busy)
	}
	if cores != 2 {
		t.Errorf("cores = %d, want 2", cores)
	}
}

// guest and guest_nice are already counted inside user and nice. Including them
// in the total would inflate the denominator and under-report CPU load.
func TestParseProcStatExcludesGuestTime(t *testing.T) {
	withGuest := "cpu  100 20 50 800 30 0 0 0 500 100\n"
	_, total, _, ok := parseProcStat(withGuest)
	if !ok {
		t.Fatal("parse failed")
	}
	if total != 1000 {
		t.Errorf("total = %d, want 1000 — guest columns must not be added again", total)
	}
}

func TestParseProcStatRejectsGarbage(t *testing.T) {
	if _, _, _, ok := parseProcStat("cpu  not a number\n"); ok {
		t.Error("expected failure on unparseable counters")
	}
	if _, _, _, ok := parseProcStat("some other file entirely\n"); ok {
		t.Error("expected failure when there is no cpu line")
	}
}

func TestParseMeminfoUsesAvailableNotFree(t *testing.T) {
	// A healthy Linux box: little free memory, most of it reclaimable cache.
	const meminfo = `MemTotal:       16384000 kB
MemFree:          512000 kB
MemAvailable:   12288000 kB
Buffers:          256000 kB
Cached:          8192000 kB
`
	totalMB, usedMB, ok := parseMeminfo(meminfo)
	if !ok {
		t.Fatal("parse failed")
	}
	if totalMB != 16000 {
		t.Errorf("total = %dMB, want 16000", totalMB)
	}
	// 16384000-12288000 = 4096000 kB = 4000 MB. Using MemFree instead would
	// report 15500MB used and make an idle host look nearly full.
	if usedMB != 4000 {
		t.Errorf("used = %dMB, want 4000 (total minus MemAvailable)", usedMB)
	}
}

func TestParseMeminfoFallsBackWithoutMemAvailable(t *testing.T) {
	const old = `MemTotal:       16384000 kB
MemFree:          512000 kB
Buffers:          256000 kB
Cached:          8192000 kB
`
	totalMB, usedMB, ok := parseMeminfo(old)
	if !ok {
		t.Fatal("parse failed")
	}
	if totalMB != 16000 {
		t.Errorf("total = %dMB, want 16000", totalMB)
	}
	// available ≈ free+buffers+cached = 8960000 kB → used = 7424000 kB = 7250 MB
	if usedMB != 7250 {
		t.Errorf("used = %dMB, want 7250", usedMB)
	}
}

func TestParseMeminfoRejectsMissingTotal(t *testing.T) {
	if _, _, ok := parseMeminfo("MemFree: 100 kB\n"); ok {
		t.Error("expected failure without MemTotal")
	}
}

const sampleNetDev = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000 10 0 0 0 0 0 0 2000 20 0 0 0 0 0 0
  eth0: 5000 50 0 0 0 0 0 0 7000 70 0 0 0 0 0 0
docker0: 9000 90 0 0 0 0 0 0 9000 90 0 0 0 0 0 0
veth1a2b: 300 3 0 0 0 0 0 0 400 4 0 0 0 0 0 0
`

// Container traffic crosses both the veth/bridge pair and the physical NIC.
// Counting the virtual interfaces would double every byte a game server sends.
func TestSumNetDevCountsOnlyPhysicalInterfaces(t *testing.T) {
	physical := func(name string) (bool, bool) { return name == "eth0", true }
	rx, tx, ok := sumNetDev(sampleNetDev, physical)
	if !ok {
		t.Fatal("expected a reading")
	}
	if rx != 5000 || tx != 7000 {
		t.Errorf("rx/tx = %d/%d, want 5000/7000 (eth0 only)", rx, tx)
	}
}

// When sysfs can't classify anything, the name heuristic takes over rather than
// the node reporting no network at all.
func TestSumNetDevFallsBackToNameHeuristic(t *testing.T) {
	unknown := func(string) (bool, bool) { return false, false }
	rx, tx, ok := sumNetDev(sampleNetDev, unknown)
	if !ok {
		t.Fatal("expected the fallback to produce a reading")
	}
	if rx != 5000 || tx != 7000 {
		t.Errorf("rx/tx = %d/%d, want 5000/7000 — lo, docker0 and veth must be skipped", rx, tx)
	}
}

func TestSumNetDevEmptyInput(t *testing.T) {
	if _, _, ok := sumNetDev("header only\n", func(string) (bool, bool) { return true, true }); ok {
		t.Error("expected no reading from a file with no interface rows")
	}
}

func TestPhysicalIfaceUsesSysfsDeviceLink(t *testing.T) {
	sys := t.TempDir()
	mustMkdir(t, filepath.Join(sys, "class", "net", "eth0", "device"))
	mustMkdir(t, filepath.Join(sys, "class", "net", "docker0"))
	r := &hostReader{sys: sys}

	if physical, known := r.physicalIface("eth0"); !physical || !known {
		t.Errorf("eth0: physical=%v known=%v, want true/true", physical, known)
	}
	if physical, known := r.physicalIface("docker0"); physical || !known {
		t.Errorf("docker0: physical=%v known=%v, want false/true", physical, known)
	}
	// An interface sysfs has never heard of means sysfs can't answer at all.
	if _, known := r.physicalIface("ghost0"); known {
		t.Error("ghost0: expected known=false so the caller falls back")
	}
}

// A zone that names a CPU sensor wins over a hotter unrelated one — on a laptop
// the hottest zone is often the battery or the wireless card.
func TestThermalZonePrefersCPUSensor(t *testing.T) {
	sys := t.TempDir()
	writeZone(t, sys, "thermal_zone0", "BAT0", "88000")
	writeZone(t, sys, "thermal_zone1", "x86_pkg_temp", "54000")
	r := &hostReader{sys: sys}

	got, ok := r.thermalZoneTemp()
	if !ok {
		t.Fatal("expected a temperature")
	}
	if got != 54 {
		t.Errorf("temp = %.1f°C, want 54 (the CPU zone, not the hotter battery)", got)
	}
}

func TestThermalZoneFallsBackToHottest(t *testing.T) {
	sys := t.TempDir()
	writeZone(t, sys, "thermal_zone0", "acpitz", "45000")
	writeZone(t, sys, "thermal_zone1", "acpitz", "61000")
	r := &hostReader{sys: sys}

	got, ok := r.thermalZoneTemp()
	if !ok {
		t.Fatal("expected a temperature")
	}
	if got != 61 {
		t.Errorf("temp = %.1f°C, want 61", got)
	}
}

// Sensors in VMs report nonsense. A 0°C or 3000°C node band is worse than one
// that admits it has no sensor.
func TestThermalZoneRejectsImplausibleReadings(t *testing.T) {
	sys := t.TempDir()
	writeZone(t, sys, "thermal_zone0", "acpitz", "0")
	writeZone(t, sys, "thermal_zone1", "acpitz", "3000000")
	r := &hostReader{sys: sys}

	if got, ok := r.thermalZoneTemp(); ok {
		t.Errorf("expected no temperature, got %.1f°C", got)
	}
}

func TestThermalZoneAbsentEntirely(t *testing.T) {
	r := &hostReader{sys: t.TempDir()}
	if _, ok := r.thermalZoneTemp(); ok {
		t.Error("expected no temperature when there are no thermal zones")
	}
}

func TestReadDiskReportsRealFilesystem(t *testing.T) {
	var s hostSnapshot
	readDisk(t.TempDir(), &s)
	if !s.diskOK {
		t.Fatal("expected a statfs reading for a directory that exists")
	}
	if s.diskTotalMB <= 0 {
		t.Errorf("total = %dMB, want positive", s.diskTotalMB)
	}
	if s.diskUsedMB < 0 || s.diskUsedMB > s.diskTotalMB {
		t.Errorf("used = %dMB, want within 0..%d", s.diskUsedMB, s.diskTotalMB)
	}
}

func TestReadDiskMissingPath(t *testing.T) {
	// A path under a real directory resolves up to it (see resolveDiskPath), so
	// the only true "no reading" case is having no path at all.
	var empty hostSnapshot
	readDisk("", &empty)
	if empty.diskOK {
		t.Error("expected no reading for an empty path")
	}
}

// The full reader against a fixture tree: every source wired together.
func TestHostReaderAgainstFixtureTree(t *testing.T) {
	root := t.TempDir()
	proc, sys := filepath.Join(root, "proc"), filepath.Join(root, "sys")
	mustMkdir(t, filepath.Join(proc, "net"))
	mustWrite(t, filepath.Join(proc, "stat"), sampleProcStat)
	mustWrite(t, filepath.Join(proc, "meminfo"), "MemTotal: 16384000 kB\nMemAvailable: 12288000 kB\n")
	mustWrite(t, filepath.Join(proc, "net", "dev"), sampleNetDev)
	mustWrite(t, filepath.Join(proc, "uptime"), "3600.50 7200.00\n")
	mustMkdir(t, filepath.Join(sys, "class", "net", "eth0", "device"))
	mustMkdir(t, filepath.Join(sys, "class", "net", "docker0"))
	writeZone(t, sys, "thermal_zone0", "x86_pkg_temp", "47000")

	r := &hostReader{proc: proc, sys: sys, dataDir: t.TempDir()}
	s := r.read()

	if !s.cpuOK || s.cpuTotal != 1000 {
		t.Errorf("cpu: ok=%v total=%d", s.cpuOK, s.cpuTotal)
	}
	if !s.memOK || s.memUsedMB != 4000 {
		t.Errorf("mem: ok=%v used=%dMB", s.memOK, s.memUsedMB)
	}
	if !s.netOK || s.netRxBytes != 5000 {
		t.Errorf("net: ok=%v rx=%d", s.netOK, s.netRxBytes)
	}
	if !s.tempOK || s.tempC != 47 {
		t.Errorf("temp: ok=%v c=%.1f", s.tempOK, s.tempC)
	}
	if s.uptime.Seconds() != 3600.5 {
		t.Errorf("uptime = %v, want 3600.5s", s.uptime)
	}
	if !s.diskOK {
		t.Error("disk should read from the real filesystem under the data dir")
	}
}

// A degraded host — procfs present but unreadable — must produce a snapshot of
// unknowns, not a panic or a set of zeroes claiming to be real.
func TestHostReaderMissingProcfs(t *testing.T) {
	r := &hostReader{proc: filepath.Join(t.TempDir(), "nope"), sys: filepath.Join(t.TempDir(), "nope"), dataDir: ""}
	s := r.read()
	if s.cpuOK || s.memOK || s.netOK || s.tempOK || s.diskOK {
		t.Errorf("expected everything unknown, got %+v", s)
	}
	if s.at.IsZero() {
		t.Error("the snapshot should still be timestamped")
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeZone(t *testing.T, sys, zone, zType, milli string) {
	t.Helper()
	dir := filepath.Join(sys, "class", "thermal", zone)
	mustMkdir(t, dir)
	mustWrite(t, filepath.Join(dir, "type"), zType+"\n")
	mustWrite(t, filepath.Join(dir, "temp"), milli+"\n")
}

// The fixture tests above prove the parsers; this one proves they are pointed at
// the right files with the right field offsets on a real kernel. CI runs on
// Linux, so this exercises the production /proc path on every run.
func TestLinuxHostReaderAgainstRealHost(t *testing.T) {
	r := newHostReader(t.TempDir())
	first := r.read()

	if !first.cpuOK {
		t.Fatal("/proc/stat produced nothing")
	}
	if first.cpuCores <= 0 {
		t.Errorf("cores = %d, want positive", first.cpuCores)
	}
	if first.cpuBusy > first.cpuTotal {
		t.Errorf("busy %d exceeds total %d", first.cpuBusy, first.cpuTotal)
	}

	if !first.memOK {
		t.Fatal("/proc/meminfo produced nothing")
	}
	if first.memTotalMB <= 0 || first.memUsedMB > first.memTotalMB {
		t.Errorf("implausible memory: %d of %dMB", first.memUsedMB, first.memTotalMB)
	}

	if !first.diskOK || first.diskTotalMB <= 0 {
		t.Errorf("statfs: ok=%v total=%dMB", first.diskOK, first.diskTotalMB)
	}
	if first.uptime <= 0 {
		t.Errorf("uptime = %v, want positive", first.uptime)
	}

	// Counters must advance so the rate math has something to difference.
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
	// Network and temperature are environment-dependent (a container without
	// host networking, a VM with no thermal zone), so they are reported, not
	// required.
	t.Logf("host: cpu %.1f%% over %d cores, mem %d/%dMB, disk %d/%dMB, net known=%v, temp known=%v (%.1f°C), up %v",
		tel.CpuPercent, tel.CpuCores, tel.MemUsedMb, tel.MemTotalMb,
		tel.DiskUsedMb, tel.DiskTotalMb, tel.NetKnown, tel.TempKnown, tel.TempCelsius,
		second.uptime.Truncate(time.Second))
}
