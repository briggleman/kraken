package api

import (
	"testing"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// A container that only changed state is a different reading (#385): the web
// reads the state to tell an offline server's stopped container from none, so
// a stop must reach the store even though no id or name moved.
func TestSameManagedContainersComparesState(t *testing.T) {
	running := []cluster.ManagedContainer{{ServerID: "srv-a", ContainerName: "kraken_srv-a", State: "running"}}
	exited := []cluster.ManagedContainer{{ServerID: "srv-a", ContainerName: "kraken_srv-a", State: "exited"}}
	if sameManagedContainers(running, exited) {
		t.Errorf("running and exited compared equal")
	}
	if !sameManagedContainers(exited, []cluster.ManagedContainer{{ServerID: "srv-a", ContainerName: "kraken_srv-a", State: "exited"}}) {
		t.Errorf("identical readings compared different")
	}
}

// The conversion carries the state and sorts deterministically, so Docker's
// listing order cannot churn the record.
func TestManagedContainersCarriesState(t *testing.T) {
	got := managedContainers([]*agentpb.ManagedContainer{
		{ServerId: "srv-b", ContainerName: "kraken_srv-b", State: "exited"},
		{ServerId: "srv-a", ContainerName: "kraken_srv-a", State: "running"},
	})
	if len(got) != 2 || got[0].ServerID != "srv-a" || got[0].State != "running" || got[1].State != "exited" {
		t.Fatalf("managedContainers = %+v, want srv-a running then srv-b exited", got)
	}
}

// Running is what every "running" reader filters on: an explicit running state,
// or no state at all from an Agent that only ever reported running containers.
func TestManagedContainerRunning(t *testing.T) {
	for state, want := range map[string]bool{"running": true, "": true, "exited": false, "created": false, "dead": false, "paused": false} {
		if got := (cluster.ManagedContainer{State: state}).Running(); got != want {
			t.Errorf("Running() with state %q = %v, want %v", state, got, want)
		}
	}
}
