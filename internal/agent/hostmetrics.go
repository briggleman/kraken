package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// hostSampleInterval is how often the sampler reads the host's counters. CPU and
// network are rates, so this — not the Panel's poll cadence — is the window they
// are measured over. Keeping it fixed and Agent-side means a Panel that polls
// irregularly (or two Panels polling at once) still sees stable numbers.
const hostSampleInterval = 2 * time.Second

// hostSnapshot is one raw reading of the host's counters. CPU and network are
// cumulative (monotonic since boot) and only become rates once differenced
// against the previous snapshot; memory, disk and temperature are already
// instantaneous. Each group carries its own ok flag because these come from
// different sources that fail independently.
type hostSnapshot struct {
	at     time.Time
	uptime time.Duration

	// cpuBusy/cpuTotal are in whatever unit the platform counts in (jiffies on
	// Linux, 100ns ticks on Windows); only their ratio is used.
	cpuBusy  uint64
	cpuTotal uint64
	cpuCores int
	cpuOK    bool

	memTotalMB int64
	memUsedMB  int64
	memOK      bool

	diskPath    string
	diskTotalMB int64
	diskUsedMB  int64
	diskOK      bool

	netRxBytes uint64
	netTxBytes uint64
	netOK      bool

	tempC  float64
	tempOK bool
}

// HostSampler reads host vitals on a fixed tick and holds the most recent
// telemetry for cheap reads. The Panel polls GetNodeTelemetry far more often
// than a node's numbers meaningfully change, so serving a precomputed snapshot
// keeps that RPC from doing filesystem or syscall work per caller.
//
// The zero value is not usable; construct with NewHostSampler.
type HostSampler struct {
	interval time.Duration
	read     func() hostSnapshot

	mu   sync.RWMutex
	prev hostSnapshot
	tel  *agentpb.NodeTelemetry // nil until the first snapshot lands
}

// NewHostSampler returns a sampler that reports disk usage for the filesystem
// holding dataDir — the one that fills up as game servers grow, and so the only
// disk an operator watching a node cares about.
func NewHostSampler(dataDir string) *HostSampler {
	r := newHostReader(dataDir)
	return &HostSampler{interval: hostSampleInterval, read: r.read}
}

// Start samples once immediately, then on every tick until ctx is cancelled.
// The immediate sample means memory, disk and temperature are available right
// away; CPU and network stay unknown until the second sample gives them an
// interval to be a rate over.
func (h *HostSampler) Start(ctx context.Context) {
	h.sample()
	go func() {
		t := time.NewTicker(h.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.sample()
			}
		}
	}()
}

func (h *HostSampler) sample() {
	cur := h.read()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tel = telemetryFrom(h.prev, cur)
	h.prev = cur
}

// Telemetry returns the most recent snapshot, or nil if none has been taken
// yet. The result is a clone: the sampler keeps publishing into its own copy,
// and a caller that holds this one should not see it change underneath.
func (h *HostSampler) Telemetry() *agentpb.NodeTelemetry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.tel == nil {
		return nil
	}
	return proto.Clone(h.tel).(*agentpb.NodeTelemetry)
}

// telemetryFrom differences two snapshots into a telemetry message. prev is the
// zero value on the first sample, which leaves the rate metrics unknown rather
// than reporting a bogus spike computed against a zero baseline.
func telemetryFrom(prev, cur hostSnapshot) *agentpb.NodeTelemetry {
	t := &agentpb.NodeTelemetry{
		TsUnixMs:      cur.at.UnixMilli(),
		UptimeSeconds: int64(cur.uptime.Seconds()),

		MemTotalMb: cur.memTotalMB,
		MemUsedMb:  cur.memUsedMB,
		MemKnown:   cur.memOK,

		DiskPath:    cur.diskPath,
		DiskTotalMb: cur.diskTotalMB,
		DiskUsedMb:  cur.diskUsedMB,
		DiskKnown:   cur.diskOK,

		TempCelsius: cur.tempC,
		TempKnown:   cur.tempOK,

		CpuCores: int32(cur.cpuCores),
	}

	elapsed := cur.at.Sub(prev.at).Seconds()
	haveInterval := !prev.at.IsZero() && elapsed > 0

	// A counter that went backwards means the host rebooted (or the interface
	// set changed) between samples. There is no meaningful rate across that
	// discontinuity, so report unknown and let the next interval recover.
	if haveInterval && prev.cpuOK && cur.cpuOK && cur.cpuTotal > prev.cpuTotal && cur.cpuBusy >= prev.cpuBusy {
		busy := float64(cur.cpuBusy - prev.cpuBusy)
		total := float64(cur.cpuTotal - prev.cpuTotal)
		pct := busy / total * 100
		t.CpuPercent = clampPercent(pct)
		t.CpuKnown = true
	}

	if haveInterval && prev.netOK && cur.netOK && cur.netRxBytes >= prev.netRxBytes && cur.netTxBytes >= prev.netTxBytes {
		t.NetRxBps = float64(cur.netRxBytes-prev.netRxBytes) / elapsed
		t.NetTxBps = float64(cur.netTxBytes-prev.netTxBytes) / elapsed
		t.NetKnown = true
	}

	return t
}

// resolveDiskPath walks up from dir to the first directory that exists.
//
// The data dir is created lazily, per server, so on a node that has never had
// one deployed it isn't there yet — and statfs on a missing path fails, which
// would leave the disk instrument permanently blank on exactly the fresh nodes
// an operator is most likely to be looking at. Every ancestor is on the same
// filesystem, so the reading is the same one; only the path label changes.
func resolveDiskPath(dir string) string {
	if dir == "" {
		return ""
	}
	for i := 0; i < 64; i++ { // bounded: a malformed path must not loop forever
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // reached the root without finding anything readable
		}
		dir = parent
	}
	return ""
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// logHostMetricOnce keeps a degraded metric source from filling the log: the
// same failing read happens every tick forever.
var (
	hostWarnMu   sync.Mutex
	hostWarnSeen = map[string]bool{}
)

func warnHostMetricOnce(what string, err error) {
	hostWarnMu.Lock()
	defer hostWarnMu.Unlock()
	if hostWarnSeen[what] {
		return
	}
	hostWarnSeen[what] = true
	slog.Debug("host metrics unavailable", "metric", what, "err", err)
}
