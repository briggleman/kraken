package api_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"testing"

	"google.golang.org/grpc"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// The container-drift badge could only ever say how many containers the Panel
// had lost track of, because a count is all the Agent sent (#340). It now sends
// the list as well, and these pin the Panel's half: the list is adopted onto the
// node record, it survives to the fleet list the UI reads, and an Agent too old
// to report it leaves the record with nothing rather than a stale roll call.
// From #385 the list carries every managed container with its state, and a
// marker says the Agent reported the whole list — so an empty one reads as "no
// containers" rather than "too old to say".

// containerListingRuntime is a fake Agent runtime whose reported managed
// containers can be set mid-test, standing in for a Docker daemon whose
// container set changes between reconciles. Unless reporting(true) is called it
// answers as an 0.54–0.58 Agent does: running containers only, no marker.
type containerListingRuntime struct {
	*agent.FakeRuntime

	mu         sync.Mutex
	containers []*agentpb.ManagedContainer
	reported   bool
}

func (r *containerListingRuntime) reporting(on bool) {
	r.mu.Lock()
	r.reported = on
	r.mu.Unlock()
}

func (r *containerListingRuntime) set(containers ...*agentpb.ManagedContainer) {
	r.mu.Lock()
	r.containers = containers
	r.mu.Unlock()
}

func (r *containerListingRuntime) NodeInfo(ctx context.Context) (*agentpb.NodeInfo, error) {
	info, err := r.FakeRuntime.NodeInfo(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	info.ManagedContainers = r.containers
	info.ContainersReported = r.reported
	// The count is the live subset (agentpb.ContainerStateLive), as the Docker runtime builds it; an entry
	// with no state is an older Agent's, and those were all running.
	info.RunningServers = 0
	for _, c := range r.containers {
		if agentpb.ContainerStateLive(c.GetState()) {
			info.RunningServers++
		}
	}
	r.mu.Unlock()
	return info, nil
}

// startAgentReportingContainers runs a real Agent gRPC server whose NodeInfo
// reports the given managed containers, and returns its address plus a handle
// for changing that set later.
func startAgentReportingContainers(t *testing.T, nodeID string, containers ...*agentpb.ManagedContainer) (string, *containerListingRuntime) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	rt := &containerListingRuntime{FakeRuntime: agent.NewFakeRuntime(nodeID, "linux", true, "test"), containers: containers}
	srv := grpc.NewServer()
	agentpb.RegisterNodeServiceServer(srv, agent.NewService(rt))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String(), rt
}

// nodeContainers reads the stored node's adopted container list off the fleet
// list — the same response the UI's badge reads.
func nodeContainers(t *testing.T, h http.Handler, token, id string) (int, []struct {
	ServerID      string `json:"server_id"`
	ContainerName string `json:"container_name"`
}) {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/v1/nodes", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list nodes: status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Nodes []struct {
			ID             string `json:"id"`
			RunningServers int    `json:"running_servers"`
			Managed        []struct {
				ServerID      string `json:"server_id"`
				ContainerName string `json:"container_name"`
			} `json:"managed_containers"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode nodes: %v", err)
	}
	for _, n := range resp.Nodes {
		if n.ID == id {
			return n.RunningServers, n.Managed
		}
	}
	t.Fatalf("node %s not in the fleet list", id)
	return 0, nil
}

// The whole point of the field: the Panel keeps the names, not just the count,
// so the badge can say which container it has no row for.
func TestNodeAdoptsTheAgentsManagedContainerList(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startAgentReportingContainers(t, "named-node",
		&agentpb.ManagedContainer{ServerId: "srv-b", ContainerName: "kraken_srv-b"},
		&agentpb.ManagedContainer{ServerId: "srv-a", ContainerName: "kraken_srv-a"},
	)
	id := registerNode(t, h, token, addr)
	pollNode(t, h, token, id)

	count, managed := nodeContainers(t, h, token, id)
	if count != 2 {
		t.Errorf("running_servers = %d, want 2 — the Agent sent two containers with no state, and an empty state counts as live", count)
	}
	if len(managed) != 2 {
		t.Fatalf("managed_containers = %+v, want both containers", managed)
	}
	// Stored sorted by server id, so the Docker listing order cannot churn the
	// record from one reconcile to the next.
	if managed[0].ServerID != "srv-a" || managed[1].ServerID != "srv-b" {
		t.Errorf("managed_containers = %+v, want them sorted by server id", managed)
	}
	if managed[0].ContainerName != "kraken_srv-a" {
		t.Errorf("container_name = %q, want the Agent's own name for it", managed[0].ContainerName)
	}
}

// The node-info endpoint is where an operator looks at one node directly, so it
// carries the list too.
func TestNodeInfoExposesTheManagedContainerList(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startAgentReportingContainers(t, "info-node",
		&agentpb.ManagedContainer{ServerId: "srv-a", ContainerName: "kraken_srv-a"},
	)
	id := registerNode(t, h, token, addr)

	rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+id+"/info", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
	var info struct {
		RunningServers int `json:"running_servers"`
		Managed        []struct {
			ServerID      string `json:"server_id"`
			ContainerName string `json:"container_name"`
		} `json:"managed_containers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	if info.RunningServers != 1 || len(info.Managed) != 1 {
		t.Fatalf("info = %+v, want one container named alongside the count", info)
	}
	if info.Managed[0].ServerID != "srv-a" || info.Managed[0].ContainerName != "kraken_srv-a" {
		t.Errorf("info container = %+v, want srv-a / kraken_srv-a", info.Managed[0])
	}
}

