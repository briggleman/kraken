package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/container"
)

// listingOps is a containerOps whose only working method is ContainerList: it
// records the options it was asked with and answers with a fixed listing or a
// fixed error. Anything else panics on the nil embedded interface, which is
// the test saying NodeInfo's roll call reached for more than a listing.
type listingOps struct {
	containerOps

	containers []container.Summary
	err        error
	asked      container.ListOptions
}

func (l *listingOps) ContainerList(_ context.Context, opts container.ListOptions) ([]container.Summary, error) {
	l.asked = opts
	return l.containers, l.err
}

func managedSummary(serverID, name, state string) container.Summary {
	return container.Summary{
		Names:  []string{"/" + name},
		Labels: map[string]string{labelManaged: "true", labelServerID: serverID},
		State:  container.ContainerState(state),
	}
}

// The real Agent's roll call (#385): every managed game container in any state,
// lowercase state words, the live ones counted, the install container left out
// in every state, and the marker set only when the listing answered.
func TestReportManagedContainers(t *testing.T) {
	ops := &listingOps{containers: []container.Summary{
		managedSummary("srv-a", "kraken_srv-a", "running"),
		managedSummary("srv-b", "kraken_srv-b", "Exited"),
		managedSummary("srv-b", "kraken_srv-b_install", "exited"),
		managedSummary("srv-c", "kraken_srv-c_install", "running"),
	}}
	list, live, reported := reportManagedContainers(context.Background(), ops)

	if !ops.asked.All {
		t.Errorf("listed without All: true — stopped containers would never be reported")
	}
	if got := ops.asked.Filters.Get("label"); len(got) != 1 || got[0] != labelManaged+"=true" {
		t.Errorf("label filter = %v, want %s=true", got, labelManaged)
	}
	if !reported {
		t.Errorf("reported = false after a listing that answered")
	}
	if live != 1 {
		t.Errorf("running_servers = %d, want 1 — only the live game container counts", live)
	}
	if len(list) != 2 {
		t.Fatalf("list = %+v, want the two game containers and no install container", list)
	}
	if list[0].GetServerId() != "srv-a" || list[0].GetState() != "running" || list[0].GetContainerName() != "kraken_srv-a" {
		t.Errorf("first = %+v, want srv-a kraken_srv-a running", list[0])
	}
	if list[1].GetServerId() != "srv-b" || list[1].GetState() != "exited" {
		t.Errorf("second = %+v, want srv-b with Docker's state lowercased to exited", list[1])
	}
}

// Paused and restarting containers hold memory and ports, so they count as
// live: the same set `docker ps` without -a lists, which is what running_servers
// always meant to an older Panel.
func TestReportManagedContainersCountsPausedAndRestartingAsLive(t *testing.T) {
	ops := &listingOps{containers: []container.Summary{
		managedSummary("srv-a", "kraken_srv-a", "paused"),
		managedSummary("srv-b", "kraken_srv-b", "restarting"),
		managedSummary("srv-c", "kraken_srv-c", "created"),
		managedSummary("srv-d", "kraken_srv-d", "dead"),
	}}
	list, live, _ := reportManagedContainers(context.Background(), ops)
	if live != 2 || len(list) != 4 {
		t.Errorf("live %d of %d listed, want 2 of 4", live, len(list))
	}
}

// A listing that failed is not an empty node: no list, no count, no marker, so
// the Panel falls back to the count instead of saying there are no containers.
func TestReportManagedContainersListErrorLeavesTheMarkerUnset(t *testing.T) {
	ops := &listingOps{err: errors.New("daemon hung up")}
	list, live, reported := reportManagedContainers(context.Background(), ops)
	if reported || live != 0 || len(list) != 0 {
		t.Errorf("after a failed listing: reported %v, live %d, list %+v — want false, 0, empty", reported, live, list)
	}
}
