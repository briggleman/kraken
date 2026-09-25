package agent

import (
	"context"
	"testing"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// NodeInfo reports every managed container named with its state, and the
// running ones counted — the count for a Panel too old to read the list (it
// always meant running), the list for one that can (#340, #385). The two come
// from one walk precisely so they cannot disagree: a count that outran its own
// list would render as drift the Panel invented out of nothing.
func TestFakeNodeInfoCountsOnlyRunningAndListsEveryContainer(t *testing.T) {
	ctx := context.Background()
	rt := NewFakeRuntime("fake-node", "linux", true, "test")

	for _, id := range []string{"srv-b", "srv-a", "srv-c"} {
		if err := rt.Create(ctx, &agentpb.ServerSpec{ServerId: id}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	info, err := rt.NodeInfo(ctx)
	if err != nil {
		t.Fatalf("NodeInfo: %v", err)
	}
	// Nothing has started, so nothing has a container — and the Agent says so
	// explicitly rather than leaving an empty list to be read as "too old".
	if !info.GetContainersReported() || len(info.GetManagedContainers()) != 0 || info.GetRunningServers() != 0 {
		t.Fatalf("before any start: reported %v, list %+v, count %d — want reported, empty, 0",
			info.GetContainersReported(), info.GetManagedContainers(), info.GetRunningServers())
	}

	// Two up, one never started — a server that never started has no container.
	for _, id := range []string{"srv-a", "srv-b"} {
		if _, err := rt.Power(ctx, id, agentpb.PowerAction_POWER_ACTION_START); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
	}
	info, err = rt.NodeInfo(ctx)
	if err != nil {
		t.Fatalf("NodeInfo: %v", err)
	}
	managed := info.GetManagedContainers()
	if info.GetRunningServers() != 2 || len(managed) != 2 {
		t.Fatalf("running_servers = %d, managed_containers = %+v, want the two running servers", info.GetRunningServers(), managed)
	}
	// Sorted, so a caller comparing two readings sees a change only when the set
	// actually changed.
	if managed[0].GetServerId() != "srv-a" || managed[1].GetServerId() != "srv-b" {
		t.Errorf("managed_containers = %+v, want srv-a then srv-b", managed)
	}
	for _, c := range managed {
		if c.GetContainerName() != "kraken_"+c.GetServerId() {
			t.Errorf("container_name for %s = %q, want the kraken_<id> name the runtime uses", c.GetServerId(), c.GetContainerName())
		}
		if c.GetState() != "running" {
			t.Errorf("state for %s = %q, want running", c.GetServerId(), c.GetState())
		}
	}

	// A stop leaves the container behind, exited: still listed, no longer counted.
	if _, err := rt.Power(ctx, "srv-a", agentpb.PowerAction_POWER_ACTION_STOP); err != nil {
		t.Fatalf("stop srv-a: %v", err)
	}
	info, err = rt.NodeInfo(ctx)
	if err != nil {
		t.Fatalf("NodeInfo after stop: %v", err)
	}
	managed = info.GetManagedContainers()
	if info.GetRunningServers() != 1 || len(managed) != 2 {
		t.Fatalf("after one stop: count %d, list %+v — want 1 running of 2 listed", info.GetRunningServers(), managed)
	}
	if managed[0].GetServerId() != "srv-a" || managed[0].GetState() != "exited" {
		t.Errorf("stopped container = %+v, want srv-a exited", managed[0])
	}
	if managed[1].GetServerId() != "srv-b" || managed[1].GetState() != "running" {
		t.Errorf("running container = %+v, want srv-b running", managed[1])
	}

	// An install pass clears the stopped container ahead of itself, as the Docker
	// runtime's guard does: the node then has no container for srv-a at all.
	err = rt.Install(ctx, &agentpb.InstallServerRequest{ServerId: "srv-a"}, func(*agentpb.InstallEvent) error { return nil })
	if err != nil {
		t.Fatalf("install srv-a: %v", err)
	}
	info, err = rt.NodeInfo(ctx)
	if err != nil {
		t.Fatalf("NodeInfo after install: %v", err)
	}
	if managed := info.GetManagedContainers(); len(managed) != 1 || managed[0].GetServerId() != "srv-b" {
		t.Errorf("after the pass: list %+v, want only srv-b", managed)
	}

	// And a removal takes the container away entirely.
	if err := rt.Remove(ctx, "srv-b", false); err != nil {
		t.Fatalf("remove srv-b: %v", err)
	}
	info, err = rt.NodeInfo(ctx)
	if err != nil {
		t.Fatalf("NodeInfo after remove: %v", err)
	}
	if !info.GetContainersReported() || len(info.GetManagedContainers()) != 0 || info.GetRunningServers() != 0 {
		t.Errorf("after remove: reported %v, list %+v, count %d — want reported, empty, 0",
			info.GetContainersReported(), info.GetManagedContainers(), info.GetRunningServers())
	}
}

// containerDisplayName is what turns Docker's name list into the name an
// operator sees; the leading slash is Docker's, not ours.
func TestContainerDisplayName(t *testing.T) {
	for _, tc := range []struct {
		names []string
		want  string
	}{
		{[]string{"/kraken_srv-a"}, "kraken_srv-a"},
		{[]string{"/kraken_srv-a", "/alias"}, "kraken_srv-a"},
		{[]string{"kraken_srv-a"}, "kraken_srv-a"},
		{nil, ""},
	} {
		if got := containerDisplayName(tc.names); got != tc.want {
			t.Errorf("containerDisplayName(%v) = %q, want %q", tc.names, got, tc.want)
		}
	}
}
