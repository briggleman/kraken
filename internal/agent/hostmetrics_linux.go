//go:build linux

package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// hostReader reads host vitals from procfs and sysfs. The roots are fields so
// tests can point them at a fixture tree; production always uses /proc and /sys.
//
// A containerized Agent reads the host's numbers here as long as it runs with
// host networking and the data dir bind-mounted from the host (which is how
// deploy/docker-compose.full.yml runs it): /proc/stat and /proc/meminfo are
// host-wide regardless of the container, /proc/net/dev follows the network
// namespace, and the statfs target follows the bind mount.
type hostReader struct {
	proc    string
	sys     string
	dataDir string
}

func newHostReader(dataDir string) *hostReader {
	return &hostReader{proc: "/proc", sys: "/sys", dataDir: dataDir}
}

func (r *hostReader) read() hostSnapshot {
	s := hostSnapshot{at: time.Now(), diskPath: r.dataDir}
	r.readCPU(&s)
	r.readMem(&s)
	r.readNet(&s)
	r.readTemp(&s)
	r.readUptime(&s)
	readDisk(r.dataDir, &s)
	return s
}

func (r *hostReader) readCPU(s *hostSnapshot) {
	b, err := os.ReadFile(filepath.Join(r.proc, "stat")) // #nosec G304 -- procfs root, fixed relative path
	if err != nil {
		warnHostMetricOnce("cpu", err)
		return
	}
	busy, total, cores, ok := parseProcStat(string(b))
	s.cpuBusy, s.cpuTotal, s.cpuCores, s.cpuOK = busy, total, cores, ok
}

// parseProcStat sums the aggregate "cpu" line into busy and total counters and
// counts the per-core "cpuN" lines.
//
// Fields are user nice system idle iowait irq softirq steal [guest guest_nice].
// The guest fields are deliberately excluded from the total: the kernel already
// counts guest time inside user and nice, so adding them again inflates the
// denominator and reports the host as less busy than it is.
func parseProcStat(text string) (busy, total uint64, cores int, ok bool) {
	for line := range strings.SplitSeq(text, "\n") {
		if !strings.HasPrefix(line, "cpu") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] != "cpu" {
			// "cpu0", "cpu1", … — one per logical core.
			if _, err := strconv.Atoi(strings.TrimPrefix(fields[0], "cpu")); err == nil {
				cores++
			}
			continue
		}
		var idle uint64
		for i, f := range fields[1:] {
			if i >= 8 {
				break // guest/guest_nice — already counted in user/nice
			}
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return 0, 0, 0, false
			}
			total += v
			if i == 3 || i == 4 { // idle, iowait
				idle += v
			}
		}
		if total == 0 {
			return 0, 0, 0, false
		}
		busy = total - idle
		ok = true
	}
	return busy, total, cores, ok
}

func (r *hostReader) readMem(s *hostSnapshot) {
	b, err := os.ReadFile(filepath.Join(r.proc, "meminfo")) // #nosec G304 -- procfs root, fixed relative path
	if err != nil {
		warnHostMetricOnce("memory", err)
		return
	}
	totalMB, usedMB, ok := parseMeminfo(string(b))
	s.memTotalMB, s.memUsedMB, s.memOK = totalMB, usedMB, ok
}

// parseMeminfo returns total and used memory in MB.
//
// Used is total minus MemAvailable, not total minus MemFree: the page cache is
// reclaimable on demand, so counting it as used reports a healthy Linux box as
// permanently near capacity. MemAvailable is the kernel's own estimate of what
// a new workload could take. On kernels too old to publish it (pre-3.14) we
// approximate with free + buffers + cached.
func parseMeminfo(text string) (totalMB, usedMB int64, ok bool) {
	var totalKB, availKB, freeKB, buffersKB, cachedKB int64
	var haveAvail bool
	for line := range strings.SplitSeq(text, "\n") {
		key, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			totalKB = v
		case "MemAvailable":
			availKB, haveAvail = v, true
		case "MemFree":
			freeKB = v
		case "Buffers":
			buffersKB = v
		case "Cached":
			cachedKB = v
		}
	}
	if totalKB <= 0 {
		return 0, 0, false
	}
	if !haveAvail {
		availKB = freeKB + buffersKB + cachedKB
	}
	if availKB > totalKB {
		availKB = totalKB
	}
	return totalKB / 1024, (totalKB - availKB) / 1024, true
}

