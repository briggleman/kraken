package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/powerbudget"
)

func isInstallRunning(err error) bool {
	return grpcstatus.Code(err) == codes.FailedPrecondition && strings.Contains(err.Error(), "install pass is running")
}

// While an install pass runs, neither a START nor a RESTART may reach the
// container — and a RESTART is refused before its stop, so the server is left
// exactly as it was.
func TestPower_RefusedWhileInstalling(t *testing.T) {
	d := newFileOpsRuntime(t)
	ops := &fakeOps{}
	d.containers = ops
	leave := d.installs.enter(guardServer)

	for _, action := range []agentpb.PowerAction{agentpb.PowerAction_POWER_ACTION_START, agentpb.PowerAction_POWER_ACTION_RESTART} {
		if _, err := d.Power(context.Background(), guardServer, action); !isInstallRunning(err) {
			t.Errorf("%v during an install: want FailedPrecondition, got %v", action, err)
		}
	}
	if ops.stops != 0 || ops.inspects != 0 {
		t.Errorf("a refused power action touched the daemon: %d stops, %d inspects", ops.stops, ops.inspects)
	}

	leave()
	if err := d.installs.check(guardServer); err != nil {
		t.Errorf("the gate must open once the pass ends: %v", err)
	}
}

// The crash watchdog restarts through ensureAndStart. While an install runs it
// must not create or start a container on the data dir.
func TestEnsureAndStart_WatchdogRefusedWhileInstalling(t *testing.T) {
	d := newFileOpsRuntime(t)
	ops := &fakeOps{}
	d.containers = ops
	defer d.installs.enter(guardServer)()

	if err := d.ensureAndStart(context.Background(), guardServer, keepImage); !isInstallRunning(err) {
		t.Fatalf("want the install gate's refusal, got %v", err)
	}
	if ops.inspects != 0 || len(ops.removed) != 0 {
		t.Errorf("the refused restart reached the daemon: %d inspects, removed %v", ops.inspects, ops.removed)
	}
}

// A RESTART whose stop fails must say so. Carrying on would find the container
// still running, keep it, and report STARTING for a restart that never
// happened.
func TestPower_RestartReturnsAFailedStop(t *testing.T) {
	d := newFileOpsRuntime(t)
	ops := &fakeOps{stopErr: errors.New("daemon hung up")}
	d.containers = ops
	_, err := d.Power(context.Background(), guardServer, agentpb.PowerAction_POWER_ACTION_RESTART)
	if err == nil || !strings.Contains(err.Error(), "daemon hung up") {
		t.Fatalf("want the stop's error, got %v", err)
	}
	if ops.inspects != 0 {
		t.Error("the restart went on to ensure the container after its stop failed")
	}
}

// The same gate on the fake, as the Panel sees it over gRPC: a START that
// arrives while a pass is running is refused, and works once it is done.
func TestFakePower_StartRefusedDuringInstall(t *testing.T) {
	f := NewFakeRuntime("n", "linux", false, "test", WithFakeInstallDelay(100*time.Millisecond))
	done := make(chan error, 1)
	go func() {
		done <- f.Install(context.Background(), &agentpb.InstallServerRequest{ServerId: guardServer, InstallScript: "steamcmd"},
			func(*agentpb.InstallEvent) error { return nil })
	}()
	deadline := time.Now().Add(3 * time.Second)
	for len(f.InstallScripts(guardServer)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the install never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := f.Power(context.Background(), guardServer, agentpb.PowerAction_POWER_ACTION_START); !isInstallRunning(err) {
		t.Errorf("START during an install: want FailedPrecondition, got %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := f.Power(context.Background(), guardServer, agentpb.PowerAction_POWER_ACTION_START); err != nil {
		t.Errorf("START after the install: %v", err)
	}
}

// The Agent's own budget and the shared one the Panel's deadlines are derived
// from must agree; if the Agent's name wait or pull budget changes, so must
// powerbudget.
func TestPowerRPCBudgetMatchesTheSharedBudget(t *testing.T) {
	if containerNameFreeWait != powerbudget.NameFreeWait {
		t.Errorf("name wait: agent %v, powerbudget %v", containerNameFreeWait, powerbudget.NameFreeWait)
	}
	for _, a := range []agentpb.PowerAction{
		agentpb.PowerAction_POWER_ACTION_START, agentpb.PowerAction_POWER_ACTION_STOP,
		agentpb.PowerAction_POWER_ACTION_RESTART, agentpb.PowerAction_POWER_ACTION_KILL,
	} {
		for _, os := range []string{"linux", "windows"} {
			if got, want := PowerRPCBudget(a, os), powerbudget.Worst(a, os); got != want {
				t.Errorf("%v on %s: agent %v, powerbudget %v", a, os, got, want)
			}
			if PowerRPCBudget(a, os) > powerbudget.Deadline(a) {
				t.Errorf("%v on %s: the Panel's deadline %v is under the budget", a, os, powerbudget.Deadline(a))
			}
		}
	}
}
