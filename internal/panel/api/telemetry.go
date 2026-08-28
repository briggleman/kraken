package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// nodeTelemetryPollInterval is how often the Panel sweeps the fleet for host
// vitals. It also fixes how long a reading stays servable (see the stale
// factor), which is why the two live in one place rather than being wired
// separately from main.
const nodeTelemetryPollInterval = 5 * time.Second

// nodeTelemetryTimeout bounds one Agent round trip. Short on purpose: this poll
// runs on a fast cadence, and a node that can't answer in a second is better
// reported as "no data" than allowed to stall the sweep.
const nodeTelemetryTimeout = 2 * time.Second

// nodeTelemetryStaleFactor is how many poll intervals a cached reading survives
// before it stops being served. A node whose Agent has died keeps its last
// numbers only briefly — an operator staring at a frozen-but-plausible CPU
// figure is worse off than one staring at an empty instrument.
const nodeTelemetryStaleFactor = 3

// nodeVitals is one node's cached host telemetry plus when it arrived.
type nodeVitals struct {
	tel         *agentpb.NodeTelemetry
	fetched     time.Time
	unsupported bool // agent predates GetNodeTelemetry; stop logging about it
}

// telemetryCache holds the most recent vitals per node.
//
// Deliberately in memory rather than in Postgres: these numbers are meaningful
// for seconds, and persisting them would mean a write per node per poll forever
// to store data nobody reads twice. The cost is that a Panel restart shows
// empty instruments until the first sweep completes, which is a few seconds and
// reads as honest.
type telemetryCache struct {
	mu   sync.RWMutex
	byID map[string]nodeVitals
	ttl  time.Duration
}

func newTelemetryCache() *telemetryCache {
	return &telemetryCache{
		byID: make(map[string]nodeVitals),
		ttl:  nodeTelemetryPollInterval * nodeTelemetryStaleFactor,
	}
}

func (c *telemetryCache) put(nodeID string, tel *agentpb.NodeTelemetry, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.byID[nodeID]
	v.tel, v.fetched = tel, now
	c.byID[nodeID] = v
}

// markUnsupported records that this node's Agent has no telemetry RPC, so the
// poller stops re-logging it every sweep. Cleared by the next successful read
// (an agent update brings the RPC with it).
func (c *telemetryCache) markUnsupported(nodeID string) (firstTime bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.byID[nodeID]
	first := !v.unsupported
	v.unsupported = true
	v.tel = nil
	c.byID[nodeID] = v
	return first
}

// drop forgets a node's reading — used when the Agent stops answering, so the
// instruments empty out instead of freezing on the last good sample.
func (c *telemetryCache) drop(nodeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.byID[nodeID]; ok {
		v.tel = nil
		c.byID[nodeID] = v
	}
}

// forget removes a node entirely (it was deleted from the cluster).
func (c *telemetryCache) forget(nodeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byID, nodeID)
}

// fresh returns the cached telemetry for a node, or nil when there is none or
// it has aged out.
func (c *telemetryCache) fresh(nodeID string, now time.Time) *agentpb.NodeTelemetry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.byID[nodeID]
	if !ok || v.tel == nil || now.Sub(v.fetched) > c.ttl {
		return nil
	}
	return v.tel
}

// StartNodeTelemetryPoller launches a background loop that reads every reachable
// node's host vitals into the in-memory cache the telemetry endpoint serves.
//
// Separate from StartNodeReconciler on purpose. That loop is the slow one: it
// persists status transitions, pushes node config, checks cert expiry and can
// trigger DNS reconciliation, so it runs every 20s. Vitals want a much faster
// cadence and must not drag any of that along with them.
func (s *Server) StartNodeTelemetryPoller(ctx context.Context) {
	go func() {
		t := time.NewTicker(nodeTelemetryPollInterval)
		defer t.Stop()
		s.pollNodeTelemetryOnce(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.pollNodeTelemetryOnce(ctx)
			}
		}
	}()
}

func (s *Server) pollNodeTelemetryOnce(ctx context.Context) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return
	}
	live := make(map[string]bool, len(nodes))
	var wg sync.WaitGroup
	for _, n := range nodes {
		live[n.ID] = true
		// An offline node has nothing to say and dialing it just burns the
		// timeout every sweep; the reconciler owns bringing it back.
		if n.Status == cluster.NodeOffline {
			s.telemetry.drop(n.ID)
			continue
		}
		wg.Add(1)
		go func(n *cluster.Node) {
			defer wg.Done()
			s.pollOneNode(ctx, n)
		}(n)
	}
	wg.Wait()

	// Nodes deleted since the last sweep shouldn't linger in the cache.
	s.telemetry.mu.RLock()
	var stale []string
	for id := range s.telemetry.byID {
		if !live[id] {
			stale = append(stale, id)
		}
	}
	s.telemetry.mu.RUnlock()
	for _, id := range stale {
		s.telemetry.forget(id)
	}
}

