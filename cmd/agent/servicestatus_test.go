package main

import (
	"strings"
	"testing"
	"time"
)

// healthyFacts is the configuration `--service install` leaves behind, as the
// SCM would read it back. Every case below starts here and breaks one thing —
// the point of the status command is that each of those breaks is visible.
func healthyFacts(cmdLine string) serviceFacts {
	return serviceFacts{
		CommandLine:      cmdLine,
		StartType:        "automatic",
		DelayedAutoStart: true,
		Recovery: []serviceRecoveryStep{
			{Action: "restart", Delay: 5 * time.Second},
			{Action: "restart", Delay: 30 * time.Second},
			{Action: "restart", Delay: 60 * time.Second},
		},
		ResetPeriod:      24 * time.Hour,
		NonCrashFailures: true,
	}
}

const testCmdLine = `C:\kraken\bin\kraken-agent.exe --root C:\kraken`

// The expectation must be exactly what a healthy install reads back, or status
// reports drift on a service that is perfectly configured. This is the test that
// fails if the recovery policy in servicestatus.go and the SCM writer in
// service_windows.go ever stop agreeing.
func TestExpectedServiceFactsMatchesAHealthyInstall(t *testing.T) {
	rows := compareServiceConfig(healthyFacts(testCmdLine), expectedServiceFacts(testCmdLine))
	if drift := serviceConfigDrift(rows); len(drift) != 0 {
		t.Errorf("a healthy install must report no drift, got %v", drift)
	}
	for _, r := range rows {
		if !r.Match {
			t.Errorf("row %q reported a mismatch: actual %q expected %q", r.Field, r.Actual, r.Expected)
		}
	}
}

// One row per field install asserts, always in the same order — the table is
// what an operator reads, and a silently dropped row is a field that stops being
// checked (which is exactly how #184 hid).
func TestCompareServiceConfigCoversEveryAssertedField(t *testing.T) {
	rows := compareServiceConfig(healthyFacts(testCmdLine), expectedServiceFacts(testCmdLine))
	want := []string{
		"command line",
		"start type",
		"delayed auto-start",
		"recovery actions",
		"failure-count reset",
		"recovery on non-crash failures",
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		if rows[i].Field != w {
			t.Errorf("row %d is %q, want %q", i, rows[i].Field, w)
		}
	}
}

// Each break is detected, on the row it belongs to, and nowhere else.
func TestCompareServiceConfigDetectsDrift(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*serviceFacts)
		field  string
	}{{
		// The #184 specimen: a service registered before the recovery policy
		// existed keeps one legacy restart slot through every binary upgrade.
		"legacy recovery actions",
		func(f *serviceFacts) {
			f.Recovery = []serviceRecoveryStep{{Action: "restart", Delay: time.Minute}}
		},
		"recovery actions",
	}, {
		// #114's flag: without it SCM ignores a clean nonzero exit, so the
		// self-update revert path never gets its restarts.
		"non-crash failures off",
		func(f *serviceFacts) { f.NonCrashFailures = false },
		"recovery on non-crash failures",
	}, {
		"manual start",
		func(f *serviceFacts) { f.StartType = "manual" },
		"start type",
	}, {
		"no delayed auto-start",
		func(f *serviceFacts) { f.DelayedAutoStart = false },
		"delayed auto-start",
	}, {
		"command line lost its flags",
		func(f *serviceFacts) { f.CommandLine = `C:\kraken\bin\kraken-agent.exe` },
		"command line",
	}, {
		"reset period is the SCM default",
		func(f *serviceFacts) { f.ResetPeriod = 0 },
		"failure-count reset",
	}, {
		// Right delays, wrong actions: SCM would reboot the HOST three times
		// instead of restarting the service.
		"reboot instead of restart",
		func(f *serviceFacts) {
			for i := range f.Recovery {
				f.Recovery[i].Action = "reboot"
			}
		},
		"recovery actions",
	}, {
		// Same actions, wrong order — SCM runs them in sequence, so a 60s-first
		// service is slower to come back than the policy says.
		"restart delays out of order",
		func(f *serviceFacts) {
			f.Recovery[0], f.Recovery[2] = f.Recovery[2], f.Recovery[0]
		},
		"recovery actions",
	}}

	for _, c := range cases {
		actual := healthyFacts(testCmdLine)
		c.mutate(&actual)
		rows := compareServiceConfig(actual, expectedServiceFacts(testCmdLine))

		drift := serviceConfigDrift(rows)
		if len(drift) != 1 {
			t.Errorf("%s: got %d drift lines, want exactly 1: %v", c.name, len(drift), drift)
			continue
		}
		if !strings.Contains(drift[0], c.field) {
			t.Errorf("%s: drift line %q should name the %q field", c.name, drift[0], c.field)
		}
		for _, r := range rows {
			if r.Match == (r.Field == c.field) {
				t.Errorf("%s: row %q match = %v, want %v", c.name, r.Field, r.Match, r.Field != c.field)
			}
		}
	}
}

// Recovery actions are compared as a rendered sequence, so the rendering has to
// carry both the action and the delay: a service left with "restart after 5s"
// alone must not read the same as the three-slot policy.
func TestFormatRecovery(t *testing.T) {
	if got, want := formatRecovery(nil), "none"; got != want {
		t.Errorf("formatRecovery(nil) = %q, want %q", got, want)
	}
	got := formatRecovery([]serviceRecoveryStep{
		{Action: "restart", Delay: 5 * time.Second},
		{Action: "reboot", Delay: 2 * time.Minute},
	})
	if want := "restart after 5s, reboot after 2m0s"; got != want {
		t.Errorf("formatRecovery = %q, want %q", got, want)
	}
}

// The table has to be readable: a matching row shows the value once, a drifting
// row shows both sides, and every row carries a marker.
func TestFormatServiceConfigTable(t *testing.T) {
	actual := healthyFacts(testCmdLine)
	actual.NonCrashFailures = false
	lines := formatServiceConfigTable(compareServiceConfig(actual, expectedServiceFacts(testCmdLine)))

	var ok, drift int
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "  ok "):
			ok++
		case strings.HasPrefix(l, "  DRIFT "):
			drift++
		}
	}
	if ok != 5 || drift != 1 {
		t.Errorf("got %d ok rows and %d drift rows, want 5 and 1:\n%s", ok, drift, strings.Join(lines, "\n"))
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "actual:   no") || !strings.Contains(joined, "expected: yes") {
		t.Errorf("a drifting row must show both sides:\n%s", joined)
	}
	if !strings.Contains(joined, testCmdLine) {
		t.Errorf("the table must show the command line verbatim:\n%s", joined)
	}
}

// The exit codes are a documented contract (--help, deploy/windows/README.md)
// that scripts branch on: 0 in sync, 1 drift, anything else "don't trust it".
func TestServiceStatusExitCodeContract(t *testing.T) {
	cases := []struct {
		name      string
		got, want int
	}{
		{"in sync", exitServiceInSync, 0},
		{"drift", exitServiceDrift, 1},
		{"not installed / SCM error", exitServiceUnavailable, 2},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("exit code for %s = %d, want %d", c.name, c.got, c.want)
		}
	}
}
