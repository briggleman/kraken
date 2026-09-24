package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel/api"
	"github.com/briggleman/kraken/internal/panel/store"
)

// The lock must be RELEASED when its holder finishes, or one start would
// leave the server unrestorable until the Panel restarts. Each path that
// claims it is run to completion and then a restore is asked for.
func TestTheStartLockIsReleasedWhenTheStartFinishes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state store.ServerState
		run   func(t *testing.T, r *restoreHarness)
	}{
		{"start then stop", store.StateOffline, func(t *testing.T, r *restoreHarness) {
			if code, body := r.power(t, "start"); code >= 400 {
				t.Fatalf("start: %d %s", code, body)
			}
			if code, body := r.power(t, "stop"); code >= 400 {
				t.Fatalf("stop: %d %s", code, body)
			}
		}},
		{"restart of a crashed server, then stop", store.StateCrashed, func(t *testing.T, r *restoreHarness) {
			if code, body := r.power(t, "restart"); code >= 400 {
				t.Fatalf("restart: %d %s", code, body)
			}
			if code, body := r.power(t, "stop"); code >= 400 {
				t.Fatalf("stop: %d %s", code, body)
			}
		}},
		{"node-scoped start (writes no row)", store.StateOffline, func(t *testing.T, r *restoreHarness) {
			rec := do(t, r.h, http.MethodPost, "/api/v1/nodes/"+r.nodeID+"/servers/"+r.id+"/power", r.token, map[string]string{"action": "start"})
			if rec.Code >= 400 {
				t.Fatalf("node-scoped start: %d %s", rec.Code, rec.Body.String())
			}
		}},
		{"scheduled restart of a crashed server", store.StateCrashed, func(t *testing.T, r *restoreHarness) {
			seedDueRestart(t, r.st, "sch-release", r.id)
			r.srv.RunDueSchedulesForTest(context.Background())
			// The Agent now reports it running; the operator stops it.
			if code, body := r.power(t, "stop"); code >= 400 {
				t.Fatalf("stop: %d %s", code, body)
			}
		}},
		{"reinstall", store.StateOffline, func(t *testing.T, r *restoreHarness) {
			rec := do(t, r.h, http.MethodPost, "/api/v1/servers/"+r.id+"/reinstall", r.token, nil)
			if rec.Code >= 400 {
				t.Fatalf("reinstall: %d %s", rec.Code, rec.Body.String())
			}
			waitForState(t, r.h, r.token, r.id, string(store.StateOffline))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRestoreHarness(t, nil, tc.state)
			tc.run(t, r)
			code, body := r.tryRestore(t)
			if strings.Contains(body, "server_busy") {
				t.Fatalf("a restore after the %s finished is still refused as busy: %d %s", tc.name, code, body)
			}
			if code != http.StatusAccepted {
				t.Errorf("restore after the %s: %d %s; want 202", tc.name, code, body)
			}
		})
	}
}

// ...and a finished restore releases the server for a start.
func TestARestoreReleasesTheServerWhenItFinishes(t *testing.T) {
	for _, failure := range []string{"", "gzip: invalid header; the live tree was not touched"} {
		name := "landed"
		var opts []agent.FakeOption
		if failure != "" {
			name = "failed"
			opts = append(opts, agent.WithFakeRestoreFailure(failure))
		}
		t.Run(name, func(t *testing.T) {
			r := newRestoreHarness(t, nil, store.StateOffline, opts...)
			r.restore(t)
			waitForRestoreView(t, r.h, r.token, r.id, settled)
			for _, action := range []string{"start", "stop", "restart"} {
				code, body := r.power(t, action)
				if strings.Contains(body, "server_restoring") || code >= 400 {
					t.Errorf("%s after a %s restore: %d %s", action, name, code, body)
				}
			}
		})
	}
}

// A start or reinstall writes back the row it read under its claim — never a
// copy from before it, which would erase what a restore that finished in
// between left there.
func TestAStartDoesNotEraseARestoreResultWrittenMeanwhile(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(t *testing.T, r *restoreHarness) int
	}{
		{"start", func(t *testing.T, r *restoreHarness) int { c, _ := r.power(t, "start"); return c }},
		{"reinstall", func(t *testing.T, r *restoreHarness) int {
			return do(t, r.h, http.MethodPost, "/api/v1/servers/"+r.id+"/reinstall", r.token, nil).Code
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRestoreHarness(t, nil, store.StateOffline)
			finished := time.Now().UTC().Truncate(time.Second)
			// Stands in for a restore that ended between the handler's first
			// read and its claim: its result lands on the row there.
			clear := api.SetRestoreClaimHookForTest(func(string) {
				r.setRow(t, func(s *store.Server) {
					s.RestoreResult = &store.RestoreResult{BackupID: restoreBackupID, OK: true, FinishedAt: finished}
				})
			})
			defer clear()
			if code := tc.call(t, r); code >= 400 {
				t.Fatalf("%s: %d", tc.name, code)
			}
			if tc.name == "reinstall" {
				waitForState(t, r.h, r.token, r.id, string(store.StateOffline))
			}
			row, _ := r.st.GetServer(context.Background(), r.id)
			if row.RestoreResult == nil || row.RestoreResult.BackupID != restoreBackupID || !row.RestoreResult.FinishedAt.Equal(finished) {
				t.Errorf("the %s wrote back a stale row and erased the restore's result: %+v", tc.name, row.RestoreResult)
			}
		})
	}
}