func (r *hostReader) readNet(s *hostSnapshot) {
	b, err := os.ReadFile(filepath.Join(r.proc, "net", "dev")) // #nosec G304 -- procfs root, fixed relative path
	if err != nil {
		warnHostMetricOnce("network", err)
		return
	}
	rx, tx, ok := sumNetDev(string(b), r.physicalIface)
	s.netRxBytes, s.netTxBytes, s.netOK = rx, tx, ok
}

// physicalIface reports whether an interface is real hardware, by asking sysfs
// whether it has a backing device. Virtual interfaces — lo, veth pairs, docker0,
// bridges — have no device link, and counting them double-counts every packet a
// container sends through the host's NIC.
//
// Returns false, false when sysfs can't answer, which sends sumNetDev to its
// name-based fallback rather than reporting an empty interface set.
func (r *hostReader) physicalIface(name string) (physical, known bool) {
	if _, err := os.Stat(filepath.Join(r.sys, "class", "net", name, "device")); err == nil {
		return true, true
	} else if !os.IsNotExist(err) {
		return false, false
	}
	if _, err := os.Stat(filepath.Join(r.sys, "class", "net", name)); err != nil {
		return false, false // no sysfs at all — caller falls back
	}
	return false, true
}

// virtualIfacePrefixes are the interface names to skip when sysfs is unavailable
// and physicality has to be guessed from the name.
var virtualIfacePrefixes = []string{
	"lo", "veth", "docker", "br-", "virbr", "tun", "tap", "kube", "cni", "flannel", "dummy",
}

