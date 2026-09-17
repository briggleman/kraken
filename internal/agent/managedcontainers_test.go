package agent

import (
	"context"
	"testing"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// NodeInfo reports its running containers twice — once as a count for a Panel
// too old to read the list, once named for one that can (#340). The two come
// from one walk precisely so they cannot disagree: a count that outran its own
// list would render as drift the Panel invented out of nothing.
func TestFakeNodeInfoCountAndListAgree(t *testing.T) {
	ctx := context.Background()
	rt := NewFakeRuntime("fake-node", "linux", true, "test")

	for _, id := range []string{"srv-b", "srv-a", "srv-c"} {
		if err := rt.Create(ctx, &agentpb.ServerSpec{ServerId: id}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	// Two up, one left stopped — a stopped server has no container to report.
	for _, id := range []string{"srv-a", "srv-b"} {
		if _, err := rt.Power(ctx, id, agentpb.PowerAction_POWER_ACTION_START); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
	}

	info, err := rt.NodeInfo(ctx)
	if err != nil {
		t.Fatalf("NodeInfo: %v", err)
	}
	managed := info.GetManagedContainers()
	if int(info.GetRunningServers()) != len(managed) {
		t.Fatalf("running_servers = %d but the list carries %d — they are the same set", info.GetRunningServers(), len(managed))
	}
	if len(managed) != 2 {
		t.Fatalf("managed_containers = %+v, want the two running servers", managed)
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
	}

	// And a stop takes its container out of both readings at once.
	if _, err := rt.Power(ctx, "srv-a", agentpb.PowerAction_POWER_ACTION_STOP); err != nil {
		t.Fatalf("stop srv-a: %v", err)
	}
	info, err = rt.NodeInfo(ctx)
	if err != nil {
		t.Fatalf("NodeInfo after stop: %v", err)
	}
	if info.GetRunningServers() != 1 || len(info.GetManagedContainers()) != 1 {
		t.Fatalf("after one stop: count %d, list %+v — want one of each", info.GetRunningServers(), info.GetManagedContainers())
	}
	if got := info.GetManagedContainers()[0].GetServerId(); got != "srv-b" {
		t.Errorf("remaining container = %q, want srv-b", got)
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