func (s *Server) pollOneNode(ctx context.Context, n *cluster.Node) {
	client, err := s.nodes.Client(n.DialTarget())
	if err != nil {
		s.telemetry.drop(n.ID)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, nodeTelemetryTimeout)
	defer cancel()
	tel, err := client.GetNodeTelemetry(cctx, &agentpb.GetNodeTelemetryRequest{})
	if err != nil {
		// An Agent older than this RPC is a normal state in a fleet mid-upgrade,
		// not a fault: report no telemetry, leave the node's health alone, and
		// say so once rather than every sweep.
		if status.Code(err) == codes.Unimplemented {
			if s.telemetry.markUnsupported(n.ID) {
				s.logger.Info("node telemetry unsupported by agent; update it to see host vitals",
					"node", n.ID, "name", n.Name, "agent_version", n.AgentVersion)
			}
			return
		}
		s.telemetry.drop(n.ID)
		return
	}
	s.telemetry.put(n.ID, tel, time.Now())
}

// nodeTelemetryBody is one node's vitals as the browser sees them. Every group
// carries its *_known flag verbatim from the Agent: the UI renders an unknown
// group as an empty instrument, and a zero here would be indistinguishable from
// a real reading of zero.
type nodeTelemetryBody struct {
	TsUnixMs      int64 `json:"ts_unix_ms"`
	UptimeSeconds int64 `json:"uptime_seconds,omitempty"`

	CPUPercent float64 `json:"cpu_percent"`
	CPUCores   int32   `json:"cpu_cores,omitempty"`
	CPUKnown   bool    `json:"cpu_known"`

	MemTotalMB int64 `json:"mem_total_mb"`
	MemUsedMB  int64 `json:"mem_used_mb"`
	MemKnown   bool  `json:"mem_known"`

	DiskPath    string `json:"disk_path,omitempty"`
	DiskTotalMB int64  `json:"disk_total_mb"`
	DiskUsedMB  int64  `json:"disk_used_mb"`
	DiskKnown   bool   `json:"disk_known"`

	NetRxBps float64 `json:"net_rx_bps"`
	NetTxBps float64 `json:"net_tx_bps"`
	NetKnown bool    `json:"net_known"`

	TempCelsius float64 `json:"temp_celsius"`
	TempKnown   bool    `json:"temp_known"`
}

func telemetryBody(t *agentpb.NodeTelemetry) nodeTelemetryBody {
	return nodeTelemetryBody{
		TsUnixMs:      t.GetTsUnixMs(),
		UptimeSeconds: t.GetUptimeSeconds(),
		CPUPercent:    t.GetCpuPercent(),
		CPUCores:      t.GetCpuCores(),
		CPUKnown:      t.GetCpuKnown(),
		MemTotalMB:    t.GetMemTotalMb(),
		MemUsedMB:     t.GetMemUsedMb(),
		MemKnown:      t.GetMemKnown(),
		DiskPath:      t.GetDiskPath(),
		DiskTotalMB:   t.GetDiskTotalMb(),
		DiskUsedMB:    t.GetDiskUsedMb(),
		DiskKnown:     t.GetDiskKnown(),
		NetRxBps:      t.GetNetRxBps(),
		NetTxBps:      t.GetNetTxBps(),
		NetKnown:      t.GetNetKnown(),
		TempCelsius:   t.GetTempCelsius(),
		TempKnown:     t.GetTempKnown(),
	}
}

// handleNodeTelemetry serves the cached host vitals for every node that has a
// fresh reading. Nodes with no data are simply absent from the map rather than
// present with zeroes — the client renders "no data" for anything missing, so
// absence is the one representation that can't be mistaken for a measurement.
func (s *Server) handleNodeTelemetry(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.store.ListNodes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list nodes")
		return
	}
	now := time.Now()
	out := make(map[string]nodeTelemetryBody, len(nodes))
	for _, n := range nodes {
		if tel := s.telemetry.fresh(n.ID, now); tel != nil {
			out[n.ID] = telemetryBody(tel)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": out})
}