// sumNetDev totals receive and transmit bytes across the host's physical
// interfaces. isPhysical answers per interface; when it reports "unknown" for
// every interface, the name heuristic takes over so a host with an unusual
// sysfs layout still reports throughput instead of nothing.
func sumNetDev(text string, isPhysical func(string) (physical, known bool)) (rx, tx uint64, ok bool) {
	type iface struct {
		name   string
		rx, tx uint64
	}
	var ifaces []iface
	for line := range strings.SplitSeq(text, "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found {
			continue // the two header lines
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		fields := strings.Fields(rest)
		// rx_bytes … (8 receive columns) … tx_bytes at index 8.
		if len(fields) < 9 {
			continue
		}
		rxB, err1 := strconv.ParseUint(fields[0], 10, 64)
		txB, err2 := strconv.ParseUint(fields[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		ifaces = append(ifaces, iface{name, rxB, txB})
	}
	if len(ifaces) == 0 {
		return 0, 0, false
	}

	var anyKnown bool
	for _, i := range ifaces {
		physical, known := isPhysical(i.name)
		if known {
			anyKnown = true
		}
		if known && physical {
			rx += i.rx
			tx += i.tx
			ok = true
		}
	}
	if anyKnown {
		// sysfs answered; an empty result means this host genuinely has no
		// physical interfaces (a VM with only virtio-less virtual NICs), and
		// zero throughput is the honest reading.
		return rx, tx, true
	}

	rx, tx = 0, 0
	for _, i := range ifaces {
		if hasAnyPrefix(i.name, virtualIfacePrefixes) {
			continue
		}
		rx += i.rx
		tx += i.tx
		ok = true
	}
	return rx, tx, ok
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// cpuThermalTypes are thermal-zone type names that name a CPU sensor. Preferred
// over the hottest-zone fallback, which on a laptop can be the battery or a
// wireless card.
var cpuThermalTypes = []string{"x86_pkg_temp", "cpu-thermal", "coretemp", "k10temp", "soc_thermal", "cpu_thermal"}

func (r *hostReader) readTemp(s *hostSnapshot) {
	if c, ok := r.thermalZoneTemp(); ok {
		s.tempC, s.tempOK = c, true
		return
	}
	if c, ok := r.hwmonTemp(); ok {
		s.tempC, s.tempOK = c, true
	}
}

// thermalZoneTemp scans /sys/class/thermal. A zone whose type names a CPU sensor
// wins outright; otherwise the hottest plausible zone stands in.
func (r *hostReader) thermalZoneTemp() (float64, bool) {
	zones, err := filepath.Glob(filepath.Join(r.sys, "class", "thermal", "thermal_zone*"))
	if err != nil || len(zones) == 0 {
		return 0, false
	}
	var hottest float64
	var found bool
	for _, z := range zones {
		milli, ok := readMilliDegrees(filepath.Join(z, "temp"))
		if !ok {
			continue
		}
		found = true
		if milli > hottest {
			hottest = milli
		}
		zType, err := os.ReadFile(filepath.Join(z, "type")) // #nosec G304 -- sysfs root, globbed zone dir
		if err != nil {
			continue
		}
		if hasAnyPrefix(strings.TrimSpace(string(zType)), cpuThermalTypes) {
			return milli, true
		}
	}
	return hottest, found
}

// hwmonTemp is the fallback for hosts with no thermal zones: read temp1_input
// from an hwmon device whose name is a known CPU sensor driver.
func (r *hostReader) hwmonTemp() (float64, bool) {
	devs, err := filepath.Glob(filepath.Join(r.sys, "class", "hwmon", "hwmon*"))
	if err != nil {
		return 0, false
	}
	for _, d := range devs {
		name, err := os.ReadFile(filepath.Join(d, "name")) // #nosec G304 -- sysfs root, globbed hwmon dir
		if err != nil || !hasAnyPrefix(strings.TrimSpace(string(name)), cpuThermalTypes) {
			continue
		}
		if c, ok := readMilliDegrees(filepath.Join(d, "temp1_input")); ok {
			return c, true
		}
	}
	return 0, false
}

// plausibleTempC bounds a temperature reading to a range a CPU could actually
// report. Thermal sensors go haywire in VMs and on odd hardware — a 0°C or
// 3000°C reading is a broken sensor, and rendering it as fact is worse than
// rendering nothing.
func plausibleTempC(v float64) bool { return v > 0 && v < 150 }

// readMilliDegrees reads a sysfs temperature file (millidegrees Celsius) and
// converts it, rejecting readings outside what a CPU could plausibly report.
func readMilliDegrees(path string) (float64, bool) {
	b, err := os.ReadFile(path) // #nosec G304 -- sysfs root, fixed leaf name
	if err != nil {
		return 0, false
	}
	milli, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil {
		return 0, false
	}
	c := milli / 1000
	if !plausibleTempC(c) {
		return 0, false
	}
	return c, true
}

func (r *hostReader) readUptime(s *hostSnapshot) {
	b, err := os.ReadFile(filepath.Join(r.proc, "uptime")) // #nosec G304 -- procfs root, fixed relative path
	if err != nil {
		return
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return
	}
	s.uptime = time.Duration(secs * float64(time.Second))
}

// readDisk fills the disk fields from a statfs of the data directory. Used is
// total minus free (not minus available): the root-reserved blocks are consumed
// capacity from the operator's point of view, which is how df reports it too.
func readDisk(dir string, s *hostSnapshot) {
	dir = resolveDiskPath(dir)
	if dir == "" {
		return
	}
	s.diskPath = dir
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		warnHostMetricOnce("disk", err)
		return
	}
	bsize := uint64(st.Bsize)
	if bsize == 0 || st.Blocks == 0 {
		return
	}
	const mb = 1024 * 1024
	s.diskTotalMB = int64(st.Blocks * bsize / mb)
	s.diskUsedMB = int64((st.Blocks - st.Bfree) * bsize / mb)
	s.diskOK = true
}
