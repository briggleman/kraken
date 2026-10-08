package api

import (
	"errors"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/updatecheck"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// Every outcome of the start's update decision (#392), and the line the
// console gets for it. The decision is one function with no Agent and no
// store in it precisely so this table can hold it.
func TestDecideUpdatePass(t *testing.T) {
	plain := &store.Server{}
	owed := &store.Server{UpdatePassOwed: true}
	known := &store.Server{Build: store.ServerBuild{InstalledBuild: "77"}}
	cases := []struct {
		name     string
		sv       *store.Server
		res      updatecheck.Result
		checkErr error
		action   startAction
		line     string
	}{
		{"current", plain, updatecheck.Result{Status: updatecheck.StatusCurrent, InstalledBuild: "100", AvailableBuild: "100"}, nil,
			startSkipCurrent, "[panel] build 100 is current, skipping the update pass"},
		{"builds differ", plain, updatecheck.Result{Status: updatecheck.StatusAvailable, InstalledBuild: "100", AvailableBuild: "101"}, nil,
			startRunPass, "[panel] build 100 → 101, running the update pass"},
		{"unsupported spec", plain, updatecheck.Result{Status: updatecheck.StatusUnsupported}, nil,
			startRunPass, "[panel] this game has no build to check, running the update pass"},
		{"agent predates the check", plain, updatecheck.Result{Status: updatecheck.StatusUnknown, Error: "the agent on node n predates the build check", AgentPredates: true}, nil,
			startRunPass, "[panel] update check unavailable (the agent on node n predates the build check), running the update pass"},
		// Could not reach the node or the Agent, or ran out of time: start on
		// what is there.
		{"node unreachable, build known", plain, updatecheck.Result{Status: updatecheck.StatusUnknown, InstalledBuild: "100", Error: "ask node n: unavailable", Unreachable: true}, nil,
			startSkipCheckFailed, "[panel] update check failed (ask node n: unavailable), starting on installed build 100"},
		{"node unreachable, earlier build on the row", known, updatecheck.Result{Status: updatecheck.StatusUnknown, Error: "node n is offline", Unreachable: true, InstalledUnread: true}, nil,
			startSkipCheckFailed, "[panel] update check failed (node n is offline), starting on installed build 77"},
		{"timed out, nothing known", plain, updatecheck.Result{Status: updatecheck.StatusUnknown, Error: "the check ran out of time", Unreachable: true}, nil,
			startSkipCheckFailed, "[panel] update check failed (the check ran out of time), starting on the installed tree"},
		// A failure about the server itself: run the pass, as before #392.
		{"manifest missing", known, updatecheck.Result{Status: updatecheck.StatusUnknown, Error: "no manifest", InstalledUnread: true}, nil,
			startRunPass, "[panel] update check could not read the installed build (no manifest), running the update pass"},
		{"unknown app on steam", plain, updatecheck.Result{Status: updatecheck.StatusUnknown, InstalledBuild: "100", Error: "steam: no app info for app 1"}, nil,
			startRunPass, "[panel] update check could not get Steam's current build (steam: no app info for app 1), running the update pass"},
		{"the check itself errored", plain, updatecheck.Result{}, errors.New("load node: boom"),
			startRunPass, "[panel] update check failed (load node: boom), running the update pass"},
		{"a variable edit owes the pass", owed, updatecheck.Result{Status: updatecheck.StatusCurrent, InstalledBuild: "100", AvailableBuild: "100"}, nil,
			startRunPass, "[panel] launch variables changed since the last install, running the update pass"},
	}
	for _, tc := range cases {
		got := decideUpdatePass(tc.sv, tc.res, tc.checkErr)
		if got.action != tc.action || got.line != tc.line {
			t.Errorf("%s: got (%v, %q), want (%v, %q)", tc.name, got.action, got.line, tc.action, tc.line)
		}
	}
}

// freshCurrentBuild — what turns the Settings tab's reason into
// current_build — holds only for a recent check that compared equal builds,
// with no variable edit owing the pass.
func TestFreshCurrentBuild(t *testing.T) {
	sp := &spec.Spec{SteamAppIDs: map[string]int{"linux": 1}, Install: spec.Install{Script: "app_update 1"}}
	now := time.Now()
	at := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	build := func(checked *time.Time, installed, available, errText string) store.ServerBuild {
		return store.ServerBuild{InstalledBuild: installed, AvailableBuild: available, CheckedAt: checked, CheckError: errText}
	}
	cases := []struct {
		name string
		sv   store.Server
		want bool
	}{
		{"fresh and current", store.Server{Kind: spec.LinuxNative, Build: build(at(time.Minute), "1", "1", "")}, true},
		{"stale", store.Server{Kind: spec.LinuxNative, Build: build(at(11*time.Minute), "1", "1", "")}, false},
		{"fresh but behind", store.Server{Kind: spec.LinuxNative, Build: build(at(time.Minute), "1", "2", "")}, false},
		{"fresh but failed", store.Server{Kind: spec.LinuxNative, Build: build(at(time.Minute), "1", "1", "x")}, false},
		{"from the future", store.Server{Kind: spec.LinuxNative, Build: build(at(-time.Hour), "1", "1", "")}, false},
		{"owed", store.Server{Kind: spec.LinuxNative, UpdatePassOwed: true, Build: build(at(time.Minute), "1", "1", "")}, false},
	}
	for _, tc := range cases {
		if got := freshCurrentBuild(&tc.sv, sp, now); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