// A container that goes away has to leave the record, and an Agent that stops
// reporting the list at all (a downgrade to a build that predates the field)
// must not leave a snapshot behind that nothing will ever refresh.
func TestManagedContainerListIsRefreshedAndCleared(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startAgentReportingContainers(t, "churny",
		&agentpb.ManagedContainer{ServerId: "srv-a", ContainerName: "kraken_srv-a"},
		&agentpb.ManagedContainer{ServerId: "srv-b", ContainerName: "kraken_srv-b"},
	)
	id := registerNode(t, h, token, addr)
	pollNode(t, h, token, id)
	if _, managed := nodeContainers(t, h, token, id); len(managed) != 2 {
		t.Fatalf("precondition: managed_containers = %+v, want 2", managed)
	}

	rt.set(&agentpb.ManagedContainer{ServerId: "srv-a", ContainerName: "kraken_srv-a"})
	pollNode(t, h, token, id)
	count, managed := nodeContainers(t, h, token, id)
	if count != 1 || len(managed) != 1 || managed[0].ServerID != "srv-a" {
		t.Errorf("after one container stopped: count %d, list %+v — want just srv-a", count, managed)
	}

	rt.set()
	pollNode(t, h, token, id)
	if count, managed := nodeContainers(t, h, token, id); count != 0 || len(managed) != 0 {
		t.Errorf("after the Agent reported none: count %d, list %+v — want both empty", count, managed)
	}
}

// nodeContainerStates reads the stored node's containers with their states and
// the reported marker off the fleet list.
func nodeContainerStates(t *testing.T, h http.Handler, token, id string) (running int, reported bool, managed []struct {
	ServerID string `json:"server_id"`
	State    string `json:"state"`
}) {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/v1/nodes", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list nodes: status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Nodes []struct {
			ID             string `json:"id"`
			RunningServers int    `json:"running_servers"`
			Reported       bool   `json:"containers_reported"`
			Managed        []struct {
				ServerID string `json:"server_id"`
				State    string `json:"state"`
			} `json:"managed_containers"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode nodes: %v", err)
	}
	for _, n := range resp.Nodes {
		if n.ID == id {
			return n.RunningServers, n.Reported, n.Managed
		}
	}
	t.Fatalf("node %s not in the fleet list", id)
	return 0, false, nil
}

// An Agent that reports every container stores each with its state and the
// marker; the count stays the running subset an older Panel reads; a state
// change alone is a change worth storing; an empty reported list keeps the
// marker (that is the whole point of it); and a downgrade clears it (#385).
func TestNodeStoresContainerStatesAndTheReportedMarker(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startAgentReportingContainers(t, "stately",
		&agentpb.ManagedContainer{ServerId: "srv-a", ContainerName: "kraken_srv-a", State: "running"},
		&agentpb.ManagedContainer{ServerId: "srv-b", ContainerName: "kraken_srv-b", State: "exited"},
	)
	rt.reporting(true)
	id := registerNode(t, h, token, addr)

	// The node-info endpoint carries the marker straight from the Agent.
	rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+id+"/info", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
	var info struct {
		Reported bool `json:"containers_reported"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	if !info.Reported {
		t.Errorf("node info containers_reported = false, want true from an Agent that sets it")
	}

	running, reported, managed := nodeContainerStates(t, h, token, id)
	if !reported {
		t.Errorf("stored containers_reported = false, want true")
	}
	if running != 1 {
		t.Errorf("running_servers = %d, want 1 — the stopped container is listed, not counted", running)
	}
	if len(managed) != 2 || managed[0].State != "running" || managed[1].State != "exited" {
		t.Fatalf("managed_containers = %+v, want srv-a running and srv-b exited", managed)
	}

	// srv-a stops: same ids, same names, only the state moved — still stored.
	rt.set(
		&agentpb.ManagedContainer{ServerId: "srv-a", ContainerName: "kraken_srv-a", State: "exited"},
		&agentpb.ManagedContainer{ServerId: "srv-b", ContainerName: "kraken_srv-b", State: "exited"},
	)
	pollNode(t, h, token, id)
	running, _, managed = nodeContainerStates(t, h, token, id)
	if running != 0 || len(managed) != 2 || managed[0].State != "exited" {
		t.Errorf("after srv-a stopped: count %d, list %+v — want 0 running, srv-a exited", running, managed)
	}

	// No containers at all, reported as such: the marker stays, the list goes.
	rt.set()
	pollNode(t, h, token, id)
	running, reported, managed = nodeContainerStates(t, h, token, id)
	if !reported || running != 0 || len(managed) != 0 {
		t.Errorf("empty reported list: reported %v, count %d, list %+v — want reported, 0, none", reported, running, managed)
	}

	// Rolled back to an Agent that predates the marker: it clears.
	rt.reporting(false)
	pollNode(t, h, token, id)
	if _, reported, _ := nodeContainerStates(t, h, token, id); reported {
		t.Errorf("after a downgrade containers_reported is still true — the web would trust a running-only list")
	}
}
