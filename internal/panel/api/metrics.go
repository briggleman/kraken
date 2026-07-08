package api

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/version"
)

// Process-wide metrics counters. One Panel runs per process, so package-level
// state is appropriate and avoids threading a registry through every handler.
var (
	metricsReqMu      sync.Mutex
	metricsReqTotal   = map[reqKey]int64{}
	metricsAuditTotal atomic.Int64
)

type reqKey struct {
	method string
	code   int
}

// knownMethods bounds the label cardinality of kraken_http_requests_total —
// r.Method is client-controlled, so unknown verbs are bucketed as "OTHER" to
// prevent unbounded map growth (a memory-exhaustion vector).
var knownMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true,
	"DELETE": true, "OPTIONS": true, "CONNECT": true, "TRACE": true,
}

func incRequest(method string, code int) {
	if !knownMethods[method] {
		method = "OTHER"
	}
	metricsReqMu.Lock()
	metricsReqTotal[reqKey{method, code}]++
	metricsReqMu.Unlock()
}

// metricsMiddleware counts every HTTP request by method + status code.
func (s *Server) metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		incRequest(r.Method, rec.status)
	})
}

// handleMetrics renders Prometheus text-format exposition. Gauges are computed
// from the store at scrape time; counters are accumulated in-process.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	fmt.Fprintf(w, "# HELP kraken_build_info Panel build information.\n")
	fmt.Fprintf(w, "# TYPE kraken_build_info gauge\n")
	fmt.Fprintf(w, "kraken_build_info{version=%q} 1\n", version.Version)

	// Servers by state.
	if servers, err := s.store.ListServers(ctx); err == nil {
		byState := map[store.ServerState]int{
			store.StateInstalling: 0, store.StateInstallFailed: 0, store.StateOffline: 0,
			store.StateStarting: 0, store.StateRunning: 0, store.StateStopping: 0,
			store.StateCrashed: 0,
		}
		for _, sv := range servers {
			byState[sv.State]++
		}
		fmt.Fprintf(w, "# HELP kraken_servers Number of servers by lifecycle state.\n")
		fmt.Fprintf(w, "# TYPE kraken_servers gauge\n")
		states := make([]string, 0, len(byState))
		for st := range byState {
			states = append(states, string(st))
		}
		sort.Strings(states)
		for _, st := range states {
			fmt.Fprintf(w, "kraken_servers{state=%q} %d\n", st, byState[store.ServerState(st)])
		}
	}

	// Nodes total + online.
	if nodes, err := s.store.ListNodes(ctx); err == nil {
		online := 0
		for _, n := range nodes {
			if n.Status == cluster.NodeOnline {
				online++
			}
		}
		fmt.Fprintf(w, "# HELP kraken_nodes_total Registered nodes.\n# TYPE kraken_nodes_total gauge\nkraken_nodes_total %d\n", len(nodes))
		fmt.Fprintf(w, "# HELP kraken_nodes_online Nodes currently online.\n# TYPE kraken_nodes_online gauge\nkraken_nodes_online %d\n", online)
	}

	// Schedules total.
	if scheds, err := s.store.ListSchedules(ctx); err == nil {
		fmt.Fprintf(w, "# HELP kraken_schedules_total Configured scheduled tasks.\n# TYPE kraken_schedules_total gauge\nkraken_schedules_total %d\n", len(scheds))
	}

	// Audit events recorded since process start.
	fmt.Fprintf(w, "# HELP kraken_audit_events_total Audit events recorded since start.\n")
	fmt.Fprintf(w, "# TYPE kraken_audit_events_total counter\n")
	fmt.Fprintf(w, "kraken_audit_events_total %d\n", metricsAuditTotal.Load())

	// HTTP requests by method + code.
	fmt.Fprintf(w, "# HELP kraken_http_requests_total HTTP requests by method and status code.\n")
	fmt.Fprintf(w, "# TYPE kraken_http_requests_total counter\n")
	metricsReqMu.Lock()
	keys := make([]reqKey, 0, len(metricsReqTotal))
	for k := range metricsReqTotal {
		keys = append(keys, k)
	}
	snapshot := make(map[reqKey]int64, len(metricsReqTotal))
	for k, v := range metricsReqTotal {
		snapshot[k] = v
	}
	metricsReqMu.Unlock()
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].method != keys[j].method {
			return keys[i].method < keys[j].method
		}
		return keys[i].code < keys[j].code
	})
	for _, k := range keys {
		fmt.Fprintf(w, "kraken_http_requests_total{method=%q,code=\"%d\"} %d\n", k.method, k.code, snapshot[k])
	}
}
